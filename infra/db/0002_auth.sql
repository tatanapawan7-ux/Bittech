-- Authentication schema: credentials, sessions, and API keys.
--
-- Secrets are never stored in plaintext: passwords are argon2id hashes, session
-- tokens and API-key secrets are stored as SHA-256 digests. TOTP secrets must be
-- encrypted at the application layer (KMS) before production.

BEGIN;

ALTER TABLE users
    ADD COLUMN password_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN totp_secret   TEXT NOT NULL DEFAULT '',
    ADD COLUMN totp_enabled  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN status        TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'locked', 'closed'));

-- Opaque bearer sessions. The raw token lives only in the client; we keep its hash.
CREATE TABLE sessions (
    token_hash  TEXT PRIMARY KEY,            -- hex SHA-256 of the bearer token
    user_id     BIGINT NOT NULL REFERENCES users(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- API keys for programmatic trading. Requests are HMAC-SHA256 signed with the
-- secret; the secret itself is shown to the user once at creation.
CREATE TABLE api_keys (
    key_id      TEXT PRIMARY KEY,            -- public identifier sent with requests
    user_id     BIGINT NOT NULL REFERENCES users(id),
    secret_hash TEXT NOT NULL,               -- hex SHA-256 of the signing secret
    label       TEXT NOT NULL DEFAULT '',
    disabled    BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_user_idx ON api_keys (user_id);

COMMIT;
