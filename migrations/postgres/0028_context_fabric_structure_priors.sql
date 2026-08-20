-- CHAOS-3977 P5 (pivot-intent design brief, DESIGN-FINAL, §3.3/§3.6/§4):
-- the versioned, immutable, org-scoped Bridge prior store. Three tables:
--
--   acr.context_fabric_structure_priors      -- immutable versioned snapshots
--   acr.context_fabric_structure_prior_pointer -- one active-version pointer per org
--   acr.context_fabric_structure_prior_revocations -- per-entry kill list
--
-- This migration ships NO behavior change on its own: nothing reads these
-- tables in production composition until the consultation wiring (this same
-- changeset, flag-gated ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_ENABLED, default
-- OFF) is also enabled AND an org has a non-null active_version pointer
-- (design brief §3.4's own two-part gate). Every organization starts absent
-- from all three tables, which is the correct "no priors, cold start" state
-- (§3.7) -- absence degrades consultation to engine-derived offers only,
-- exactly like every other optional Context Fabric dependency's nil-means-
-- off convention.
--
-- No inline BEGIN/COMMIT: migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction.

-- One row PER (org, version). A version is an IMMUTABLE snapshot -- rows are
-- INSERTed once by the curation job and never UPDATEd; a "curation-rule
-- change is a version bump, never an in-place edit" (design brief §3.2).
-- entries is a JSONB array of StructurePriorEntry -- ids/enums/values only
-- (member, applied value, provenance/support counts, entry_id), never
-- question TEXT (curation reads QuestionHash only, §2.4's own sink
-- discipline applied to the store).
CREATE TABLE IF NOT EXISTS acr.context_fabric_structure_priors (
    org_id                TEXT NOT NULL,
    version               BIGINT NOT NULL,
    entries               JSONB NOT NULL,
    -- created_from_watermark names the event-log high-water mark this
    -- version was curated from (design brief §3.3: "created_from event-log
    -- watermark") -- an opaque, monotonic cursor value (this migration
    -- stores it as TEXT so curation's own cursor representation can evolve
    -- without a schema change), never re-derived, so a re-run of the SAME
    -- curation-rule version against the SAME watermark is deterministic and
    -- reproducible (§3.2's own "deterministic and re-runnable from the
    -- event log" requirement).
    created_from_watermark TEXT NOT NULL,
    curation_rule_version  TEXT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (org_id, version)
);

ALTER TABLE acr.context_fabric_structure_priors
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_priors_org_id_length;
ALTER TABLE acr.context_fabric_structure_priors
    ADD CONSTRAINT ck_acr_cf_structure_priors_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_priors
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_priors_version_positive;
ALTER TABLE acr.context_fabric_structure_priors
    ADD CONSTRAINT ck_acr_cf_structure_priors_version_positive
        CHECK (version >= 1);

ALTER TABLE acr.context_fabric_structure_priors
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_priors_curation_rule_version_length;
ALTER TABLE acr.context_fabric_structure_priors
    ADD CONSTRAINT ck_acr_cf_structure_priors_curation_rule_version_length
        CHECK (char_length(curation_rule_version) BETWEEN 1 AND 128);

CREATE INDEX IF NOT EXISTS ix_acr_cf_structure_priors_org_version
    ON acr.context_fabric_structure_priors (org_id, version DESC);

-- One row per org: the durable active-version pointer, the data-scale
-- analogue of CHAOS-3898's epoch pointer (design brief §3.3: "Runtime reads
-- ONE pinned active version per org (a pointer -- the 3898 epoch-pointer
-- pattern at data scale)"). active_version NULL means "no active priors" --
-- the cold-start default every org starts in, and also the state a
-- whole-version revocation-to-nothing leaves behind.
--
-- CAS discipline mirrors context_fabric_graph_lifecycle exactly (0019's own
-- header comment): every flip/rollback is
--   UPDATE ... SET active_version = $new, previous_version = $old, ...
--   WHERE org_id = $1 AND active_version IS NOT DISTINCT FROM $expected
-- so exactly one concurrent flip wins; the loser observes zero rows
-- affected (cf_prior_flip_cas_conflict) and must re-read before retrying.
--
-- DP8(a) (chris, ratified): a flip is a HUMAN-RATIFIED operation, never
-- automatic -- ratified_by is NOT NULL and carries the operator identity
-- (an opaque, operator-supplied string; never durable question content) so
-- every row in this table's own history (context_fabric_structure_prior_pointer_history,
-- below) has an accountable actor. Nothing in this changeset's own
-- production composition ever calls the flip path itself -- see
-- cmd/acr-projector's own "priors" subcommand, the sole caller.
CREATE TABLE IF NOT EXISTS acr.context_fabric_structure_prior_pointer (
    org_id           TEXT PRIMARY KEY,
    active_version   BIGINT,
    previous_version BIGINT,
    ratified_by      TEXT NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE acr.context_fabric_structure_prior_pointer
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_prior_pointer_org_id_length;
ALTER TABLE acr.context_fabric_structure_prior_pointer
    ADD CONSTRAINT ck_acr_cf_structure_prior_pointer_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

-- A durable append-only log of every flip/rollback (design brief §3.6's own
-- drift-measurement discipline: "Standing measurements, pre-registered per
-- version flip"), mirroring context_fabric_graph_epoch_retirements' own
-- "durable per-epoch retire records" precedent (0019) -- the pointer table
-- above is the CURRENT state; this table is the audit trail a drift
-- measurement or an incident review reads.
CREATE TABLE IF NOT EXISTS acr.context_fabric_structure_prior_pointer_history (
    id               BIGSERIAL PRIMARY KEY,
    org_id           TEXT NOT NULL,
    from_version     BIGINT,
    to_version       BIGINT,
    ratified_by      TEXT NOT NULL,
    ratified_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE acr.context_fabric_structure_prior_pointer_history
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_prior_pointer_history_org_id_length;
ALTER TABLE acr.context_fabric_structure_prior_pointer_history
    ADD CONSTRAINT ck_acr_cf_structure_prior_pointer_history_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_prior_pointer_history
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_prior_pointer_history_ratified_by_length;
ALTER TABLE acr.context_fabric_structure_prior_pointer_history
    ADD CONSTRAINT ck_acr_cf_structure_prior_pointer_history_ratified_by_length
        CHECK (char_length(ratified_by) BETWEEN 1 AND 256);

CREATE INDEX IF NOT EXISTS ix_acr_cf_structure_prior_pointer_history_org
    ON acr.context_fabric_structure_prior_pointer_history (org_id, ratified_at DESC);

-- Per-entry revocation (design brief §3.3: "Revocation is two-grained:
-- whole-version (pointer rollback, instant) and per-entry (a revocation
-- list consulted at read, for targeted kills between versions)"). entry_id
-- is deterministic and STABLE across curation runs (contextfabric's own
-- prior-entry-id derivation, keyed on org+member+feature+applied value, not
-- a per-version sequence number) -- so revoking one entry_id kills it in
-- EVERY version that ever re-proposes the same (member, feature, value)
-- triple, present or future, without needing to know which versions
-- contain it.
CREATE TABLE IF NOT EXISTS acr.context_fabric_structure_prior_revocations (
    org_id      TEXT NOT NULL,
    entry_id    TEXT NOT NULL,
    revoked_by  TEXT NOT NULL,
    revoked_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (org_id, entry_id)
);

ALTER TABLE acr.context_fabric_structure_prior_revocations
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_prior_revocations_org_id_length;
ALTER TABLE acr.context_fabric_structure_prior_revocations
    ADD CONSTRAINT ck_acr_cf_structure_prior_revocations_org_id_length
        CHECK (char_length(org_id) BETWEEN 1 AND 256);

ALTER TABLE acr.context_fabric_structure_prior_revocations
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_prior_revocations_revoked_by_length;
ALTER TABLE acr.context_fabric_structure_prior_revocations
    ADD CONSTRAINT ck_acr_cf_structure_prior_revocations_revoked_by_length
        CHECK (char_length(revoked_by) BETWEEN 1 AND 256);
