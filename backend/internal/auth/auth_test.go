package auth_test

import (
	"strings"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
)

func TestPasswordHashingRoundTrip(t *testing.T) {
	const password = "ein-langes-passwort"

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if strings.Contains(hash, password) {
		t.Fatal("the stored hash must not contain the password")
	}
	if !auth.VerifyPassword(password, hash) {
		t.Fatal("the correct password must verify")
	}
	if auth.VerifyPassword("ein-langes-passwort ", hash) {
		t.Fatal("a near miss must not verify")
	}
}

// TestVerifyPasswordRejectsBrokenHash pins that a corrupted row can never turn
// into an authentication bypass.
func TestVerifyPasswordRejectsBrokenHash(t *testing.T) {
	for _, hash := range []string{"", "!", "not-a-hash", "$argon2id$broken"} {
		if auth.VerifyPassword("anything", hash) {
			t.Errorf("hash %q must never verify", hash)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := auth.ValidatePassword("kurz"); err == nil {
		t.Error("a short password must be rejected")
	}
	if err := auth.ValidatePassword(strings.Repeat("a", 201)); err == nil {
		t.Error("an over-long password must be rejected")
	}
	if err := auth.ValidatePassword("zehnzeichen"); err != nil {
		t.Errorf("a compliant password must be accepted: %v", err)
	}
}

func TestCipherRoundTrip(t *testing.T) {
	box, err := auth.NewCipher("a-secret-key-long-enough-for-hkdf-derivation")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	const secret = "JBSWY3DPEHPK3PXP"
	encrypted, err := box.Encrypt(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(encrypted, secret) {
		t.Fatal("the ciphertext must not contain the plaintext")
	}

	decrypted, err := box.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != secret {
		t.Fatalf("round trip mismatch: %q", decrypted)
	}
}

// TestCipherRejectsForeignKey is the observable consequence of changing
// SECRET_KEY: stored second-factor secrets stop opening, loudly.
func TestCipherRejectsForeignKey(t *testing.T) {
	first, _ := auth.NewCipher("the-original-secret-key-for-this-instance")
	second, _ := auth.NewCipher("a-different-secret-key-for-this-instance!")

	encrypted, err := first.Encrypt("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := second.Decrypt(encrypted); err == nil {
		t.Fatal("a secret encrypted under a different key must not decrypt")
	}
}

// TestCodeNormalization pins that a code typed with the display grouping, in
// lower case, or with stray spaces still matches what was stored.
func TestCodeNormalization(t *testing.T) {
	code, err := auth.RandomCode(12)
	if err != nil {
		t.Fatalf("random code: %v", err)
	}
	if !strings.Contains(code, "-") {
		t.Fatalf("codes are grouped for legibility, got %q", code)
	}

	stored := auth.HashCode(code)
	variants := []string{
		strings.ToLower(code),
		strings.ReplaceAll(code, "-", ""),
		" " + strings.ToLower(strings.ReplaceAll(code, "-", " ")) + " ",
	}
	for _, variant := range variants {
		if auth.HashCode(variant) != stored {
			t.Errorf("variant %q must hash to the same value as %q", variant, code)
		}
	}
}

// TestCodeAlphabetAvoidsAmbiguousCharacters keeps codes readable when someone
// copies one off a screen by hand.
func TestCodeAlphabetAvoidsAmbiguousCharacters(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := auth.RandomCode(12)
		if err != nil {
			t.Fatalf("random code: %v", err)
		}
		if strings.ContainsAny(code, "OI01") {
			t.Fatalf("code %q contains an easily misread character", code)
		}
	}
}

func TestTokenHashingIsStableAndOpaque(t *testing.T) {
	token, err := auth.RandomToken(32)
	if err != nil {
		t.Fatalf("random token: %v", err)
	}
	hashed := auth.HashToken(token)

	if hashed == token {
		t.Fatal("the stored value must differ from the token")
	}
	if auth.HashToken(token) != hashed {
		t.Fatal("hashing must be deterministic")
	}
	if !auth.EqualTokens(hashed, auth.HashToken(token)) {
		t.Fatal("EqualTokens must accept identical hashes")
	}
	if auth.EqualTokens(hashed, auth.HashToken("other")) {
		t.Fatal("EqualTokens must reject different hashes")
	}
}

func TestNormalizeUsername(t *testing.T) {
	if _, err := auth.NormalizeUsername("  ab  "); err == nil {
		t.Error("a too-short username must be rejected")
	}
	if _, err := auth.NormalizeUsername("with\x00null"); err == nil {
		t.Error("control characters must be rejected")
	}
	name, err := auth.NormalizeUsername("  merch-stand  ")
	if err != nil {
		t.Fatalf("valid username rejected: %v", err)
	}
	if name != "merch-stand" {
		t.Fatalf("expected trimming, got %q", name)
	}
}

func TestNormalizeDigits(t *testing.T) {
	if got := auth.NormalizeDigits(" 123 456 "); got != "123456" {
		t.Fatalf("expected 123456, got %q", got)
	}
}
