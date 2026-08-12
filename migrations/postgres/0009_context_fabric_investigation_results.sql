CREATE TABLE IF NOT EXISTS acr.context_fabric_investigation_results (
    result_id     TEXT NOT NULL PRIMARY KEY,
    org_id        TEXT NOT NULL,
    payload       JSONB NOT NULL,
    generated_at  TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ck_acr_cf_investigation_results_result_id_length CHECK (char_length(result_id) BETWEEN 8 AND 256),
    CONSTRAINT ck_acr_cf_investigation_results_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256)
);

-- The primary key already covers result_id alone; this composite index is
-- what makes the org-scoped WHERE (org_id, result_id) fast for Get.
CREATE INDEX IF NOT EXISTS ix_acr_cf_investigation_results_org_result
    ON acr.context_fabric_investigation_results (org_id, result_id);
