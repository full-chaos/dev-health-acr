-- Durable chain identity: the result id THIS result followed.
--
-- The chain-identity request field (parent_result_id) lets a caller name the
-- turn they are continuing, but a REQUEST-ONLY pointer is not durable chain
-- identity. A later turn can only walk a conversation backwards if every turn
-- in it recorded its own parent, and several engine paths return BEFORE any
-- carry runs -- the four pre-carry veto/terminal returns and the answer-reuse
-- hit. A chain whose middle turn was vetoed on an unrelated axis would have a
-- hole in it, and the turn after the hole could not reach anything earlier.
-- So ancestry is stamped by every Save, independent of whether any axis was
-- carried or disclosed.
--
-- STORE METADATA, NOT A RESULT WIRE FIELD, deliberately -- the same choice
-- graph_epoch made in 0021 and for the same kind of reason. Only the SERVER
-- walks this chain (the carry resolvers read it through an org-scoped
-- InvestigationResultStore.Get), so no client needs to read it, and adding a
-- field to the result payload would break every consumer pinning the result
-- schema with additionalProperties:false until its pin was bumped. Disclosure
-- of a carry's ORIGIN is a separate concern and already has a home on the
-- wire: ContextFabricConfirmedStructureEntry.PriorResultID.
--
-- NULLABLE with no default, matching every additive column since 0011:
-- pre-migration rows and rows saved by a store that never had a parent read
-- back NULL, and NULL means "no recorded parent" -- never "unknown, assume
-- something". The walk fails closed on NULL exactly as it does on a missing
-- reference.
--
-- No foreign key to result_id, on purpose. A parent can be legitimately
-- absent later (retention, purge, an org's graph rebuilt and old results
-- swept), and an FK would either block those deletions or cascade them into
-- rewriting history. A dangling parent is a normal, expected state that the
-- walk already handles: the org-scoped Get simply misses and the carry
-- reports miss_unloadable.
--
-- Bounded 8..256, the SAME bound the contract puts on parent_result_id and on
-- prior_*_receipts[].result_id, so the database and the wire cannot disagree
-- about what a well-formed result id is.
--
-- No index. The walk resolves parent BY result_id, which is already the
-- table's key; nothing looks results up by their parent, and an unused index
-- on a hot insert path is a cost with no reader.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction). Every ALTER below is
-- independently idempotent.

ALTER TABLE acr.context_fabric_investigation_results
    ADD COLUMN IF NOT EXISTS parent_result_id TEXT;

ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_parent_result_id_len;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_parent_result_id_len
        CHECK (parent_result_id IS NULL OR char_length(parent_result_id) BETWEEN 8 AND 256);

-- A result may not name ITSELF as its parent. A self-referencing row would
-- make the bounded carry walk revisit its own origin on the first hop; the
-- walk's visited-set already terminates it, but a row that cannot be
-- meaningful should not be storable in the first place.
ALTER TABLE acr.context_fabric_investigation_results
    DROP CONSTRAINT IF EXISTS ck_acr_cf_investigation_results_parent_not_self;
ALTER TABLE acr.context_fabric_investigation_results
    ADD CONSTRAINT ck_acr_cf_investigation_results_parent_not_self
        CHECK (parent_result_id IS NULL OR parent_result_id <> result_id);
