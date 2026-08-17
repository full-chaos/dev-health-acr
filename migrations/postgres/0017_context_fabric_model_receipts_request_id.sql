-- CHAOS-3889 (LLM decision-event observability, secondary/deferred-if-hard
-- item): add request_id to the durable ModelExecutionReceipt sink, closing
-- the observability-audit MED item "ModelExecutionReceipt has no
-- request_id column (correlatability)" -- a receipt row could not be joined
-- back to the InvestigationRequest that produced it.
--
-- contextfabric.ModelExecutionReceipt.RequestID is a new OPTIONAL field
-- (see model_runtime.go's doc comment on it): genkitruntime.Runtime stamps
-- it from InvestigationRequest.RequestID / SynthesisInput.Request.RequestID
-- when it builds a receipt, but the field was never required, and every
-- receipt already stored is (and stays) valid with it empty. This mirrors
-- 0015's NULL-sentinel discipline exactly: NULLABLE, no DEFAULT, every
-- pre-migration row reads back NULL, which never satisfies an equality
-- predicate -- there is nothing to backfill, because no pre-migration
-- receipt ever carried a request id to recover.
--
-- request_id is ALSO already captured inside this table's existing
-- `payload` JSONB blob once the Go struct carries the field (the whole
-- receipt is marshaled verbatim into payload) -- so this column is not the
-- only way to recover it, but a dedicated indexed column is what makes
-- "every receipt for this request" a plain equality lookup instead of a
-- JSONB unpack on every row.
--
-- No inline BEGIN/COMMIT (0015's sol-review lesson: migrations/postgres/
-- runner.go's applyMigration already wraps this whole file in its own
-- transaction). Every statement is independently idempotent (ADD COLUMN IF
-- NOT EXISTS, DROP CONSTRAINT IF EXISTS + ADD CONSTRAINT, CREATE INDEX IF
-- NOT EXISTS), matching 0015/0016's own idiom.

ALTER TABLE acr.context_fabric_model_execution_receipts
    ADD COLUMN IF NOT EXISTS request_id TEXT;

-- Matches 0015's per-column length CHECK shape: NULL is exempt (a receipt
-- built before this field existed, or by a ModelRuntime that never
-- received a request id), and 256 mirrors
-- ContextFabricInvestigationRequest's own request_id upper bound
-- (internal/contracts/v1/validate_context_fabric_request.go), so a value
-- this CHECK would reject could never have been a real request_id to begin
-- with.
ALTER TABLE acr.context_fabric_model_execution_receipts
    DROP CONSTRAINT IF EXISTS ck_acr_cf_model_receipts_request_id_length;
ALTER TABLE acr.context_fabric_model_execution_receipts
    ADD CONSTRAINT ck_acr_cf_model_receipts_request_id_length
        CHECK (request_id IS NULL OR char_length(request_id) BETWEEN 1 AND 256);

-- request_id-first (not org-scoped): the point of this column is "given a
-- request id (already known to be scoped to some org), find every receipt
-- it produced" -- a support/correlation lookup that starts from the
-- request, not from the org. The existing ix_acr_cf_model_receipts_org_started
-- index (0010) already serves every org-scoped time-range query; this is a
-- second, narrower index for a different access pattern, not a replacement.
CREATE INDEX IF NOT EXISTS ix_acr_cf_model_receipts_request_id
    ON acr.context_fabric_model_execution_receipts (request_id)
    WHERE request_id IS NOT NULL;
