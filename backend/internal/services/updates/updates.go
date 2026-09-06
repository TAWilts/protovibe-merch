// Package updates reports whether a newer release exists.
//
// The check is advisory: the instance never updates itself, it only tells an
// operator that something newer is published. That keeps a self-hosted band
// server from changing under its owner's feet.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrNotConfigured is returned when no repository is set.
var ErrNotConfigured = errors.New("updates: no repository configured")

// Release is what the check reports back.
type Release struct {
	Current string    `json:"current"`
	Latest  string    `json:"latest"`
	Newer   bool      `json:"newer_available"`
	URL     string    `json:"url"`
	Notes   string    `json:"notes"`
	Checked time.Time `json:"checked_at"`
	// CachedAt is set only on an answer served from the cache; a zero
	// time.Time is not omitted by encoding/json, so it has to be a pointer.
	CachedAt *time.Time `json:"cached_at,omitempty"`
}

// Config is what the service needs from the environment.
type Config struct {
	Repository string
	Token      string
	Timeout    time.Duration
	CacheTTL   time.Duration
	Version    string
}

// Service caches the last answer.
//
// GitHub rate-limits unauthenticated calls hard, and a settings page that is
// opened repeatedly must not spend that budget.
type Service struct {
	cfg    Config
	client *http.Client

	mu     sync.Mutex
	cached *Release
}

// NewService builds the update checker.
func NewService(cfg Config) *Service {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Service{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

// Latest reports the newest published release, from cache when it is fresh.
func (s *Service) Latest(ctx context.Context, force bool) (*Release, error) {
	if strings.TrimSpace(s.cfg.Repository) == "" {
		return nil, ErrNotConfigured
	}

	s.mu.Lock()
	if !force && s.cached != nil && time.Since(s.cached.Checked) < s.cfg.CacheTTL {
		cached := *s.cached
		s.mu.Unlock()
		cached.CachedAt = &cached.Checked
		return &cached, nil
	}
	s.mu.Unlock()

	release, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cached = release
	s.mu.Unlock()

	answer := *release
	return &answer, nil
}

func (s *Service) fetch(ctx context.Context) (*Release, error) {
	url := "https://api.github.com/repos/" + s.cfg.Repository + "/releases/latest"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if s.cfg.Token != "" {
		request.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	}

	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("updates: reaching GitHub failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		// A repository without any release is a normal state for a fork.
		return &Release{Current: s.cfg.Version, Checked: time.Now().UTC()}, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updates: GitHub answered %d", response.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}

	return &Release{
		Current: s.cfg.Version,
		Latest:  payload.TagName,
		Newer:   isNewer(s.cfg.Version, payload.TagName),
		URL:     payload.HTMLURL,
		Notes:   payload.Body,
		Checked: time.Now().UTC(),
	}, nil
}

// isNewer compares two version tags.
//
// Comparison is numeric per component rather than lexical, so v0.10.0 counts
// as newer than v0.9.0. Anything unparsable falls back to "different means
// newer", which errs towards telling the operator rather than staying quiet.
func isNewer(current, latest string) bool {
	if latest == "" {
		return false
	}
	c, okCurrent := parse(current)
	l, okLatest := parse(latest)
	if !okCurrent || !okLatest {
		return current != latest
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parse(tag string) ([3]int, bool) {
	var out [3]int
	trimmed := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if trimmed == "" {
		return out, false
	}
	// A pre-release suffix is ignored for the comparison.
	if index := strings.IndexAny(trimmed, "-+"); index >= 0 {
		trimmed = trimmed[:index]
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, part := range parts {
		var value int
		if _, err := fmt.Sscanf(part, "%d", &value); err != nil {
			return out, false
		}
		out[i] = value
	}
	return out, true
}
