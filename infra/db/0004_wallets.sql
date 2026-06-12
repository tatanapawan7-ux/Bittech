-- Wallet / custody schema: deposit addresses, deposits, withdrawals, and the
-- per-user withdrawal address allowlist.
--
-- Custody of the actual keys is delegated to a provider (Fireblocks/BitGo/MPC);
-- these tables track the exchange-side state machine and link on-chain activity
-- to ledger postings. On-chain identifiers (addresses, tx hashes) are unique so
-- the same chain event can never be credited twice.

BEGIN;

CREATE TABLE deposit_addresses (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id),
    asset       TEXT NOT NULL REFERENCES assets(symbol),
    address     TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One active deposit address per (user, asset) in the MVP.
    UNIQUE (user_id, asset)
);

CREATE TABLE deposits (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id),
    asset         TEXT NOT NULL REFERENCES assets(symbol),
    amount        BIGINT NOT NULL CHECK (amount > 0),
    tx_hash       TEXT NOT NULL,
    confirmations INT NOT NULL DEFAULT 0,
    -- pending  : seen on-chain, not yet final
    -- credited : confirmed and posted to the ledger
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'credited')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A chain output (tx_hash, asset) is credited at most once.
    UNIQUE (tx_hash, asset)
);

CREATE TABLE withdrawal_allowlist (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id),
    asset       TEXT NOT NULL REFERENCES assets(symbol),
    address     TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, asset, address)
);

CREATE TABLE withdrawals (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id),
    asset         TEXT NOT NULL REFERENCES assets(symbol),
    amount        BIGINT NOT NULL CHECK (amount > 0),
    address       TEXT NOT NULL,
    -- requested : funds locked, awaiting manual/automated approval
    -- broadcast  : approved and sent to the custody provider (funds left books)
    -- rejected   : denied; locked funds returned
    status        TEXT NOT NULL DEFAULT 'requested'
                  CHECK (status IN ('requested', 'broadcast', 'rejected')),
    tx_hash       TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at    TIMESTAMPTZ
);
CREATE INDEX withdrawals_status_idx ON withdrawals (status);
CREATE INDEX withdrawals_user_idx   ON withdrawals (user_id);

COMMIT;
