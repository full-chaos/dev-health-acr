-- A browser token family whose organization is gone is revoked with the reason
-- org_deprovisioned (the refresh grant's organization check). It used to record
-- sign_out.
--
-- acr.browser_token_families.revoked_reason is a closed set (migration 0062:
-- reuse, sign_out, member_revoked). This migration widens it by one value and
-- keeps every other term of ck_acr_browser_token_families_shape as 0062 wrote
-- it. Migration 0062 is not edited.
--
-- The new set is a superset, so every existing row satisfies it. The constraint
-- is added NOT VALID, as 0027/0044 do: it skips the full scan of the table under
-- the ACCESS EXCLUSIVE lock and is still enforced on every INSERT and UPDATE
-- from now on. No VALIDATE CONSTRAINT follows (0027 gives the reason).
--
-- Roll order: the migration runs before the service that writes the new value
-- (service+migrate on the same build). Rollback is a helm rollback only; the old
-- CHECK is never put back, and a helm upgrade with a chart that predates 0073
-- is refused by acr-migrate (schema ahead of the binary). No data write, no
-- grant. lock_timeout: past 5s behind a long holder the migration fails
-- (SQLSTATE 55P03) and the roll can retry. Idempotent. No inline BEGIN/COMMIT
-- (the runner wraps the file).
SET LOCAL lock_timeout = '5s';

ALTER TABLE acr.browser_token_families
    DROP CONSTRAINT IF EXISTS ck_acr_browser_token_families_shape;
ALTER TABLE acr.browser_token_families
    ADD CONSTRAINT ck_acr_browser_token_families_shape CHECK (
        char_length(family_id) BETWEEN 1 AND 256
        AND char_length(client_id) BETWEEN 1 AND 2048
        AND expires_at > created_at
        AND ((revoked_at IS NULL AND revoked_reason IS NULL)
             OR (revoked_at IS NOT NULL AND revoked_reason IS NOT NULL
                 AND revoked_reason IN ('reuse', 'sign_out', 'member_revoked', 'org_deprovisioned')))
    ) NOT VALID;
