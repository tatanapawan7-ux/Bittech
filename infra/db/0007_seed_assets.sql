-- Seed reference data: the assets the MVP trades. Assets are referenced by
-- foreign keys throughout the ledger and wallet tables, so they must exist
-- before any deposit or trade. Idempotent, so re-applying migrations is safe.
--
-- scale = number of decimal places in the asset's smallest unit (see libs/money).

BEGIN;

INSERT INTO assets (symbol, name, scale) VALUES
    ('BTC',  'Bitcoin',  8),
    ('ETH',  'Ether',    18),
    ('USDT', 'Tether',   6),
    ('USDC', 'USD Coin', 6)
ON CONFLICT (symbol) DO NOTHING;

COMMIT;
