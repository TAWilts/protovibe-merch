package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/receipt"
	"github.com/tawilts/protovibe-merch/backend/internal/services/sales"
)

func (s *Server) registerSalesRoutes(g *gin.RouterGroup) {
	sell := g.Group("", requireAuth(), requireBandRole(models.RoleSeller))
	sell.GET("/receipt-preview", s.receiptPreview)
	sell.POST("/sales", s.createSale)
	sell.GET("/sale-events", s.listSaleEvents)
	sell.POST("/sale-events", s.createSaleEvent)
	sell.POST("/sale-events/:id/select", s.selectSaleEvent)

	// The history and the work queues belong to the member workflows; a seller
	// records sales but does not follow up on them.
	members := g.Group("", requireAuth(), requireBandRole(models.RoleMember))
	members.GET("/history", s.saleHistory)
	members.GET("/operations", s.operationsQueues)
	members.PATCH("/sales/:id/cancel", s.cancelSale)
	members.PATCH("/sales/:id/delivery-status", s.setDeliveryStatus)
	members.PATCH("/sales/:id/payment-status", s.markSalePaid)
}

// saleHistory returns the receipts, newest first. Cancelled positions stay
// visible on purpose — that is the difference between a cancellation and a
// deletion.
func (s *Server) saleHistory(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	receipts, err := s.sales.History(c.Request.Context(), limit)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"receipts": receipts})
}

func (s *Server) operationsQueues(c *gin.Context) {
	queues, err := s.sales.Operations(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, queues)
}

type cancelSaleRequest struct {
	// Scope is "item" for a single position or "receipt" for the whole basket.
	Scope string `json:"scope"`
}

func (s *Server) cancelSale(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req cancelSaleRequest
	_ = c.ShouldBindJSON(&req)

	scope := sales.CancelItem
	switch req.Scope {
	case "", string(sales.CancelItem):
	case string(sales.CancelReceipt):
		scope = sales.CancelReceipt
	default:
		fail(c, http.StatusBadRequest, "invalid_scope", "scope must be item or receipt")
		return
	}

	ctx := c.Request.Context()
	cancelled, err := s.sales.Cancel(ctx, id, scope)
	if err != nil {
		s.reportSalesError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSaleCancelled, EntityType: "sale", EntityID: &id,
		Details: map[string]any{"scope": string(scope), "cancelled_ids": cancelled},
	})
	c.JSON(http.StatusOK, gin.H{"cancelled_ids": cancelled})
}

type deliveryStatusRequest struct {
	Status models.DeliveryStatus `json:"status" binding:"required"`
}

func (s *Server) setDeliveryStatus(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req deliveryStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	if err := s.sales.SetDeliveryStatus(ctx, id, req.Status); err != nil {
		s.reportSalesError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSaleStatus, EntityType: "sale", EntityID: &id,
		Details: map[string]any{"delivery_status": string(req.Status)},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) markSalePaid(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if err := s.sales.MarkPaid(ctx, id); err != nil {
		s.reportSalesError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSaleStatus, EntityType: "sale", EntityID: &id,
		Details: map[string]any{"is_paid": true},
	})
	c.Status(http.StatusNoContent)
}

// receiptPreview proposes the ID the seller can already read out to a customer.
//
// It is explicitly only a proposal: a concurrent sale may take the number
// first, which is why the booking itself settles the final ID.
func (s *Server) receiptPreview(c *gin.Context) {
	prefix := receipt.PrefixSale
	switch c.Query("kind") {
	case "", "sale":
	case "purchase":
		prefix = receipt.PrefixPurchase
	default:
		fail(c, http.StatusBadRequest, "invalid_kind", "kind must be sale or purchase")
		return
	}

	on, ok := queryDate(c, "date")
	if !ok {
		return
	}
	if on.IsZero() {
		on = s.today()
	}

	id, err := s.receipts.Next(c.Request.Context(), prefix, on)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"receipt_id": id, "provisional": true})
}

// createSaleRequest is the booking payload plus the optional offline envelope.
type createSaleRequest struct {
	sales.Request
	// ClientEventID, ClientDeviceID and ClientCreatedAt are set by a device
	// replaying a sale it queued while offline.
	ClientEventID   string     `json:"client_event_id"`
	ClientDeviceID  string     `json:"client_device_id"`
	ClientCreatedAt *time.Time `json:"client_created_at"`
}

func (s *Server) createSale(c *gin.Context) {
	var req createSaleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.SoldOn.IsZero() {
		req.SoldOn = s.today()
	}

	state := stateFrom(c)
	actor := sales.Actor{UserID: state.User.ID, Username: state.User.Username}
	// The seller field defaults to whoever is signed in but stays editable, so
	// one shared tablet can still record who actually made the sale.
	if strings.TrimSpace(req.SoldBy) == "" {
		req.SoldBy = state.User.Username
	}

	var offline *sales.OfflineEvent
	if req.ClientEventID != "" {
		created := time.Now().UTC()
		if req.ClientCreatedAt != nil {
			created = req.ClientCreatedAt.UTC()
		}
		offline = &sales.OfflineEvent{
			EventID:   req.ClientEventID,
			DeviceID:  req.ClientDeviceID,
			CreatedAt: created,
		}
	}

	ctx := c.Request.Context()
	result, err := s.sales.Book(ctx, req.Request, actor, offline)
	if err != nil {
		s.reportSalesError(c, err)
		return
	}

	if !result.Replayed {
		s.audit.Log(ctx, actorFrom(c), audit.Entry{
			Action: audit.ActionSaleCreated, EntityType: "sale",
			Details: map[string]any{
				"receipt_id":      result.ReceiptID,
				"total_due_cents": result.TotalDueCents,
				"donation_cents":  result.DonationCents,
				"positions":       len(result.SaleIDs),
				"offline":         offline != nil,
			},
		})
		// PaymentMethod is a closed enum; receipt/customer/article data never
		// enters the telemetry service.
		s.recordTelemetryEvent(c, "payment_method", req.PaymentMethod)
	}

	status := http.StatusCreated
	if result.Replayed {
		// A replay created nothing; 200 tells the device its queue entry is
		// settled without implying a second booking.
		status = http.StatusOK
	}
	c.JSON(status, result)
}

// reportSalesError maps booking errors onto stable API codes.
func (s *Server) reportSalesError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sales.ErrSyncConflict):
		fail(c, http.StatusConflict, "sync_conflict",
			"this offline event ID was already used with different data")
	case errors.Is(err, sales.ErrIntentUnusable):
		fail(c, http.StatusConflict, "payment_code_unusable",
			"the payment code is expired, cancelled or already redeemed")
	case errors.Is(err, sales.ErrEmptyBasket):
		fail(c, http.StatusBadRequest, "empty_basket", err.Error())
	case errors.Is(err, sales.ErrContactRequired):
		fail(c, http.StatusBadRequest, "contact_required", err.Error())
	case errors.Is(err, sales.ErrAmountTooLow):
		fail(c, http.StatusBadRequest, "amount_too_low", err.Error())
	case errors.Is(err, sales.ErrSaleNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such sale")
	case errors.Is(err, sales.ErrAlreadyCancelled):
		fail(c, http.StatusConflict, "already_cancelled", err.Error())
	case errors.Is(err, sales.ErrAlreadyPaid):
		fail(c, http.StatusConflict, "already_paid", err.Error())
	case errors.Is(err, sales.ErrNoDeliveryFlow):
		fail(c, http.StatusConflict, "no_delivery_flow", err.Error())
	case errors.Is(err, sales.ErrInvalidTransition):
		fail(c, http.StatusConflict, "invalid_transition", err.Error())
	case errors.Is(err, sales.ErrUnknownVariant):
		fail(c, http.StatusBadRequest, "unknown_variant", err.Error())
	case errors.Is(err, sales.ErrVariantNotOffered):
		fail(c, http.StatusBadRequest, "variant_not_offered", err.Error())
	case errors.Is(err, sales.ErrUnknownPayment):
		fail(c, http.StatusBadRequest, "unknown_payment_method", err.Error())
	case errors.Is(err, sales.ErrInvalidQuantity), errors.Is(err, sales.ErrNegativePrice):
		fail(c, http.StatusBadRequest, "invalid_basket", err.Error())
	default:
		serverError(c, err)
	}
}

// --- sale events ----------------------------------------------------------

type saleEventPayload struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	IsSelected bool   `json:"is_selected"`
}

// listSaleEvents returns the band's events, most recently used first, together
// with the one currently selected.
//
// The selection is shared across the band rather than per user, so several
// phones at the same stand book against the same gig.
func (s *Server) listSaleEvents(c *gin.Context) {
	ctx := c.Request.Context()

	var events []models.SaleEvent
	if err := s.db.WithContext(ctx).Order("last_selected_at DESC, id DESC").Find(&events).Error; err != nil {
		serverError(c, err)
		return
	}

	selectedID, err := s.selectedEventID(c)
	if err != nil {
		serverError(c, err)
		return
	}

	payload := make([]saleEventPayload, 0, len(events))
	for _, event := range events {
		payload = append(payload, saleEventPayload{
			ID: event.ID, Name: event.Name, IsSelected: event.ID == selectedID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"events": payload, "selected_event_id": selectedID})
}

func (s *Server) selectedEventID(c *gin.Context) (int64, error) {
	var state models.SaleEventState
	err := s.db.WithContext(c.Request.Context()).First(&state).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// No event chosen yet is a normal state, not an error.
			return 0, nil
		}
		return 0, err
	}
	return state.EventID, nil
}

type createSaleEventRequest struct {
	Name string `json:"name" binding:"required"`
	// Select immediately makes the new event the band's active one, which is
	// what the sales page does when a seller types a new gig name.
	Select bool `json:"select"`
}

func (s *Server) createSaleEvent(c *gin.Context) {
	var req createSaleEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 200 {
		fail(c, http.StatusBadRequest, "invalid_name", "the event name must be 1 to 200 characters")
		return
	}

	ctx := c.Request.Context()
	now := time.Now().UTC()

	// An event that already exists is reused rather than duplicated, because
	// the same gig name typed twice is the same gig.
	var event models.SaleEvent
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&event).Error
	switch {
	case err == nil:
	case errors.Is(err, gorm.ErrRecordNotFound):
		event = models.SaleEvent{Name: name, CreatedAt: now, LastSelectedAt: now}
		if err := s.db.WithContext(ctx).Create(&event).Error; err != nil {
			serverError(c, err)
			return
		}
	default:
		serverError(c, err)
		return
	}

	if req.Select {
		if err := s.selectEvent(c, event.ID); err != nil {
			serverError(c, err)
			return
		}
	}
	c.JSON(http.StatusCreated, saleEventPayload{ID: event.ID, Name: event.Name, IsSelected: req.Select})
}

func (s *Server) selectSaleEvent(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	var event models.SaleEvent
	if err := s.db.WithContext(c.Request.Context()).First(&event, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "not_found", "no such event")
			return
		}
		serverError(c, err)
		return
	}
	if err := s.selectEvent(c, event.ID); err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, saleEventPayload{ID: event.ID, Name: event.Name, IsSelected: true})
}

// selectEvent stores the band-wide selection and refreshes the event's
// recency, which is what orders the picker.
func (s *Server) selectEvent(c *gin.Context, eventID int64) error {
	ctx := c.Request.Context()
	now := time.Now().UTC()

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Model(&models.SaleEvent{}).
			Where("id = ?", eventID).Update("last_selected_at", now).Error; err != nil {
			return err
		}

		var state models.SaleEventState
		err := tx.WithContext(ctx).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.WithContext(ctx).Create(&models.SaleEventState{EventID: eventID, UpdatedAt: now}).Error
		}
		if err != nil {
			return err
		}
		return tx.WithContext(ctx).Model(&models.SaleEventState{}).Where("id = ?", state.ID).
			Updates(map[string]any{"event_id": eventID, "updated_at": now}).Error
	})
}

// queryDate reads an optional YYYY-MM-DD query parameter, defaulting to today
// in the configured display timezone so a gig after midnight still books on
// the day the band experienced.
func queryDate(c *gin.Context, name string) (models.Date, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return models.Date{}, true
	}
	parsed, err := models.ParseDate(raw)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_date", err.Error())
		return models.Date{}, false
	}
	return parsed, true
}

// today is the current calendar date in the configured display timezone, so a
// gig that runs past midnight still books on the day the band experienced.
func (s *Server) today() models.Date {
	now := time.Now().In(s.cfg.DisplayTimezone)
	return models.NewDate(now.Year(), now.Month(), now.Day())
}
