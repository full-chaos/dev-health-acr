-- Per-organization BYO LLM provider configuration (CHAOS-3775) and the
-- durable ModelExecutionReceipt sink (CHAOS-3775 AC-3775-6, drift item D16).
--
-- One row per organization: a PUT-triggered upsert replaces the whole row
-- (see internal/contextfabric/pgmodelconfig). credential_ciphertext is an
-- AES-256-GCM sealed blob (internal/contextfabric/modelconfigcrypto); the
-- plaintext credential never reaches this table.
--
-- generation (Codex round-1 finding F3) is a table-wide sequence, not a
-- per-row counter and not the wall-clock updated_at: internal/contextfabric/
-- modelruntimeresolver's per-organization runtime cache keys on this value,
-- and updated_at alone cannot serve that role -- clock_timestamp() only
-- guarantees microsecond resolution, so two upserts landing in the same
-- tick (or a system clock that steps backward) would produce indistinguishable
-- timestamps and pin a stale runtime. A table-wide sequence is monotonic
-- across every statement that touches this table, INCLUDING an INSERT that
-- follows a DELETE for the same org_id (finding F4): PostgreSQL evaluates a
-- DEFAULT nextval(...) expression while constructing the candidate row for
-- an INSERT ... ON CONFLICT statement whether or not the row ultimately
-- inserts or updates, and a sequence never rewinds when a transaction rolls
-- back or a row is deleted -- so a value already used by a NOW-DELETED row
-- is never reused. This closes the "delete then immediately re-add" race
-- an org_id-scoped counter (reset per row) would reopen.
CREATE SEQUENCE IF NOT EXISTS acr.context_fabric_org_model_config_generation_seq;

CREATE TABLE IF NOT EXISTS acr.context_fabric_org_model_config (
    org_id                  TEXT NOT NULL PRIMARY KEY,
    provider                TEXT NOT NULL,
    base_url                TEXT NOT NULL DEFAULT '',
    model                   TEXT NOT NULL,
    fallback_model          TEXT NOT NULL DEFAULT '',
    credential_ciphertext   BYTEA NOT NULL,
    credential_kid          TEXT NOT NULL,
    generation              BIGINT NOT NULL DEFAULT nextval('acr.context_fabric_org_model_config_generation_seq'),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ck_acr_cf_org_model_config_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_org_model_config_provider_length CHECK (char_length(provider) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_org_model_config_model_length CHECK (char_length(model) BETWEEN 1 AND 256),
    CONSTRAINT ck_acr_cf_org_model_config_kid_length CHECK (char_length(credential_kid) BETWEEN 1 AND 128)
);

ALTER SEQUENCE acr.context_fabric_org_model_config_generation_seq
    OWNED BY acr.context_fabric_org_model_config.generation;

-- Insert-only receipt sink: one row for every ModelRuntime call, org-scoped,
-- never updated or deleted by the application (internal/contextfabric/
-- pgmodelreceipts). Mirrors acr.context_fabric_investigation_results'
-- shape (0009): a wide JSONB payload plus the org/time columns that need
-- indexed access, so the contract (internal/contracts/v1 /
-- contextfabric.ModelExecutionReceipt) stays the single source of truth for
-- the field list.
CREATE TABLE IF NOT EXISTS acr.context_fabric_model_execution_receipts (
    receipt_id      TEXT NOT NULL PRIMARY KEY,
    org_id          TEXT NOT NULL,
    operation       TEXT NOT NULL,
    provider        TEXT NOT NULL,
    outcome         TEXT NOT NULL,
    fallback_used   BOOLEAN NOT NULL,
    payload         JSONB NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ck_acr_cf_model_receipts_receipt_id_length CHECK (char_length(receipt_id) BETWEEN 8 AND 256),
    CONSTRAINT ck_acr_cf_model_receipts_org_id_length CHECK (char_length(org_id) BETWEEN 1 AND 256)
);

CREATE INDEX IF NOT EXISTS ix_acr_cf_model_receipts_org_started
    ON acr.context_fabric_model_execution_receipts (org_id, started_at);
