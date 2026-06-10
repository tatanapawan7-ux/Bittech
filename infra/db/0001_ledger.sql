-- Double-entry ledger schema — the financial backbone of the exchange.
--
-- Every movement of value is recorded as a transaction containing two or more
-- balanced entries (debits + credits sum to zero per asset). Balances are never
-- updated in place by application code as a standalone write; they are derived
-- from / kept consistent with entries. This makes the system auditable and makes
-- "where did the money go?" always answerable — the failure mode that has sunk
-- real exchanges.
--
-- All amounts are BIGINT in the asset's smallest unit (see libs/money). No floats.

BEGIN;

CREATE TABLE assets (
    symbol      TEXT PRIMARY KEY,            -- e.g. 'BTC', 'USDT'
    name        TEXT NOT NULL,
    scale       SMALLINT NOT NULL CHECK (scale BETWEEN 0 AND 18), -- decimal places
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email       TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- An account is a (owner, asset) pair plus a type. System accounts (e.g. the
-- exchange fee account, external/blockchain clearing accounts) have user_id NULL.
CREATE TABLE accounts (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT REFERENCES users(id),          -- NULL for system accounts
    asset       TEXT NOT NULL REFERENCES assets(symbol),
    kind        TEXT NOT NULL DEFAULT 'main'           -- 'main' | 'locked' | 'system'
                CHECK (kind IN ('main', 'locked', 'system')),
    -- Cached balance, maintained transactionally alongside entries. A
    -- reconciliation job asserts it equals SUM(entries.amount) for the account.
    balance     BIGINT NOT NULL DEFAULT 0,
    UNIQUE (user_id, asset, kind)
);

-- A transaction groups a set of balanced entries applied atomically.
CREATE TABLE transactions (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind            TEXT NOT NULL,             -- 'trade' | 'deposit' | 'withdrawal' | 'lock' | 'unlock' | 'fee'
    -- Idempotency: replaying the same source event (e.g. an engine trade event)
    -- must not double-post. Producers supply a stable key.
    idempotency_key TEXT NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE entries (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id  BIGINT NOT NULL REFERENCES transactions(id),
    account_id      BIGINT NOT NULL REFERENCES accounts(id),
    asset           TEXT NOT NULL REFERENCES assets(symbol),
    amount          BIGINT NOT NULL,           -- signed: debit < 0, credit > 0
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX entries_account_idx     ON entries (account_id);
CREATE INDEX entries_transaction_idx ON entries (transaction_id);

-- Invariant check helper: every transaction must net to zero per asset.
-- Enforced in application code within the posting transaction; this view lets a
-- reconciliation job find violations.
CREATE VIEW unbalanced_transactions AS
    SELECT transaction_id, asset, SUM(amount) AS net
    FROM entries
    GROUP BY transaction_id, asset
    HAVING SUM(amount) <> 0;

COMMIT;
