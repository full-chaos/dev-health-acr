-- CHAOS-3927 P4 (codex adversarial review, HIGH finding): 0023 created
-- acr.context_fabric_structure_supersession_claims EMPTY -- it protects
-- every structure confirmation persisted FROM THAT MIGRATION ONWARD, but
-- any InvestigationResult already carrying an applied, receipt-sourced
-- ConfirmedStructure entry (P1 has computed and persisted this block,
-- inside the immutable payload JSONB, since migration 0009's table existed
-- -- 0023 only added the SEPARATE claim table, it did not change what
-- Save already wrote into payload) has NO claim protecting its redeemed
-- offer. A request submitted after 0023/0024 deploy, naming the SAME
-- (org, prior_result_id, member) tuple a pre-0023 result already redeemed,
-- would sail through canonicalizeStructure's pre-flight consult (nothing
-- claimed) and Save's own atomic insert (nothing to conflict with) and
-- "win" a claim a DIFFERENT result already, in effect, held.
--
-- This migration closes that gap with a one-time, idempotent backfill:
-- scan every already-persisted investigation_results row's own payload for
-- confirmed_structure entries that actually redeemed something
-- (disposition=applied, source=receipt), and insert the claim each one
-- implies. ON CONFLICT DO NOTHING makes re-running this migration (or
-- running it against a database where 0023/0024 already accumulated some
-- claims the normal way) safe.
--
-- Backfill ordering, stated plainly: if pre-migration duplicates already
-- exist (the SAME tuple redeemed by more than one result, which was
-- ALWAYS possible before this table existed -- there was no supersession
-- protection at all before 0023/0024), this backfill can only pick ONE
-- winner per tuple, first-writer-wins by created_at. This is the best a
-- POST-HOC backfill can do; it cannot undo a race that already happened
-- before any claim mechanism existed. Going forward from this migration,
-- the SAME atomic-claim protection Save's own transaction provides
-- (migration 0023's own header comment) makes that class of duplicate
-- impossible again.
--
-- No inline BEGIN/COMMIT (migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction).

INSERT INTO acr.context_fabric_structure_supersession_claims (org_id, prior_result_id, member, claimed_by_result_id, claimed_at)
SELECT DISTINCT ON (winner.org_id, winner.prior_result_id, winner.member)
    winner.org_id, winner.prior_result_id, winner.member, winner.result_id, winner.created_at
FROM (
    SELECT
        r.org_id,
        r.result_id,
        r.created_at,
        entry->>'prior_result_id' AS prior_result_id,
        entry->>'member'          AS member
    FROM acr.context_fabric_investigation_results r,
         -- codex round-2 adversarial review, MEDIUM finding: an unguarded
         -- jsonb_array_elements(r.payload->'confirmed_structure') aborts
         -- this ENTIRE migration the moment ANY row's confirmed_structure
         -- is not itself a JSON array -- missing key (-> yields SQL NULL),
         -- explicit JSON null, or (a hand-edited/foreign-written row,
         -- Get's own "may have reached storage some other way" defensive
         -- posture applies here too) an object or scalar. The CASE guard
         -- makes every one of those shapes degrade to an EMPTY array
         -- (jsonb_typeof(NULL::jsonb) is SQL NULL, which the equality
         -- check below also fails, so a missing key takes the same safe
         -- path as an explicit null) rather than raising -- a row this
         -- migration cannot make sense of contributes zero candidate
         -- claims, it never blocks every OTHER row's backfill.
         LATERAL jsonb_array_elements(
             CASE WHEN jsonb_typeof(r.payload->'confirmed_structure') = 'array'
                  THEN r.payload->'confirmed_structure'
                  ELSE '[]'::jsonb
             END
         ) AS entry
    WHERE entry->>'disposition' = 'applied'
      AND entry->>'source' = 'receipt'
      AND coalesce(entry->>'prior_result_id', '') <> ''
      AND coalesce(entry->>'member', '') <> ''
) AS winner
ORDER BY winner.org_id, winner.prior_result_id, winner.member, winner.created_at ASC, winner.result_id ASC
ON CONFLICT (org_id, prior_result_id, member) DO NOTHING;
