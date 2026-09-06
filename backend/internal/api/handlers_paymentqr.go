package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/paymentqr"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
	"github.com/tawilts/protovibe-merch/backend/internal/services/sales"
)

func (s *Server) registerPaymentQRRoutes(g *gin.RouterGroup) {
	// A seller shows codes; only a band admin changes where the money goes.
	sellers := g.Group("/payment-qr", requireAuth(), requireBandRole(models.RoleSeller))
	sellers.GET("/availability", s.paymentQRAvailability)
	sellers.POST("/intents", s.createPaymentQRIntent)
	sellers.POST("/intents/:token/cancel", s.cancelPaymentQRIntent)

	admins := g.Group("/payment-qr", requireAuth(), requireBandRole(models.RoleBandAdmin))
	admins.GET("/settings", s.getPaymentQRSettings)
	admins.PUT("/settings", requireBandAccount(), s.savePaymentQRSettings)
}

func (s *Server) paymentQRAvailability(c *gin.Context) {
	availability, err := s.paymentQR.Availability(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, availability)
}

// paymentQRSettingsPayload never returns anything secret; a bank account is
// printed on every invoice anyway, and the band admin needs to verify it.
type paymentQRSettingsPayload struct {
	PayPalMeURL       string `json:"paypal_me_url"`
	BankAccountHolder string `json:"bank_account_holder"`
	BankIBAN          string `json:"bank_iban"`
	BankBIC           string `json:"bank_bic"`
}

func (s *Server) getPaymentQRSettings(c *gin.Context) {
	settings, err := s.paymentQR.Settings(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, paymentQRSettingsPayload{
		PayPalMeURL:       settings.PayPalMeURL,
		BankAccountHolder: settings.BankAccountHolder,
		BankIBAN:          settings.BankIBAN,
		BankBIC:           settings.BankBIC,
	})
}

func (s *Server) savePaymentQRSettings(c *gin.Context) {
	var req paymentQRSettingsPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()
	settings, err := s.paymentQR.SaveSettings(ctx, models.PaymentQRSettings{
		PayPalMeURL:       req.PayPalMeURL,
		BankAccountHolder: req.BankAccountHolder,
		BankIBAN:          req.BankIBAN,
		BankBIC:           req.BankBIC,
	}, state.User.ID, state.User.Username)
	if err != nil {
		s.reportPaymentQRError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "payment_qr.settings_changed", EntityType: "payment_qr_settings",
		Details: map[string]any{
			"paypal_configured": settings.PayPalMeURL != "",
			"bank_configured":   settings.BankIBAN != "",
		},
	})
	c.JSON(http.StatusOK, paymentQRSettingsPayload{
		PayPalMeURL:       settings.PayPalMeURL,
		BankAccountHolder: settings.BankAccountHolder,
		BankIBAN:          settings.BankIBAN,
		BankBIC:           settings.BankBIC,
	})
}

type createIntentRequest struct {
	Method string `json:"method" binding:"required"`
	// Sale is the basket the code is for. It is stored with the reservation so
	// confirming the payment books exactly what the customer scanned.
	Sale sales.Request `json:"sale"`
}

// createPaymentQRIntent reserves a receipt number and renders the code.
//
// Nothing is booked here: showing a code must never move stock or create a
// ledger row, because a customer can always walk away mid-scan.
func (s *Server) createPaymentQRIntent(c *gin.Context) {
	var req createIntentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Sale.SoldOn.IsZero() {
		req.Sale.SoldOn = s.today()
	}

	ctx := c.Request.Context()

	// The amount is computed from the catalogue rather than taken from the
	// client, so a tampered request cannot show a customer the wrong total.
	amountCents, err := s.sales.QuoteTotal(ctx, req.Sale)
	if err != nil {
		s.reportSalesError(c, err)
		return
	}

	var descriptions []string
	if req.Method == models.PaymentMethodTransfer {
		descriptions, err = s.sales.PaymentQRDescriptions(ctx, req.Sale.Items)
		if err != nil {
			s.reportSalesError(c, err)
			return
		}
	}

	encoded, err := json.Marshal(req.Sale)
	if err != nil {
		serverError(c, err)
		return
	}

	state := stateFrom(c)
	var intent *paymentqr.Intent
	// Allocation scans reservations and therefore cannot lock a row that does
	// not exist yet. If two devices choose the same next number concurrently,
	// the unique constraint picks a winner and the loser simply allocates the
	// next number instead of exposing a sporadic 500 error.
	for attempt := 0; attempt < 4; attempt++ {
		receiptID, allocateErr := s.receipts.Allocate(
			ctx, receipt.PrefixSale, req.Sale.ReceiptID, req.Sale.SoldOn, "",
		)
		if allocateErr != nil {
			serverError(c, allocateErr)
			return
		}
		intent, err = s.paymentQR.CreateIntent(ctx, req.Method, amountCents, receiptID,
			string(encoded), state.User.ID, descriptions)
		if !errors.Is(err, paymentqr.ErrReceiptReserved) {
			break
		}
	}
	if err != nil {
		s.reportPaymentQRError(c, err)
		return
	}
	c.JSON(http.StatusCreated, intent)
}

func (s *Server) cancelPaymentQRIntent(c *gin.Context) {
	token := c.Param("token")
	if err := s.paymentQR.CancelIntent(c.Request.Context(), token); err != nil {
		s.reportPaymentQRError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) reportPaymentQRError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, paymentqr.ErrIntentNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such payment code")
	case errors.Is(err, paymentqr.ErrNotConfigured):
		fail(c, http.StatusConflict, "payment_not_configured",
			"this payment method is not configured for the band")
	case errors.Is(err, paymentqr.ErrUnknownMethod):
		fail(c, http.StatusBadRequest, "unknown_payment_method", err.Error())
	case errors.Is(err, paymentqr.ErrNoAmount):
		fail(c, http.StatusBadRequest, "invalid_amount", err.Error())
	case errors.Is(err, paymentqr.ErrAmountTooLarge):
		fail(c, http.StatusBadRequest, "invalid_amount", err.Error())
	case errors.Is(err, paymentqr.ErrInvalidIBAN), errors.Is(err, paymentqr.ErrMissingIBAN):
		fail(c, http.StatusBadRequest, "invalid_iban", err.Error())
	case errors.Is(err, paymentqr.ErrInvalidPayPalURL):
		fail(c, http.StatusBadRequest, "invalid_paypal_url", err.Error())
	case errors.Is(err, paymentqr.ErrInvalidBIC):
		fail(c, http.StatusBadRequest, "invalid_bic", err.Error())
	case errors.Is(err, paymentqr.ErrMissingHolder):
		fail(c, http.StatusBadRequest, "missing_account_holder", err.Error())
	case errors.Is(err, paymentqr.ErrPayloadTooLarge):
		fail(c, http.StatusBadRequest, "qr_payload_too_large", err.Error())
	case errors.Is(err, paymentqr.ErrReceiptReserved):
		fail(c, http.StatusConflict, "payment_receipt_busy",
			"a receipt number was reserved concurrently; please try again")
	default:
		serverError(c, err)
	}
}
