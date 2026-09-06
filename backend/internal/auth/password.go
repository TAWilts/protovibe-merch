package auth

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
)

// Password policy. The original required a non-trivial password without a hard
// rule; a floor of 10 characters keeps short passwords out while staying
// usable for a band member typing on a phone at a gig.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 200
)

// ErrWeakPassword is returned when a chosen password fails the policy.
var ErrWeakPassword = errors.New("auth: password does not meet the policy")

// argon2Params are tuned for an interactive login on a small server: roughly
// 64 MiB and a few iterations, which costs well under a second per attempt
// while making offline cracking expensive.
var argon2Params = &argon2id.Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

// HashPassword produces the stored password hash.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := argon2id.CreateHash(password, argon2Params)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return hash, nil
}

// VerifyPassword checks a password against a stored hash.
//
// A malformed stored hash is reported as "no match" rather than as an error,
// so a corrupted row cannot turn into an authentication bypass.
func VerifyPassword(password, hash string) bool {
	if hash == "" {
		return false
	}
	match, err := argon2id.ComparePasswordAndHash(password, hash)
	return err == nil && match
}

// ValidatePassword applies the password policy.
func ValidatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters required", ErrWeakPassword, MinPasswordLength)
	}
	if length > MaxPasswordLength {
		return fmt.Errorf("%w: at most %d characters allowed", ErrWeakPassword, MaxPasswordLength)
	}
	return nil
}
