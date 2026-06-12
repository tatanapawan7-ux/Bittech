// Package account implements user accounts for the exchange: signup, login
// (password + optional TOTP 2FA), bearer sessions, and API-key management.
//
// Business rules live in Service; persistence is behind the Store interface so
// the same logic runs against Postgres in production and an in-memory store in
// tests.
package account

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/tatanapawan7-ux/bittech/services/account-service/auth"
)

var (
	// ErrEmailTaken is returned when signing up with an existing email.
	ErrEmailTaken = errors.New("account: email already registered")
	// ErrInvalidCredentials covers wrong email/password/2FA uniformly so
	// responses don't reveal which factor failed.
	ErrInvalidCredentials = errors.New("account: invalid credentials")
	// ErrTOTPRequired is returned by Login when the account has 2FA enabled and
	// no code was supplied.
	ErrTOTPRequired = errors.New("account: totp code required")
	// ErrNotFound is returned for unknown users or sessions.
	ErrNotFound = errors.New("account: not found")
	// ErrWeakPassword is returned when a password fails minimum requirements.
	ErrWeakPassword = errors.New("account: password must be at least 10 characters")
	// ErrBadEmail is returned for malformed email addresses.
	ErrBadEmail = errors.New("account: invalid email address")
)

// SessionTTL is how long a login session stays valid.
const SessionTTL = 24 * time.Hour

// User is the persisted account record.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	TOTPSecret   string
	TOTPEnabled  bool
	Status       string
	IsAdmin      bool
}

// APIKey is a persisted programmatic-trading key (secret stored as hash only).
type APIKey struct {
	KeyID      string
	UserID     int64
	SecretHash string
	Label      string
	Disabled   bool
}

// Session is a persisted bearer session (token stored as hash only).
type Session struct {
	TokenHash string
	UserID    int64
	ExpiresAt time.Time
}

// Store is the persistence boundary for accounts.
type Store interface {
	CreateUser(ctx context.Context, email, passwordHash string) (*User, error)
	UserByEmail(ctx context.Context, email string) (*User, error)
	UserByID(ctx context.Context, id int64) (*User, error)
	SetTOTP(ctx context.Context, userID int64, secret string, enabled bool) error
	CreateSession(ctx context.Context, s Session) error
	SessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	CreateAPIKey(ctx context.Context, k APIKey) error
	APIKeysByUser(ctx context.Context, userID int64) ([]APIKey, error)
}

// Service implements the account business logic on top of a Store.
type Service struct {
	store Store
	now   func() time.Time // injected for tests
}

// NewService creates a Service backed by the given store.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// Signup registers a new user.
func (s *Service) Signup(ctx context.Context, email, password string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, ErrBadEmail
	}
	if len(password) < 10 {
		return nil, ErrWeakPassword
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	return s.store.CreateUser(ctx, email, hash)
}

// Login verifies credentials (and TOTP when enabled) and returns a bearer
// token. The raw token is returned once; only its hash is stored.
func (s *Service) Login(ctx context.Context, email, password, totpCode string) (token string, u *User, err error) {
	u, err = s.store.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		// Hash anyway so response timing doesn't reveal whether the email exists.
		_ = auth.VerifyPassword(password, "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		return "", nil, ErrInvalidCredentials
	}
	if err := auth.VerifyPassword(password, u.PasswordHash); err != nil {
		return "", nil, ErrInvalidCredentials
	}
	if u.TOTPEnabled {
		if totpCode == "" {
			return "", nil, ErrTOTPRequired
		}
		if !auth.VerifyTOTP(u.TOTPSecret, totpCode, s.now()) {
			return "", nil, ErrInvalidCredentials
		}
	}
	token, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		return "", nil, err
	}
	sess := Session{TokenHash: tokenHash, UserID: u.ID, ExpiresAt: s.now().Add(SessionTTL)}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return "", nil, err
	}
	return token, u, nil
}

// Authenticate resolves a bearer token to its user, enforcing expiry.
func (s *Service) Authenticate(ctx context.Context, token string) (*User, error) {
	sess, err := s.store.SessionByTokenHash(ctx, auth.HashSecret(token))
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if s.now().After(sess.ExpiresAt) {
		_ = s.store.DeleteSession(ctx, sess.TokenHash)
		return nil, ErrInvalidCredentials
	}
	return s.store.UserByID(ctx, sess.UserID)
}

// SetupTOTP generates and stores a pending (not yet enabled) TOTP secret and
// returns the otpauth:// provisioning URI for the authenticator app.
func (s *Service) SetupTOTP(ctx context.Context, userID int64) (secret, uri string, err error) {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return "", "", err
	}
	secret, err = auth.NewTOTPSecret()
	if err != nil {
		return "", "", err
	}
	if err := s.store.SetTOTP(ctx, userID, secret, false); err != nil {
		return "", "", err
	}
	return secret, auth.TOTPProvisioningURI(secret, u.Email, "Bittech"), nil
}

// RequireTOTP performs step-up verification for sensitive actions (withdrawals,
// allowlist changes). It returns nil only when the user has 2FA enabled and the
// supplied code is valid, so callers can hard-fail actions for accounts without
// 2FA. Returns ErrTOTPRequired when 2FA is not enabled on the account.
func (s *Service) RequireTOTP(ctx context.Context, userID int64, code string) error {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !u.TOTPEnabled {
		return ErrTOTPRequired
	}
	if !auth.VerifyTOTP(u.TOTPSecret, code, s.now()) {
		return ErrInvalidCredentials
	}
	return nil
}

// ConfirmTOTP enables 2FA after the user proves possession of the secret by
// submitting one valid code.
func (s *Service) ConfirmTOTP(ctx context.Context, userID int64, code string) error {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.TOTPSecret == "" || !auth.VerifyTOTP(u.TOTPSecret, code, s.now()) {
		return ErrInvalidCredentials
	}
	return s.store.SetTOTP(ctx, userID, u.TOTPSecret, true)
}

// CreateAPIKey mints a programmatic-trading key. The secret is returned once
// and never persisted in plaintext.
func (s *Service) CreateAPIKey(ctx context.Context, userID int64, label string) (keyID, secret string, err error) {
	keyID, secret, secretHash, err := auth.NewAPIKey()
	if err != nil {
		return "", "", err
	}
	k := APIKey{KeyID: keyID, UserID: userID, SecretHash: secretHash, Label: label}
	if err := s.store.CreateAPIKey(ctx, k); err != nil {
		return "", "", err
	}
	return keyID, secret, nil
}
