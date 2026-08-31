package auth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Errors from the session layer.
var (
	ErrNoSession      = errors.New("auth: no valid session")
	ErrSessionExpired = errors.New("auth: session expired")
	ErrStaleSession   = errors.New("auth: session was invalidated")
	ErrReauthRequired = errors.New("auth: a fresh password confirmation is required")
)

// SessionBundle is what the HTTP layer needs to serve one request.
type SessionBundle struct {
	Session *models.Session
	User    *models.User
	// Grant is set when the session operates under a support-access grant.
	Grant *models.SupportAccessGrant
}

// CreateSession starts a session and returns the raw cookie token and the raw
// CSRF token. Only their hashes are stored, so a database dump cannot be
// replayed as a live session.
func (s *Service) CreateSession(ctx context.Context, user *models.User, userAgent, ip string) (sessionToken, csrfToken string, err error) {
	sessionToken, err = RandomToken(32)
	if err != nil {
		return "", "", err
	}
	csrfToken, err = RandomToken(32)
	if err != nil {
		return "", "", err
	}

	now := time.Now().UTC()
	session := &models.Session{
		ID:             HashToken(sessionToken),
		UserID:         user.ID,
		BandID:         user.BandID,
		SessionVersion: user.SessionVersion,
		CSRFTokenHash:  HashToken(csrfToken),
		UserAgent:      truncate(userAgent, 255),
		IPAddress:      truncate(ip, 45),
		CreatedAt:      now,
		LastSeenAt:     now,
		ExpiresAt:      now.Add(s.sessionTTL),
	}
	if err := s.accountsDB(ctx).Create(session).Error; err != nil {
		return "", "", err
	}

	user.LastLoginAt = &now
	if err := s.accountsDB(ctx).Model(user).Update("last_login_at", now).Error; err != nil {
		return "", "", err
	}

	return sessionToken, csrfToken, nil
}

// LoadSession resolves a cookie token into a live session.
//
// It enforces four things on every single request: the session exists, it has
// not expired, it has not gone idle, and its session_version still matches the
// user. The last one is what makes password changes, role changes,
// deactivation and the admin center's session-kill take effect instantly.
func (s *Service) LoadSession(ctx context.Context, sessionToken string) (*SessionBundle, error) {
	if sessionToken == "" {
		return nil, ErrNoSession
	}

	var session models.Session
	if err := s.accountsDB(ctx).First(&session, "id = ?", HashToken(sessionToken)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoSession
		}
		return nil, err
	}

	now := time.Now().UTC()
	if now.After(session.ExpiresAt) || now.Sub(session.LastSeenAt) > s.sessionIdleTTL {
		_ = s.accountsDB(ctx).Delete(&models.Session{}, "id = ?", session.ID).Error
		return nil, ErrSessionExpired
	}

	var user models.User
	if err := s.accountsDB(ctx).First(&user, session.UserID).Error; err != nil {
		return nil, ErrNoSession
	}
	if !user.IsActive || user.SessionVersion != session.SessionVersion {
		_ = s.accountsDB(ctx).Delete(&models.Session{}, "id = ?", session.ID).Error
		return nil, ErrStaleSession
	}

	bundle := &SessionBundle{Session: &session, User: &user}

	if session.ActingGrantID != nil {
		var grant models.SupportAccessGrant
		if err := s.accountsDB(ctx).First(&grant, *session.ActingGrantID).Error; err != nil {
			return nil, ErrStaleSession
		}
		if !grant.IsLive(now) {
			// The grant lapsed while the session was open. The session itself
			// stays valid, it simply loses its band scope.
			if err := s.ClearSupportScope(ctx, &session); err != nil {
				return nil, err
			}
		} else {
			bundle.Grant = &grant
		}
	}

	// Only touch last_seen_at when it actually moved, so a busy sales page
	// does not write a row on every poll.
	if now.Sub(session.LastSeenAt) > time.Minute {
		session.LastSeenAt = now
		_ = s.accountsDB(ctx).Model(&session).Update("last_seen_at", now).Error
	}

	return bundle, nil
}

// VerifyCSRF checks the token a client sent for an unsafe request.
func (s *Service) VerifyCSRF(session *models.Session, token string) bool {
	return token != "" && EqualTokens(session.CSRFTokenHash, HashToken(token))
}

// MarkReauthenticated stamps a successful step-up confirmation.
func (s *Service) MarkReauthenticated(ctx context.Context, session *models.Session) error {
	now := time.Now().UTC()
	session.ReauthAt = &now
	return s.accountsDB(ctx).Model(session).Update("reauth_at", now).Error
}

// HasFreshReauth reports whether a step-up confirmation is still valid.
func (s *Service) HasFreshReauth(session *models.Session) bool {
	return session.ReauthAt != nil &&
		time.Since(*session.ReauthAt) <= s.reauthWindow
}

// SetPOSMode toggles the restricted point-of-sale mode of one session.
func (s *Service) SetPOSMode(ctx context.Context, session *models.Session, enabled bool) error {
	session.POSMode = enabled
	return s.accountsDB(ctx).Model(session).Update("pos_mode", enabled).Error
}

// ApplySupportScope binds a platform session to one band under a live grant.
func (s *Service) ApplySupportScope(ctx context.Context, session *models.Session, grant *models.SupportAccessGrant) error {
	session.BandID = &grant.BandID
	session.ActingGrantID = &grant.ID
	return s.accountsDB(ctx).Model(session).Updates(map[string]any{
		"band_id":         grant.BandID,
		"acting_grant_id": grant.ID,
	}).Error
}

// ClearSupportScope removes a band binding from a platform session, either
// because the grant lapsed or because the admin ended it.
func (s *Service) ClearSupportScope(ctx context.Context, session *models.Session) error {
	session.BandID = nil
	session.ActingGrantID = nil
	return s.accountsDB(ctx).Model(session).Updates(map[string]any{
		"band_id":         nil,
		"acting_grant_id": nil,
	}).Error
}

// DeleteSession signs one session out.
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	return s.accountsDB(ctx).Delete(&models.Session{}, "id = ?", sessionID).Error
}

// RevokeUserSessions signs a user out everywhere.
func (s *Service) RevokeUserSessions(ctx context.Context, userID int64) error {
	return s.accountsDB(ctx).Delete(&models.Session{}, "user_id = ?", userID).Error
}

// RevokeBandSessions signs out every member of a band at once. The admin
// center uses it when a band is deactivated or when credentials may have
// leaked.
func (s *Service) RevokeBandSessions(ctx context.Context, bandID int64) error {
	db := s.accountsDB(ctx)
	if err := db.Model(&models.User{}).
		Where("band_id = ?", bandID).
		UpdateColumn("session_version", gorm.Expr("session_version + 1")).Error; err != nil {
		return err
	}
	return db.Exec(
		"DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE band_id = ?)",
		bandID,
	).Error
}

// PurgeExpired removes sessions and pending authentication states that are
// past their lifetime. It runs on a schedule so the tables stay small.
func (s *Service) PurgeExpired(ctx context.Context) error {
	now := time.Now().UTC()
	db := s.accountsDB(ctx)
	if err := db.Delete(&models.Session{}, "expires_at < ? OR last_seen_at < ?",
		now, now.Add(-s.sessionIdleTTL)).Error; err != nil {
		return err
	}
	return db.Delete(&models.PendingAuth{}, "expires_at < ?", now).Error
}

// createPendingAuth stores the short-lived state between two login steps and
// returns the token identifying it.
func (s *Service) createPendingAuth(ctx context.Context, userID int64, purpose string, ttl time.Duration) (string, error) {
	token, err := RandomToken(32)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	pending := &models.PendingAuth{
		ID:        HashToken(token),
		UserID:    userID,
		Purpose:   purpose,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := s.accountsDB(ctx).Create(pending).Error; err != nil {
		return "", err
	}
	return token, nil
}

// ConsumePendingAuth validates a pending token for the expected purpose and
// removes it, so each step can only be taken once.
func (s *Service) ConsumePendingAuth(ctx context.Context, token, purpose string) (*models.User, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	hashed := HashToken(token)

	var pending models.PendingAuth
	if err := s.accountsDB(ctx).First(&pending, "id = ? AND purpose = ?", hashed, purpose).Error; err != nil {
		return nil, ErrNoSession
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = s.accountsDB(ctx).Delete(&pending).Error
		return nil, ErrSessionExpired
	}

	var user models.User
	if err := s.accountsDB(ctx).First(&user, pending.UserID).Error; err != nil {
		return nil, ErrNoSession
	}
	if !user.IsActive {
		return nil, ErrAccountInactive
	}
	if err := s.accountsDB(ctx).Delete(&pending).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// PeekPendingAuth resolves a pending token without consuming it. The MFA
// enrolment flow needs it because the token survives a failed code entry.
func (s *Service) PeekPendingAuth(ctx context.Context, token, purpose string) (*models.User, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	var pending models.PendingAuth
	if err := s.accountsDB(ctx).First(&pending, "id = ? AND purpose = ?", HashToken(token), purpose).Error; err != nil {
		return nil, ErrNoSession
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		return nil, ErrSessionExpired
	}
	var user models.User
	if err := s.accountsDB(ctx).First(&user, pending.UserID).Error; err != nil {
		return nil, ErrNoSession
	}
	return &user, nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

// PersistRecoveryCodes stores the remaining codes after one was consumed.
//
// It is separate from the verification so a failed follow-up step cannot leave
// a used recovery code still usable.
func (s *Service) PersistRecoveryCodes(ctx context.Context, user *models.User) error {
	return s.accountsDB(ctx).Model(user).
		Update("mfa_recovery_code_hashes", user.MFARecoveryCodeHashes).Error
}
