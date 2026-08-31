package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/importer"
)

func (s *Server) registerImportRoutes(g *gin.RouterGroup) {
	imports := g.Group("/imports", requireAuth(), requireBandRole(models.RoleManager))
	imports.POST("/:kind/preview", s.previewImport)
	imports.POST("/:kind/apply", s.applyImport)
}

// readImportFile validates the upload and returns the parsed rows.
//
// The whole file is parsed before anything is written: a half-applied import
// would leave the stock ledger in a state nobody can reason about.
func (s *Server) readImportFile(c *gin.Context) (importer.Kind, []importer.Row, bool) {
	kind := importer.Kind(c.Param("kind"))
	if !kind.Valid() {
		fail(c, http.StatusNotFound, "unknown_import", "no such import")
		return "", nil, false
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, importer.MaxBytes+1024)
	header, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "missing_file", "no file was uploaded")
		return "", nil, false
	}

	file, err := header.Open()
	if err != nil {
		serverError(c, err)
		return "", nil, false
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, importer.MaxBytes+1))
	if err != nil {
		serverError(c, err)
		return "", nil, false
	}

	rows, err := importer.Parse(kind, content)
	if err != nil {
		// Parse errors are the user's to fix and name the offending line, so
		// they are returned verbatim rather than replaced with a generic code.
		failWithDetails(c, http.StatusBadRequest, "invalid_csv", err.Error(), nil)
		return "", nil, false
	}
	return kind, rows, true
}

// previewImport reports what an import would change without writing anything.
func (s *Server) previewImport(c *gin.Context) {
	kind, rows, ok := s.readImportFile(c)
	if !ok {
		return
	}

	preview, err := s.importer.Preflight(c.Request.Context(), kind, rows)
	if err != nil {
		s.reportImportError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (s *Server) applyImport(c *gin.Context) {
	kind, rows, ok := s.readImportFile(c)
	if !ok {
		return
	}

	on := s.today()
	if raw := c.PostForm("date"); raw != "" {
		parsed, err := models.ParseDate(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_date", err.Error())
			return
		}
		on = parsed
	}

	state := stateFrom(c)
	ctx := c.Request.Context()
	result, err := s.importer.Apply(ctx, kind, rows, on, importer.Actor{
		UserID: state.User.ID, Username: state.User.Username,
	})
	if err != nil {
		s.reportImportError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: "import.applied", EntityType: "import",
		Details: map[string]any{
			"kind": string(kind), "receipt_id": result.ReceiptID,
			"rows": result.RowCount, "total_cents": result.TotalCents,
		},
	})
	c.JSON(http.StatusCreated, result)
}

func (s *Server) reportImportError(c *gin.Context, err error) {
	if errors.Is(err, importer.ErrEmptyFile) {
		fail(c, http.StatusBadRequest, "empty_file", err.Error())
		return
	}
	// The preflight's messages name the article and the problem, which is
	// exactly what the band needs to fix the file.
	failWithDetails(c, http.StatusBadRequest, "import_rejected", err.Error(), nil)
}
