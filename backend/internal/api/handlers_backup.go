package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/services/backup"
)

func (s *Server) registerBackupRoutes(g *gin.RouterGroup) {
	p := g.Group("/platform/backups", requireAuth(), requirePlatformStaff())
	p.GET("", s.listBackups)

	// Running and downloading a dump is a system admin's job: a dump is the
	// band's entire book-keeping in one file.
	admin := p.Group("", requireSystemAdmin())
	admin.POST("", s.runBackup)
	admin.GET("/:id/download", s.downloadBackup)
	admin.POST("/prune", s.pruneBackups)
	admin.POST("/:id/restore", s.restoreBackup)
}

func (s *Server) listBackups(c *gin.Context) {
	var bandID *int64
	if raw := c.Query("band_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_id", "invalid band identifier")
			return
		}
		bandID = &parsed
	}

	runs, err := s.backups.List(c.Request.Context(), bandID)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

type runBackupRequest struct {
	// BandID nil means a full-instance dump.
	BandID *int64 `json:"band_id"`
}

// runBackup starts a dump synchronously.
//
// It is deliberately not fire-and-forget: an operator clicking "back up now"
// before a risky change needs to know it actually finished.
func (s *Server) runBackup(c *gin.Context) {
	var req runBackupRequest
	_ = c.ShouldBindJSON(&req)

	state := stateFrom(c)
	ctx := c.Request.Context()

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBackupStarted, EntityType: "backup",
		Details: map[string]any{"band_id": req.BandID},
	})

	run, err := s.backups.Run(ctx, req.BandID, "manual", backup.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBackupFinished, EntityType: "backup", EntityID: &run.ID,
		Details: map[string]any{"size_bytes": run.SizeBytes, "band_id": req.BandID},
	})
	c.JSON(http.StatusCreated, run)
}

// restoreBackup puts one band back to a captured state.
//
// It is the most destructive action the admin center offers, so it is a system
// admin's alone and it always takes a safety point first — the response hands
// that run back, so the operator can see what to return to if this was the
// wrong choice.
func (s *Server) restoreBackup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	state := stateFrom(c)
	ctx := c.Request.Context()

	safety, err := s.backups.RestoreBand(ctx, id, backup.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		s.reportBackupError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionBackupRestored, EntityType: "backup", EntityID: &id,
		Details: map[string]any{"safety_run_id": safety.ID},
	})
	c.JSON(http.StatusOK, gin.H{"safety_run": safety})
}

func (s *Server) reportBackupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, backup.ErrRunNotFound), errors.Is(err, backup.ErrRestoreMissing):
		fail(c, http.StatusNotFound, "not_found", "no such backup")
	case errors.Is(err, backup.ErrNotPerBand):
		fail(c, http.StatusBadRequest, "not_per_band",
			"only a per-band backup can be restored")
	case errors.Is(err, backup.ErrRestoreFailed):
		fail(c, http.StatusConflict, "restore_failed", err.Error())
	default:
		serverError(c, err)
	}
}

func (s *Server) downloadBackup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	reader, name, err := s.backups.Open(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, backup.ErrRunNotFound) {
			fail(c, http.StatusNotFound, "not_found", "no such backup")
			return
		}
		serverError(c, err)
		return
	}
	defer reader.Close()

	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action: "backup.downloaded", EntityType: "backup", EntityID: &id,
	})

	c.Header("Content-Type", "application/sql")
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(c.Writer, c.Request, name, time.Time{}, reader)
}

func (s *Server) pruneBackups(c *gin.Context) {
	removed, err := s.backups.Prune(c.Request.Context())
	if err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(c.Request.Context(), actorFrom(c), audit.Entry{
		Action: "backup.pruned", EntityType: "backup",
		Details: map[string]any{"removed": removed},
	})
	c.JSON(http.StatusOK, gin.H{"removed": removed})
}
