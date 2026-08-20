-- CHAOS-3860 P6 precondition fix (pivot-intent design brief, DESIGN-FINAL,
-- §2.4/§B5): the design brief's own sharding table (§4, P6 row) names P6's
-- dependency as "P4 (schema incl. ConsensusEvidence)". P4 (CHAOS-3927,
-- migration 0024) shipped StructureSelectionEvent WITHOUT the
-- ConsensusEvidence field the brief's §2.4 capture-schema contract and
-- §B5 changelog both call for: "events gain ... ConsensusEvidence (3860
-- panel ids + agreement bits)". Discovered as a P4 gap while activating
-- P6 (DP5(b) — chris ratified 2026-08-20); fixed here, additively, as its
-- own migration rather than folded into any P6 harness change.
--
-- ConsensusEvidence is populated ONLY on events captured from a CHAOS-3860
-- agent-user (multi-model panel) acceptance run -- every other
-- SelectionMode (human_panel, agent_receipt, agent_explicit,
-- agent_explicit_echo) leaves this column NULL, mirroring
-- source_watermarks' own additive/nullable precedent (0011). Content
-- discipline matches the brief's own words verbatim: "ids/enums, nothing
-- else" -- panel member model identity ids plus a parallel per-member
-- agreement bit (bool: did that panel member's own independently-derived
-- selection match Selected). No question text, no free-form rationale, no
-- caller-facing labels.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). ADD COLUMN IF NOT
-- EXISTS matches every prior migration's idempotency convention.

ALTER TABLE acr.context_fabric_structure_selections
    ADD COLUMN IF NOT EXISTS consensus_evidence JSONB;

-- consensus_evidence is well-formed JSON only when SelectionMode implies a
-- 3860 panel run captured it (agent_receipt is the vocabulary value that
-- surface uses, per structure_capture.go's own StructureSelectionEvent.
-- SelectionMode doc comment -- P6 runs speak the hosted contract as
-- credentialed non-panel callers, same as any other agent_receipt
-- confirmation); every other mode must leave it NULL, so a future reader
-- can trust presence as the ConsensusEvidence signal itself, never guess
-- from SelectionMode alone.
ALTER TABLE acr.context_fabric_structure_selections
    DROP CONSTRAINT IF EXISTS ck_acr_cf_structure_selections_consensus_mode;
ALTER TABLE acr.context_fabric_structure_selections
    ADD CONSTRAINT ck_acr_cf_structure_selections_consensus_mode
        CHECK (consensus_evidence IS NULL OR selection_mode = 'agent_receipt');
