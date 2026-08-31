package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/export"
)

func (s *Server) registerExportRoutes(g *gin.RouterGroup) {
	// One route for the whole filename: a path parameter cannot carry a
	// suffix in Gin, and the extension is what selects CSV or ZIP anyway.
	g.GET("/exports/:filename", requireAuth(), requireBandRole(models.RoleMember), s.exportFile)
}

// exportFile dispatches on the requested filename.
func (s *Server) exportFile(c *gin.Context) {
	filename := c.Param("filename")
	switch {
	case filename == "all.zip":
		s.exportZIP(c)
	case strings.HasSuffix(filename, ".csv"):
		s.exportCSV(c, export.Kind(strings.TrimSuffix(filename, ".csv")))
	default:
		fail(c, http.StatusNotFound, "unknown_export", "no such export")
	}
}

// exportCSV streams one sheet.
//
// Exports are produced from the database rather than from a rendered table, so
// a filter applied in the UI can never silently truncate the download.
func (s *Server) exportCSV(c *gin.Context, kind export.Kind) {
	if !kind.Valid() {
		fail(c, http.StatusNotFound, "unknown_export", "no such export")
		return
	}

	ctx := c.Request.Context()
	sheet, err := s.exports.Build(ctx, kind)
	if err != nil {
		serverError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "export.downloaded", EntityType: "export",
		Details: map[string]any{"kind": string(kind), "rows": len(sheet.Rows)},
	})

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+s.exportFilename(sheet.Name)+`.csv"`)
	if err := export.WriteCSV(c.Writer, sheet); err != nil {
		_ = c.Error(err)
	}
}

func (s *Server) exportZIP(c *gin.Context) {
	ctx := c.Request.Context()

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "export.downloaded", EntityType: "export",
		Details: map[string]any{"kind": "alles"},
	})

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="`+s.exportFilename("export")+`.zip"`)
	if err := s.exports.WriteZIP(ctx, c.Writer); err != nil {
		_ = c.Error(err)
	}
}

// exportFilename stamps the download with the date, so several exports in a
// downloads folder stay distinguishable.
func (s *Server) exportFilename(base string) string {
	return base + "-" + time.Now().In(s.cfg.DisplayTimezone).Format("2006-01-02")
}
