package api

import (
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/config"
)

// Route registration itself catches conflicting Gin wildcard paths before a
// database-backed integration test ever sends a request.
func TestSandboxRoutesRegisterWithoutConflicts(t *testing.T) {
	t.Helper()
	_ = New(&Server{cfg: &config.Config{Environment: "development"}, metrics: newMetrics()})
}
