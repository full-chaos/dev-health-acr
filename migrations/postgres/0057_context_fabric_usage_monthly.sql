-- CHAOS-11914 (Ask Dev plan §2.9, INV-5): the shared monthly usage aggregate.
--
-- One row per (organization, calendar month UTC): the number of ADMITTED
-- submission runs on the deployment's own model. Admission increments it in
-- the same transaction that inserts the submission record, under the
-- monthly allowance (`platform_runs < allowance`), serialized per
-- organization by an advisory lock. A run on the organization's own model
-- is not counted here (it counts to concurrency only).
--
-- Content-free by construction: counts only, no question, answer, principal
-- or result id. It is outside retention: deleting submission records or
-- results leaves it unchanged. An organization purge deletes the
-- organization's rows (orgpurge purgeTables, lead ruling 2026-10-29), so no
-- row keyed by a purged organization stays; the write fence (0052) below
-- refuses a later write for it.
--
-- cost_micro_usd stays NULL: ACR records token counts per model call
-- (ModelExecutionReceipt.Usage) but no price, so it meters no cost. A
-- later change that meters cost writes it in the same statement.
--
-- No inline BEGIN/COMMIT (the runner wraps the file). Idempotent.

CREATE TABLE IF NOT EXISTS acr.context_fabric_usage_monthly (
    org_id         TEXT        NOT NULL,
    month          DATE        NOT NULL,
    platform_runs  BIGINT      NOT NULL,
    cost_micro_usd BIGINT,
    updated_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT pk_acr_cf_usage_monthly PRIMARY KEY (org_id, month),
    CONSTRAINT ck_acr_cf_usage_monthly_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_usage_monthly_month_start CHECK (month = date_trunc('month', month)::date),
    CONSTRAINT ck_acr_cf_usage_monthly_runs CHECK (platform_runs >= 0),
    CONSTRAINT ck_acr_cf_usage_monthly_cost CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0)
);

-- The organization write fence (0052), on the new table.
DROP TRIGGER IF EXISTS refuse_purged_org_insert ON acr.context_fabric_usage_monthly;
DROP TRIGGER IF EXISTS refuse_purged_org_update ON acr.context_fabric_usage_monthly;
CREATE TRIGGER refuse_purged_org_insert BEFORE INSERT ON acr.context_fabric_usage_monthly
    FOR EACH ROW EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
CREATE TRIGGER refuse_purged_org_update BEFORE UPDATE OF org_id ON acr.context_fabric_usage_monthly
    FOR EACH ROW WHEN (NEW.org_id IS DISTINCT FROM OLD.org_id) EXECUTE FUNCTION acr.context_fabric_refuse_purged_org();
