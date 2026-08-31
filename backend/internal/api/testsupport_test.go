package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/api"
	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/db"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

var uniqueCounter atomic.Int64

func unique(prefix string) string {
	return prefix + strconv.FormatInt(uniqueCounter.Add(1), 10) +
		strconv.FormatInt(time.Now().UnixNano()%1_000_000, 36)
}

// harness is a running API server plus a cookie-aware client, which is what
// makes the session and CSRF behaviour testable exactly as a browser sees it.
type harness struct {
	t      *testing.T
	server *httptest.Server
	db     *gorm.DB
	auth   *auth.Service

	cookie    string
	csrfToken string
	// csrfCookie mirrors what a browser would keep, so a test can simulate a
	// page reload by discarding only the in-memory token.
	csrfCookie string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set; skipping API integration test")
	}

	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("SECRET_KEY", "test-secret-key-that-is-long-enough-for-config")
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("COOKIE_SECURE", "false")
	// An empty bootstrap password keeps the tests from creating a stray
	// platform account; each test makes the accounts it needs.
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	apiServer, err := api.NewServer(cfg, database)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := apiServer.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	authService, err := auth.NewService(database, cfg)
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}

	server := httptest.NewServer(api.New(apiServer))
	t.Cleanup(func() {
		server.Close()
		if sqlDB, err := database.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	return &harness{t: t, server: server, db: database, auth: authService}
}

// ctx returns a cross-band context for direct fixture manipulation in tests.
func (h *harness) ctx() context.Context {
	return tenant.WithCrossBandAccess(context.Background())
}

// response is a decoded API answer.
type response struct {
	Status int
	Body   map[string]any
}

// do performs a request, carrying the session cookie and CSRF token the way
// the browser client does.
func (h *harness) do(method, path string, payload any) response {
	h.t.Helper()

	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.cookie != "" {
		req.Header.Set("Cookie", h.cookie)
	}
	if h.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", h.csrfToken)
	}

	res, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("perform request: %v", err)
	}
	defer res.Body.Close()

	for _, cookie := range res.Cookies() {
		switch cookie.Name {
		case "merch_session":
			if cookie.MaxAge < 0 {
				h.cookie = ""
			} else {
				h.cookie = cookie.Name + "=" + cookie.Value
			}
		case "merch_csrf":
			if cookie.MaxAge < 0 {
				h.csrfCookie = ""
			} else {
				h.csrfCookie = cookie.Value
			}
		}
	}

	out := response{Status: res.StatusCode, Body: map[string]any{}}
	_ = json.NewDecoder(res.Body).Decode(&out.Body)
	return out
}

// signIn runs the full login exchange and stores the session for later calls.
func (h *harness) signIn(bandSlug, username, secret string) response {
	h.t.Helper()
	h.cookie, h.csrfToken, h.csrfCookie = "", "", ""

	res := h.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"band":     bandSlug,
		"username": username,
		"secret":   secret,
	})
	if token, ok := res.Body["csrf_token"].(string); ok {
		h.csrfToken = token
	}
	return res
}

// makeBand inserts a tenant.
func (h *harness) makeBand() *models.Band {
	h.t.Helper()
	band := &models.Band{
		Slug:         unique("band-"),
		Name:         "Test Band",
		IsActive:     true,
		FeatureFlags: models.FeatureFlags{},
	}
	if err := h.db.WithContext(h.ctx()).Create(band).Error; err != nil {
		h.t.Fatalf("create band: %v", err)
	}
	h.t.Cleanup(func() {
		_ = h.db.Exec("DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE band_id = ?)", band.ID).Error
		_ = h.db.Exec("DELETE FROM audit_log WHERE band_id = ?", band.ID).Error
		_ = h.db.Exec("DELETE FROM users WHERE band_id = ?", band.ID).Error
		_ = h.db.Exec("DELETE FROM bands WHERE id = ?", band.ID).Error
	})
	return band
}

// makeUser creates an account with a usable password, skipping the setup-code
// dance that has its own dedicated test.
func (h *harness) makeUser(bandID *int64, role models.Role, password string) *models.User {
	h.t.Helper()

	hash, err := auth.HashPassword(password)
	if err != nil {
		h.t.Fatalf("hash password: %v", err)
	}
	user := &models.User{
		BandID:                bandID,
		Username:              unique("user-"),
		PasswordHash:          hash,
		Role:                  role,
		IsActive:              true,
		MFARecoveryCodeHashes: models.JSONSlice{},
	}
	if err := h.db.WithContext(h.ctx()).Create(user).Error; err != nil {
		h.t.Fatalf("create user: %v", err)
	}
	h.t.Cleanup(func() {
		_ = h.db.Exec("DELETE FROM sessions WHERE user_id = ?", user.ID).Error
		_ = h.db.Exec("DELETE FROM users WHERE id = ?", user.ID).Error
	})
	return user
}

// reload re-reads a user from the database.
func (h *harness) reload(user *models.User) *models.User {
	h.t.Helper()
	var fresh models.User
	if err := h.db.WithContext(h.ctx()).First(&fresh, user.ID).Error; err != nil {
		h.t.Fatalf("reload user: %v", err)
	}
	return &fresh
}

// signInAs creates a band user with the given role and signs it in, returning
// the account. It is the shortcut most endpoint tests start from.
func (h *harness) signInAs(band *models.Band, role models.Role) *models.User {
	h.t.Helper()
	const password = "ein-langes-passwort"

	user := h.makeUser(&band.ID, role, password)
	if res := h.signIn(band.Slug, user.Username, password); res.Status != 200 {
		h.t.Fatalf("sign in as %s failed: %d %v", role, res.Status, res.Body)
	}
	return user
}

// jsonList reads a list field out of a decoded response body.
func jsonList(body map[string]any, key string) []any {
	list, _ := body[key].([]any)
	return list
}

// jsonObject reads a nested object out of a decoded response body.
func jsonObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// itoa renders an ID for a URL path.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// onHand reads a variant's derived stock through the API.
func (h *harness) onHand(variantID int64) int64 {
	h.t.Helper()

	res := h.do("GET", "/api/v1/articles", nil)
	for _, rawArticle := range jsonList(res.Body, "articles") {
		for _, rawVariant := range jsonList(jsonObject(rawArticle), "variants") {
			variant := jsonObject(rawVariant)
			if int64(variant["id"].(float64)) == variantID {
				return int64(variant["on_hand"].(float64))
			}
		}
	}
	h.t.Fatalf("variant %d not found in the catalogue", variantID)
	return 0
}

// decodeInto decodes a JSON body, tolerating an empty one.
func decodeInto(r io.Reader, target *map[string]any) error {
	return json.NewDecoder(r).Decode(target)
}
