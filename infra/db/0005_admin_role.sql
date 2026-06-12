-- Operator role. Admin users can approve/reject withdrawals and access the
-- admin panel. Roles are coarse for the MVP (one boolean); a real deployment
-- would use scoped permissions and a separate operator identity system.

BEGIN;

ALTER TABLE users ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT false;

COMMIT;
