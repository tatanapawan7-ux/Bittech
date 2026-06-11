package auth

import (
	"errors"
	"testing"
	"time"
)

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword("correct horse battery staple", h); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if err := VerifyPassword("wrong", h); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	h1, _ := HashPassword("pw")
	h2, _ := HashPassword("pw")
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

// RFC 6238 Appendix B test vectors (SHA-1). The reference secret is the ASCII
// string "12345678901234567890"; vectors there use 8 digits, so we compare the
// trailing 6 digits our implementation produces.
func TestTOTPRFCVectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	cases := []struct {
		unix int64
		want string // last 6 digits of the RFC's 8-digit codes
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		got, err := TOTPCode(secret, time.Unix(c.unix, 0))
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("TOTP at %d = %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestTOTPVerifyWithSkew(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, _ := TOTPCode(secret, now)
	if !VerifyTOTP(secret, code, now) {
		t.Fatal("current code rejected")
	}
	// Code from one step ago should still verify (clock drift tolerance).
	prev, _ := TOTPCode(secret, now.Add(-30*time.Second))
	if !VerifyTOTP(secret, prev, now) {
		t.Fatal("previous-step code rejected within skew")
	}
	// A code from far in the past must fail.
	old, _ := TOTPCode(secret, now.Add(-10*time.Minute))
	if VerifyTOTP(secret, old, now) && old != code && old != prev {
		t.Fatal("stale code accepted")
	}
}

func TestAPIKeySignatureRoundTrip(t *testing.T) {
	_, secret, _, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sig := SignRequest(secret, now, "POST", "/v1/orders", `{"symbol":"BTC-USDT"}`)
	if !VerifyRequest(secret, sig, now.UnixMilli(), "POST", "/v1/orders", `{"symbol":"BTC-USDT"}`, now) {
		t.Fatal("valid signature rejected")
	}
	// Tampered body must fail.
	if VerifyRequest(secret, sig, now.UnixMilli(), "POST", "/v1/orders", `{"symbol":"ETH-USDT"}`, now) {
		t.Fatal("tampered body accepted")
	}
	// Stale timestamp must fail (replay protection).
	stale := now.Add(-2 * SignatureWindow)
	staleSig := SignRequest(secret, stale, "POST", "/v1/orders", "")
	if VerifyRequest(secret, staleSig, stale.UnixMilli(), "POST", "/v1/orders", "", now) {
		t.Fatal("stale signature accepted")
	}
}

func TestSessionToken(t *testing.T) {
	tok, hash, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if HashSecret(tok) != hash {
		t.Fatal("token hash mismatch")
	}
}
