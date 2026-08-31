package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// RecoveryCodeCount matches the original: ten single-use codes, shown once.
const RecoveryCodeCount = 10

// recoveryCodeLength is the number of characters per code.
const recoveryCodeLength = 12

// Errors from the two-factor flows.
var (
	ErrMFANotEnrolled  = errors.New("auth: no second factor enrolled")
	ErrInvalidMFACode  = errors.New("auth: invalid authentication code")
	ErrNoPendingSecret = errors.New("auth: no pending enrolment to confirm")
)

// totpOptions keep a one-step window either side of the current interval, so a
// phone whose clock drifts by a few seconds still works.
var totpOptions = totp.ValidateOpts{
	Period:    30,
	Skew:      1,
	Digits:    otp.DigitsSix,
	Algorithm: otp.AlgorithmSHA1,
}

// BeginEnrollment generates a pending TOTP secret and the otpauth URI an
// authenticator app scans. The secret only becomes active once the user proves
// they can produce a code from it, which is what ConfirmEnrollment does.
func (s *Service) BeginEnrollment(user *models.User) (secret string, otpauthURI string, err error) {
	secret, err = RandomTOTPSecret()
	if err != nil {
		return "", "", err
	}

	encrypted, err := s.cipher.Encrypt(secret)
	if err != nil {
		return "", "", err
	}
	user.MFAPendingSecretEncrypted = encrypted

	return secret, s.otpauthURI(user.Username, secret), nil
}

// otpauthURI builds the standard provisioning URI.
func (s *Service) otpauthURI(username, secret string) string {
	label := url.PathEscape(s.mfaIssuer + ":" + username)
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", s.mfaIssuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", "6")
	query.Set("period", "30")
	return "otpauth://totp/" + label + "?" + query.Encode()
}

// ConfirmEnrollment promotes the pending secret once a live code matches, and
// returns the plaintext recovery codes, which are shown exactly once.
func (s *Service) ConfirmEnrollment(user *models.User, code string) ([]string, error) {
	if user.MFAPendingSecretEncrypted == "" {
		return nil, ErrNoPendingSecret
	}
	secret, err := s.cipher.Decrypt(user.MFAPendingSecretEncrypted)
	if err != nil {
		return nil, err
	}
	if !validateTOTP(code, secret) {
		return nil, ErrInvalidMFACode
	}

	plain, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	user.MFASecretEncrypted = user.MFAPendingSecretEncrypted
	user.MFAPendingSecretEncrypted = ""
	user.MFARecoveryCodeHashes = hashes
	user.MFAEnabled = true
	user.MFAEnrolledAt = &now

	return plain, nil
}

// VerifySecondFactor accepts either a live TOTP code or one unused recovery
// code. A consumed recovery code is removed from the user, so the caller must
// persist the user afterwards.
func (s *Service) VerifySecondFactor(user *models.User, code string) error {
	if !user.MFAEnabled || user.MFASecretEncrypted == "" {
		return ErrMFANotEnrolled
	}

	secret, err := s.cipher.Decrypt(user.MFASecretEncrypted)
	if err != nil {
		return err
	}
	if validateTOTP(code, secret) {
		return nil
	}

	// Fall back to the recovery codes. Each is single use.
	wanted := HashCode(code)
	for i, stored := range user.MFARecoveryCodeHashes {
		if EqualTokens(stored, wanted) {
			user.MFARecoveryCodeHashes = append(
				append(models.JSONSlice{}, user.MFARecoveryCodeHashes[:i]...),
				user.MFARecoveryCodeHashes[i+1:]...,
			)
			return nil
		}
	}
	return ErrInvalidMFACode
}

// RegenerateRecoveryCodes issues a fresh set and invalidates the old one.
func (s *Service) RegenerateRecoveryCodes(user *models.User) ([]string, error) {
	if !user.MFAEnabled {
		return nil, ErrMFANotEnrolled
	}
	plain, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	user.MFARecoveryCodeHashes = hashes
	return plain, nil
}

// DisableMFA clears every second-factor artefact. Platform accounts must not
// reach this path; the caller enforces that.
func DisableMFA(user *models.User) {
	user.MFAEnabled = false
	user.MFASecretEncrypted = ""
	user.MFAPendingSecretEncrypted = ""
	user.MFARecoveryCodeHashes = models.JSONSlice{}
	user.MFAEnrolledAt = nil
}

func generateRecoveryCodes() (plain []string, hashes models.JSONSlice, err error) {
	plain = make([]string, 0, RecoveryCodeCount)
	hashes = make(models.JSONSlice, 0, RecoveryCodeCount)
	for i := 0; i < RecoveryCodeCount; i++ {
		code, err := RandomCode(recoveryCodeLength)
		if err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		plain = append(plain, code)
		hashes = append(hashes, HashCode(code))
	}
	return plain, hashes, nil
}

func validateTOTP(code, secret string) bool {
	normalized := NormalizeDigits(code)
	if len(normalized) != 6 {
		return false
	}
	ok, err := totp.ValidateCustom(normalized, secret, time.Now().UTC(), totpOptions)
	return err == nil && ok
}

// NormalizeDigits strips spaces and separators an authenticator app may show.
func NormalizeDigits(input string) string {
	out := make([]rune, 0, len(input))
	for _, r := range input {
		if r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}
	return string(out)
}

// NewEnrollmentPending starts the short-lived state that lets an account
// finish its mandatory second-factor setup right after choosing a password.
func (s *Service) NewEnrollmentPending(ctx context.Context, userID int64) (string, error) {
	return s.createPendingAuth(ctx, userID, models.PendingAuthMFAEnrollment, 30*time.Minute)
}
