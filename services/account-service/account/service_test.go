package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tatanapawan7-ux/bittech/services/account-service/auth"
)

func newTestService() *Service {
	c, err := auth.NewCipher(make([]byte, 32))
	if err != nil {
		panic(err)
	}
	return NewService(NewMemStore()).WithCipher(c)
}

func TestSignupAndLogin(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()

	u, err := svc.Signup(ctx, "Trader@Example.com", "averysecurepw")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "trader@example.com" {
		t.Fatalf("email not normalized: %q", u.Email)
	}

	token, _, err := svc.Login(ctx, "trader@example.com", "averysecurepw", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Authenticate(ctx, token)
	if err != nil || got.ID != u.ID {
		t.Fatalf("Authenticate: %v, user %+v", err, got)
	}
}

func TestSignupValidation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	if _, err := svc.Signup(ctx, "not-an-email", "averysecurepw"); !errors.Is(err, ErrBadEmail) {
		t.Fatalf("expected ErrBadEmail, got %v", err)
	}
	if _, err := svc.Signup(ctx, "a@b.com", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("expected ErrWeakPassword, got %v", err)
	}
	svc.Signup(ctx, "a@b.com", "averysecurepw")
	if _, err := svc.Signup(ctx, "a@b.com", "averysecurepw"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	svc.Signup(ctx, "a@b.com", "averysecurepw")
	if _, _, err := svc.Login(ctx, "a@b.com", "wrongpassword", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if _, _, err := svc.Login(ctx, "nobody@b.com", "averysecurepw", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown email must look identical: got %v", err)
	}
}

func TestTOTPFlow(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	u, _ := svc.Signup(ctx, "a@b.com", "averysecurepw")

	secret, uri, err := svc.SetupTOTP(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if uri == "" {
		t.Fatal("empty provisioning URI")
	}

	// Login still works without 2FA until the secret is confirmed.
	if _, _, err := svc.Login(ctx, "a@b.com", "averysecurepw", ""); err != nil {
		t.Fatalf("login before confirm should not require totp: %v", err)
	}

	// Confirming with a wrong code fails; with the right code succeeds.
	if err := svc.ConfirmTOTP(ctx, u.ID, "000000"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected rejection of bad code, got %v", err)
	}
	code, _ := auth.TOTPCode(secret, time.Now())
	if err := svc.ConfirmTOTP(ctx, u.ID, code); err != nil {
		t.Fatal(err)
	}

	// Now login demands a code and rejects bad ones.
	if _, _, err := svc.Login(ctx, "a@b.com", "averysecurepw", ""); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("expected ErrTOTPRequired, got %v", err)
	}
	if _, _, err := svc.Login(ctx, "a@b.com", "averysecurepw", "000000"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for bad code, got %v", err)
	}
	code, _ = auth.TOTPCode(secret, time.Now())
	if _, _, err := svc.Login(ctx, "a@b.com", "averysecurepw", code); err != nil {
		t.Fatalf("login with valid code failed: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	svc.Signup(ctx, "a@b.com", "averysecurepw")
	token, _, _ := svc.Login(ctx, "a@b.com", "averysecurepw", "")

	// Fast-forward past the TTL.
	svc.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	u, _ := svc.Signup(ctx, "a@b.com", "averysecurepw")

	keyID, secret, err := svc.CreateAPIKey(ctx, u.ID, "trading bot")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := svc.store.APIKeysByUser(ctx, u.ID)
	if len(keys) != 1 || keys[0].KeyID != keyID {
		t.Fatalf("key not persisted: %+v", keys)
	}
	// The hash matches the returned secret, and the secret is not stored raw.
	if keys[0].SecretHash != auth.HashSecret(secret) {
		t.Fatal("stored hash does not match secret")
	}
	if keys[0].SecretHash == secret || keys[0].SecretEnc == secret {
		t.Fatal("secret stored in plaintext")
	}

	// A request signed with the secret verifies and resolves to the user.
	now := time.Now()
	sig := auth.SignRequest(secret, now, "POST", "/v1/orders", `{"x":1}`)
	got, err := svc.VerifyAPIRequest(ctx, keyID, sig, now.UnixMilli(), "POST", "/v1/orders", `{"x":1}`)
	if err != nil || got.ID != u.ID {
		t.Fatalf("VerifyAPIRequest: %v user=%+v", err, got)
	}
	// A tampered body fails verification.
	if _, err := svc.VerifyAPIRequest(ctx, keyID, sig, now.UnixMilli(), "POST", "/v1/orders", `{"x":2}`); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for tampered body, got %v", err)
	}
}

func TestAPIKeysDisabledWithoutCipher(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemStore()) // no cipher
	svc.Signup(ctx, "a@b.com", "averysecurepw")
	if _, _, err := svc.CreateAPIKey(ctx, 1, "bot"); !errors.Is(err, ErrAPIKeysDisabled) {
		t.Fatalf("expected ErrAPIKeysDisabled, got %v", err)
	}
}
