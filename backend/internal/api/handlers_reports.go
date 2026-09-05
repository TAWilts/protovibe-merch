package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/bandfinance"
)

func (s *Server) registerReportRoutes(g *gin.RouterGroup) {
	members := g.Group("", requireAuth(), requireBandRole(models.RoleMember))
	members.GET("/balances", s.balances)
	members.GET("/band-finances", s.listBandFinances)
	members.GET("/band-finances/recurring", s.listRecurringBandTransactions)

	managers := g.Group("/band-finances", requireAuth(), requireBandRole(models.RoleManager))
	managers.POST("", s.createBandTransaction)
	managers.POST("/:id/cancel", s.cancelBandTransaction)
	managers.POST("/recurring", s.createRecurringBandTransaction)
	managers.PATCH("/recurring/:id/active", s.setRecurringBandTransactionActive)
	managers.DELETE("/recurring/:id", s.deleteRecurringBandTransaction)
}

// balances is the stock and money overview.
func (s *Server) balances(c *gin.Context) {
	if _, err := s.bandFinance.MaterializeDueForBand(c.Request.Context(), s.today()); err != nil {
		serverError(c, err)
		return
	}
	payload, err := s.balancesService.Compute(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (s *Server) listBandFinances(c *gin.Context) {
	if _, err := s.bandFinance.MaterializeDueForBand(c.Request.Context(), s.today()); err != nil {
		serverError(c, err)
		return
	}
	ledger, err := s.bandFinance.List(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, ledger)
}

func (s *Server) createBandTransaction(c *gin.Context) {
	var entry bandfinance.Entry
	if err := c.ShouldBindJSON(&entry); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if entry.TransactionOn.IsZero() {
		entry.TransactionOn = s.today()
	}

	state := stateFrom(c)
	ctx := c.Request.Context()
	transaction, err := s.bandFinance.Create(ctx, entry, bandfinance.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		s.reportBandFinanceError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "band_transaction.created", EntityType: "band_transaction", EntityID: &transaction.ID,
		Details: map[string]any{
			"type": string(transaction.TransactionType), "amount_cents": transaction.AmountCents,
		},
	})
	c.JSON(http.StatusCreated, transaction)
}

func (s *Server) listRecurringBandTransactions(c *gin.Context) {
	rules, err := s.bandFinance.ListRecurring(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"recurring": rules})
}

func (s *Server) createRecurringBandTransaction(c *gin.Context) {
	var entry bandfinance.RecurringEntry
	if err := c.ShouldBindJSON(&entry); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if entry.StartOn.IsZero() {
		entry.StartOn = s.today()
	}

	state := stateFrom(c)
	rule, err := s.bandFinance.CreateRecurring(c.Request.Context(), entry, bandfinance.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		s.reportBandFinanceError(c, err)
		return
	}

	if _, err := s.bandFinance.MaterializeDueForBand(c.Request.Context(), s.today()); err != nil {
		serverError(c, err)
		return
	}
	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action:     "band_transaction.recurring_created",
		EntityType: "recurring_band_transaction",
		EntityID:   &rule.ID,
		Details: map[string]any{
			"interval_value": rule.IntervalValue,
			"interval_unit":  rule.IntervalUnit,
		},
	})
	c.JSON(http.StatusCreated, rule)
}

func (s *Server) setRecurringBandTransactionActive(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	var req struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := s.bandFinance.SetRecurringActive(c.Request.Context(), id, req.Active, s.today()); err != nil {
		s.reportBandFinanceError(c, err)
		return
	}
	if req.Active {
		if _, err := s.bandFinance.MaterializeDueForBand(c.Request.Context(), s.today()); err != nil {
			serverError(c, err)
			return
		}
	}

	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action:     "band_transaction.recurring_state",
		EntityType: "recurring_band_transaction",
		EntityID:   &id,
		Details:    map[string]any{"active": req.Active},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteRecurringBandTransaction(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	if err := s.bandFinance.DeleteRecurring(c.Request.Context(), id); err != nil {
		s.reportBandFinanceError(c, err)
		return
	}

	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action:     "band_transaction.recurring_deleted",
		EntityType: "recurring_band_transaction",
		EntityID:   &id,
		Details:    map[string]any{"historic_bookings_kept": true},
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) cancelBandTransaction(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()
	err := s.bandFinance.Cancel(ctx, id, bandfinance.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		s.reportBandFinanceError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "band_transaction.cancelled", EntityType: "band_transaction", EntityID: &id,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) reportBandFinanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, bandfinance.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such entry")
	case errors.Is(err, bandfinance.ErrAlreadyCancelled):
		fail(c, http.StatusConflict, "already_cancelled", err.Error())
	case errors.Is(err, bandfinance.ErrInvalidAmount):
		fail(c, http.StatusBadRequest, "invalid_amount", err.Error())
	case errors.Is(err, bandfinance.ErrInvalidType):
		fail(c, http.StatusBadRequest, "invalid_type", err.Error())
	case errors.Is(err, bandfinance.ErrMissingFields):
		fail(c, http.StatusBadRequest, "missing_fields", err.Error())
	case errors.Is(err, bandfinance.ErrInvalidInterval):
		fail(c, http.StatusBadRequest, "invalid_interval", err.Error())
	default:
		serverError(c, err)
	}
}
