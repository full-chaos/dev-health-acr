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
-- or result id. It is outside retention (deleting submission records leaves
-- it unchanged) and outside the organization purge (orgpurge KeptTables),
-- like the audit trail.
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
