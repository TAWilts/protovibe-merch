// Package auth implements accounts, sessions, two-factor authentication and
// the step-up confirmations that guard destructive actions.
//
// It keeps the original's security properties: TOTP secrets are encrypted at
// rest with a key derived from SECRET_KEY, recovery and setup codes are stored
// only as one-way hashes, and bumping a user's session_version invalidates
// every session they have anywhere.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// ErrDecrypt is returned when a stored secret cannot be opened, which in
// practice means SECRET_KEY changed.
var ErrDecrypt = errors.New("auth: cannot decrypt stored secret; SECRET_KEY may have changed")

// Cipher encrypts small secrets — currently TOTP seeds and the stored SMTP
// password — with a key derived from SECRET_KEY.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher derives an AES-256-GCM key from the application secret. The label
// separates this key from any other future use of the same secret.
func NewCipher(secretKey string) (*Cipher, error) {
	key := make([]byte, 32)
	reader := hkdf.New(sha256.New, []byte(secretKey), nil, []byte("protovibe-merch/secret-box/v1"))
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns a base64 string safe to store in a TEXT column.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", ErrDecrypt
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrDecrypt
	}
	if len(raw) < c.aead.NonceSize() {
		return "", ErrDecrypt
	}
	nonce, ciphertext := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plaintext), nil
}

// RandomToken returns a URL-safe random string of the given byte length. It is
// the source for session IDs, CSRF tokens and QR intent tokens.
func RandomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken produces the value stored for a session ID or CSRF token.
//
// A plain SHA-256 is deliberate: these are already 256 bits of entropy, so a
// slow password hash would add cost without adding resistance.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// EqualTokens compares two token hashes without leaking timing information.
func EqualTokens(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// codeAlphabet excludes characters that are easy to misread when a setup or
// recovery code is copied off a screen by hand.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandomCode returns an uppercase code of the given length, grouped in blocks
// of four for legibility, for example "H7K2-9PQM-3XRT".
func RandomCode(length int) (string, error) {
	if length <= 0 {
		return "", errors.New("auth: code length must be positive")
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	var b strings.Builder
	for i, v := range buf {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[int(v)%len(codeAlphabet)])
	}
	return b.String(), nil
}

// NormalizeCode makes user input from a setup or recovery code comparable:
// case and grouping dashes are irrelevant, mistyped whitespace is ignored.
func NormalizeCode(input string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		if strings.ContainsRune(codeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HashCode stores a setup or recovery code. These are single-use, high-entropy
// values that are only ever compared, so a fast hash of the normalised form is
// the right trade-off — the same choice the original made.
func HashCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeCode(code)))
	return hex.EncodeToString(sum[:])
}

// RandomTOTPSecret returns a base32 seed suitable for an authenticator app.
func RandomTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}
