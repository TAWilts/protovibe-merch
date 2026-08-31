package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/purchases"
)

func (s *Server) registerPurchaseRoutes(g *gin.RouterGroup) {
	// Members may read the goods-receipt history; only managers may book,
	// correct or remove entries.
	read := g.Group("/purchases", requireAuth(), requireBandRole(models.RoleMember))
	read.GET("", s.listPurchases)
	read.GET("/last-cost/:id", s.lastPurchaseCost)

	write := g.Group("/purchases", requireAuth(), requireBandRole(models.RoleManager))
	write.POST("", s.createPurchase)
	write.PATCH("/:id", s.updatePurchase)
	write.DELETE("/:id", s.deletePurchase)

	g.DELETE("/purchase-receipts/:receiptID", requireAuth(), requireBandRole(models.RoleManager), s.deletePurchaseReceipt)
}

type purchasePayload struct {
	ID                int64       `json:"id"`
	ReceiptID         string      `json:"receipt_id"`
	VariantID         int64       `json:"variant_id"`
	ArticleName       string      `json:"article_name"`
	VariantLabel      string      `json:"variant_label"`
	Quantity          int         `json:"quantity"`
	UnitCostCents     int64       `json:"unit_cost_cents"`
	TotalCostCents    int64       `json:"total_cost_cents"`
	PurchasedOn       models.Date `json:"purchased_on"`
	Supplier          string      `json:"supplier"`
	InvoiceReference  string      `json:"invoice_reference"`
	HasInvoiceFile    bool        `json:"has_invoice_file"`
	Comment           string      `json:"comment"`
	CreatedByUsername string      `json:"created_by_username"`
}

// listPurchases returns the goods-receipt history, newest first.
func (s *Server) listPurchases(c *gin.Context) {
	ctx := c.Request.Context()

	var rows []models.Purchase
	err := s.db.WithContext(ctx).
		Order("purchased_on DESC, receipt_id DESC, id DESC").
		Find(&rows).Error
	if err != nil {
		serverError(c, err)
		return
	}

	labels, err := s.catalogue.VariantLabels(ctx)
	if err != nil {
		serverError(c, err)
		return
	}

	payload := make([]purchasePayload, 0, len(rows))
	for _, row := range rows {
		payload = append(payload, purchasePayload{
			ID:                row.ID,
			ReceiptID:         row.ReceiptID,
			VariantID:         row.VariantID,
			ArticleName:       labels[row.VariantID].ArticleName,
			VariantLabel:      labels[row.VariantID].VariantLabel,
			Quantity:          row.Quantity,
			UnitCostCents:     row.UnitCostCents,
			TotalCostCents:    int64(row.Quantity) * row.UnitCostCents,
			PurchasedOn:       row.PurchasedOn,
			Supplier:          row.Supplier,
			InvoiceReference:  row.InvoiceReference,
			HasInvoiceFile:    row.InvoiceFilePath != "",
			Comment:           row.Comment,
			CreatedByUsername: row.CreatedByUsername,
		})
	}
	c.JSON(http.StatusOK, gin.H{"purchases": payload})
}

// lastPurchaseCost pre-fills the form for a reorder.
func (s *Server) lastPurchaseCost(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	cost, found, err := s.purchases.LastUnitCost(c.Request.Context(), id)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"unit_cost_cents": cost, "found": found})
}

func (s *Server) createPurchase(c *gin.Context) {
	var req purchases.Request
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.PurchasedOn.IsZero() {
		req.PurchasedOn = s.today()
	}

	state := stateFrom(c)
	ctx := c.Request.Context()
	result, err := s.purchases.Create(ctx, req, purchases.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		s.reportPurchaseError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionPurchaseCreated, EntityType: "purchase",
		Details: map[string]any{
			"receipt_id":       result.ReceiptID,
			"positions":        len(result.PurchaseIDs),
			"total_cost_cents": result.TotalCostCents,
		},
	})
	c.JSON(http.StatusCreated, result)
}

func (s *Server) updatePurchase(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var item purchases.Item
	if err := c.ShouldBindJSON(&item); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	if err := s.purchases.Update(ctx, id, item); err != nil {
		s.reportPurchaseError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionPurchaseUpdated, EntityType: "purchase", EntityID: &id,
		Details: map[string]any{"quantity": item.Quantity, "unit_cost_cents": item.UnitCostCents},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) deletePurchase(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	path, err := s.purchases.Delete(ctx, id)
	if err != nil {
		s.reportPurchaseError(c, err)
		return
	}
	// The attachment is removed only after the row is gone, so a failed
	// delete never leaves a booking pointing at a missing file.
	s.removeStoredFile(ctx, path)

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionPurchaseDeleted, EntityType: "purchase", EntityID: &id,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) deletePurchaseReceipt(c *gin.Context) {
	receiptID := c.Param("receiptID")
	if receiptID == "" {
		fail(c, http.StatusBadRequest, "invalid_id", "invalid receipt identifier")
		return
	}

	ctx := c.Request.Context()
	paths, err := s.purchases.DeleteReceipt(ctx, receiptID)
	if err != nil {
		s.reportPurchaseError(c, err)
		return
	}
	for _, path := range paths {
		s.removeStoredFile(ctx, path)
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionPurchaseDeleted, EntityType: "purchase_receipt",
		Details: map[string]any{"receipt_id": receiptID, "attachments": len(paths)},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) reportPurchaseError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, purchases.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such purchase")
	case errors.Is(err, purchases.ErrEmptyReceipt):
		fail(c, http.StatusBadRequest, "empty_receipt", err.Error())
	case errors.Is(err, purchases.ErrInvalidQuantity):
		fail(c, http.StatusBadRequest, "invalid_quantity", err.Error())
	case errors.Is(err, purchases.ErrNegativeCost):
		fail(c, http.StatusBadRequest, "invalid_cost", err.Error())
	case errors.Is(err, purchases.ErrUnknownVariant):
		fail(c, http.StatusBadRequest, "unknown_variant", err.Error())
	default:
		serverError(c, err)
	}
}

// parsePathID is pathID for handlers whose parameter is not called "id".
func parsePathID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid_id", "invalid identifier")
		return 0, false
	}
	return id, true
}
