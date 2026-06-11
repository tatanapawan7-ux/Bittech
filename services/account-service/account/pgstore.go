package account

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the production Store backed by PostgreSQL (schema in
// infra/db/0001_ledger.sql and 0002_auth.sql).
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore wraps an existing connection pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (p *PGStore) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	u := &User{Email: email, PasswordHash: passwordHash, Status: "active"}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		email, passwordHash).Scan(&u.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (p *PGStore) UserByEmail(ctx context.Context, email string) (*User, error) {
	return p.scanUser(p.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, totp_secret, totp_enabled, status
		 FROM users WHERE email = $1`, email))
}

func (p *PGStore) UserByID(ctx context.Context, id int64) (*User, error) {
	return p.scanUser(p.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, totp_secret, totp_enabled, status
		 FROM users WHERE id = $1`, id))
}

func (p *PGStore) scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.TOTPSecret, &u.TOTPEnabled, &u.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (p *PGStore) SetTOTP(ctx context.Context, userID int64, secret string, enabled bool) error {
	tag, err := p.pool.Exec(ctx,
		`UPDATE users SET totp_secret = $2, totp_enabled = $3 WHERE id = $1`,
		userID, secret, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *PGStore) CreateSession(ctx context.Context, s Session) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		s.TokenHash, s.UserID, s.ExpiresAt)
	return err
}

func (p *PGStore) SessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	var s Session
	err := p.pool.QueryRow(ctx,
		`SELECT token_hash, user_id, expires_at FROM sessions WHERE token_hash = $1`,
		tokenHash).Scan(&s.TokenHash, &s.UserID, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (p *PGStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (p *PGStore) CreateAPIKey(ctx context.Context, k APIKey) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO api_keys (key_id, user_id, secret_hash, label) VALUES ($1, $2, $3, $4)`,
		k.KeyID, k.UserID, k.SecretHash, k.Label)
	return err
}

func (p *PGStore) APIKeysByUser(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT key_id, user_id, secret_hash, label, disabled FROM api_keys WHERE user_id = $1`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.KeyID, &k.UserID, &k.SecretHash, &k.Label, &k.Disabled); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
