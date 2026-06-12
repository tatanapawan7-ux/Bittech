-- API-key signing secrets are stored encrypted (AES-256-GCM) so HMAC request
-- signatures can be verified. The encryption key lives in KMS/Vault, never in
-- the database. secret_hash is retained for audit/lookup but is no longer the
-- source of truth for verification.

BEGIN;

ALTER TABLE api_keys ADD COLUMN secret_enc TEXT NOT NULL DEFAULT '';

COMMIT;
