package api

import (
	"errors"
	"log/slog"
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
	telemetrysvc "github.com/tawilts/protovibe-merch/backend/internal/services/telemetry"
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
	members.PATCH("/sales/receipt/:receiptID/shipping-cost", s.updateSaleShippingCost)
	members.DELETE("/sale-events/:id", s.deleteSaleEvent)
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
	s.recordSaleTelemetry(c, "sale_status", cancelled)
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
	s.recordSaleTelemetry(c, "sale_status", []int64{id})
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
	s.recordSaleReceiptTelemetry(c, "sale_status", id)
	c.Status(http.StatusNoContent)
}

func (s *Server) updateSaleShippingCost(c *gin.Context) {
	receiptID := strings.TrimSpace(c.Param("receiptID"))
	if receiptID == "" {
		fail(c, http.StatusBadRequest, "invalid_receipt", "receipt identifier is required")
		return
	}
	var req struct {
		ShippingCostCents int64 `json:"shipping_cost_cents"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	result, err := s.sales.UpdateShippingCost(ctx, receiptID, req.ShippingCostCents)
	if err != nil {
		s.reportSalesError(c, err)
		return
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSaleShippingChanged, EntityType: "sale_receipt",
		Details: map[string]any{
			"receipt_id": receiptID, "sale_ids": result.SaleIDs,
			"old_shipping_cost_cents": result.OldShippingCostCents,
			"shipping_cost_cents":     result.ShippingCostCents,
			"discount_cents":          result.DiscountCents, "donation_cents": result.DonationCents,
		},
	})
	s.recordSaleTelemetry(c, "sale_shipping_changed", result.SaleIDs)
	c.JSON(http.StatusOK, result)
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
				"receipt_id":       result.ReceiptID,
				"total_due_cents":  result.TotalDueCents,
				"total_paid_cents": result.TotalPaidCents,
				"discount_cents":   result.DiscountCents,
				"donation_cents":   result.DonationCents,
				"positions":        len(result.SaleIDs),
				"offline":          offline != nil,
			},
		})
		s.recordTelemetryEvent(c, "payment_method", req.PaymentMethod)
		s.recordSaleTelemetry(c, "sale_created", result.SaleIDs)
	}

	status := http.StatusCreated
	if result.Replayed {
		// A replay created nothing; 200 tells the device its queue entry is
		// settled without implying a second booking.
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (s *Server) recordSaleReceiptTelemetry(c *gin.Context, eventType string, saleID int64) {
	state := stateFrom(c)
	if state == nil || state.User == nil || state.User.BandID == nil {
		return
	}

	ctx := c.Request.Context()
	var sale models.Sale
	if err := s.db.WithContext(ctx).First(&sale, saleID).Error; err != nil {
		slog.Warn("could not resolve sale receipt for telemetry", "error", err)
		return
	}
	var ids []int64
	if err := s.db.WithContext(ctx).Model(&models.Sale{}).
		Where("receipt_id = ?", sale.ReceiptID).
		Pluck("id", &ids).Error; err != nil {
		slog.Warn("could not resolve sale receipt positions for telemetry", "error", err)
		return
	}
	s.recordSaleTelemetry(c, eventType, ids)
}

func (s *Server) recordSaleTelemetry(c *gin.Context, eventType string, saleIDs []int64) {
	state := stateFrom(c)
	if state == nil || state.User == nil || state.User.BandID == nil ||
		state.User.TelemetryDecidedAt == nil ||
		state.User.TelemetryConsentVersion < models.CurrentTelemetryConsentVersion ||
		!state.User.TelemetryEnabled || len(saleIDs) == 0 {
		return
	}

	ctx := c.Request.Context()
	var saleRows []models.Sale
	if err := s.db.WithContext(ctx).Where("id IN ?", saleIDs).Find(&saleRows).Error; err != nil {
		slog.Warn("could not load sale rows for telemetry", "error", err)
		return
	}

	variantIDs := make([]int64, 0, len(saleRows))
	seenVariants := map[int64]bool{}
	for _, sale := range saleRows {
		if !seenVariants[sale.VariantID] {
			seenVariants[sale.VariantID] = true
			variantIDs = append(variantIDs, sale.VariantID)
		}
	}

	var variants []models.Variant
	if len(variantIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("id IN ?", variantIDs).Find(&variants).Error; err != nil {
			slog.Warn("could not resolve articles for telemetry", "error", err)
			return
		}
	}
	articleByVariant := make(map[int64]int64, len(variants))
	for _, variant := range variants {
		articleByVariant[variant.ID] = variant.ArticleID
	}

	for _, sale := range saleRows {
		snapshot := telemetrysvc.SaleSnapshot{
			BandID:          sale.BandID,
			SaleID:          sale.ID,
			ArticleID:       articleByVariant[sale.VariantID],
			Quantity:        sale.Quantity,
			UnitPriceCents:  sale.UnitPriceCents,
			AmountCents:     sale.AmountDueCents - sale.DiscountCents,
			PaymentMethod:   sale.PaymentMethod,
			IsPaid:          sale.IsPaid,
			IsReceived:      sale.IsReceived,
			IsCancelled:     sale.IsCancelled,
			DeliveryStatus:  string(sale.DeliveryStatus),
			PaymentFollowUp: sale.PaymentFollowUp,
			Location:        sale.EventName,
		}
		if err := s.telemetry.RecordSale(ctx, eventType, snapshot); err != nil {
			slog.Warn("pseudonymous sale telemetry write failed", "error", err)
		}
	}
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
	case errors.Is(err, sales.ErrDiscountConfirmationRequired):
		fail(c, http.StatusConflict, "discount_confirmation_required", err.Error())
	case errors.Is(err, sales.ErrSaleNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such sale")
	case errors.Is(err, sales.ErrAlreadyCancelled):
		fail(c, http.StatusConflict, "already_cancelled", err.Error())
	case errors.Is(err, sales.ErrAlreadyPaid):
		fail(c, http.StatusConflict, "already_paid", err.Error())
	case errors.Is(err, sales.ErrNoDeliveryFlow):
		fail(c, http.StatusConflict, "no_delivery_flow", err.Error())
	case errors.Is(err, sales.ErrShippingClosed):
		fail(c, http.StatusConflict, "shipping_closed", err.Error())
	case errors.Is(err, sales.ErrInvalidTransition):
		fail(c, http.StatusConflict, "invalid_transition", err.Error())
	case errors.Is(err, sales.ErrUnknownVariant):
		fail(c, http.StatusBadRequest, "unknown_variant", err.Error())
	case errors.Is(err, sales.ErrVariantNotOffered):
		fail(c, http.StatusBadRequest, "variant_not_offered", err.Error())
	case errors.Is(err, sales.ErrUnknownPayment):
		fail(c, http.StatusBadRequest, "unknown_payment_method", err.Error())
	case errors.Is(err, sales.ErrInvalidQuantity), errors.Is(err, sales.ErrNegativePrice), errors.Is(err, sales.ErrNegativeAmount),
		errors.Is(err, sales.ErrNegativeShipping), errors.Is(err, sales.ErrShippingOnCounter):
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

func (s *Server) deleteSaleEvent(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var event models.SaleEvent
	wasSelected := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).First(&event, id).Error; err != nil {
			return err
		}
		var selectedCount int64
		if err := tx.WithContext(ctx).Model(&models.SaleEventState{}).Where("event_id = ?", id).Count(&selectedCount).Error; err != nil {
			return err
		}
		wasSelected = selectedCount > 0
		if err := tx.WithContext(ctx).Where("event_id = ?", id).Delete(&models.SaleEventState{}).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Delete(&event).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "not_found", "no such event")
			return
		}
		serverError(c, err)
		return
	}
	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionSaleEventDeleted, EntityType: "sale_event", EntityID: &id,
		Details: map[string]any{
			"old": map[string]any{"id": event.ID, "name": event.Name, "selected": wasSelected},
			"new": nil,
		},
	})
	c.Status(http.StatusNoContent)
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
