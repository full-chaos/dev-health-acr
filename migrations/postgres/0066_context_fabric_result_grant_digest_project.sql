-- CHAOS-12227 (CHAOS-12107 design v3 V2, R2-1): the stored-result grant
-- digest also takes the project-scope form. A project-only computing
-- principal (an organization with no repository inventory, CHAOS-12228) is
-- recorded as 'p:<32 lowercase hex>', a framed digest of its canonical
-- project set. The earlier forms keep their meaning: 'g:<32 hex>' for a
-- repository-restricted principal, 'open' for an unrestricted or universal
-- one, NULL for a row saved before 0044 (grant unrecorded). 'p:' is the
-- format of THIS column only: the work-item census digest keeps its 64-hex
-- envelope (R2-1).
--
-- Order: this migration is applied before any build writes 'p:'. Nothing
-- writes it yet; no reader behavior changes here.
--
-- lock_timeout: the constraint swap takes an ACCESS EXCLUSIVE lock on the
-- results table. Waiting behind a long reader would queue every later query
-- on the table behind this one, so the wait is bounded: past 5s the
-- migration fails (SQLSTATE 55P03) and the roll can retry. SET LOCAL scopes
-- it to this file's transaction (the runner applies each file in one
-- transaction, runner.go applyMigration).
--
-- NOT VALID, as 0044: enforced on every INSERT/UPDATE from now on, without a
-- validating scan. The new CHECK is a superset of the old one, so every
-- existing row already satisfies it (rows before 0044 hold NULL, rows since
-- passed the old CHECK). A VALIDATE here would scan the whole table under the
-- ACCESS EXCLUSIVE lock and could find nothing to refuse.
--
-- `grant_digest IS NULL` adds no behavior to a CHECK (a NULL result passes)
-- and is kept explicit, as in 0044.
--
-- One statement, so the lock is taken once. No inline BEGIN/COMMIT (the
-- runner wraps the file). Idempotent.

SET LOCAL lock_timeout = '5s';

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_grant_digest_shape,
    ADD CONSTRAINT ck_acr_cf_investigation_results_grant_digest_shape
        CHECK (
            grant_digest IS NULL
            OR grant_digest = 'open'
            OR grant_digest ~ '^g:[0-9a-f]{32}$'
            OR grant_digest ~ '^p:[0-9a-f]{32}$'
        )
        NOT VALID;
