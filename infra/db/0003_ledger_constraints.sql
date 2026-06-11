-- System accounts (user_id IS NULL) represent the external world: one per
-- (asset, kind). The UNIQUE(user_id, asset, kind) constraint on accounts does
-- not deduplicate NULL user_ids, so enforce it with a partial unique index —
-- this also lets get-or-create use ON CONFLICT for system accounts.

BEGIN;

CREATE UNIQUE INDEX accounts_system_uniq
    ON accounts (asset, kind) WHERE user_id IS NULL;

COMMIT;
