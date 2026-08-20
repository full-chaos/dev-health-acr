-- CHAOS-3977 P5 (codex adversarial review round 2/3, medium finding,
-- repro-confirmed and fixed): the extended source-ineligibility rule
-- (design brief §2.1/v3/B2 -- "NO structure-bearing result is ever a
-- reuse source") was only ever implemented on the reuse-LOOKUP side
-- (Engine's DP11 bypass) until this same ticket's own reuseColumnsFor fix
-- closed the WRITE side. That fix governs future Saves only -- it cannot
-- retroactively change rows a pre-fix binary already wrote with reuse
-- columns populated. This migration is the one-time cleanup: clear the
-- reuse-key columns (never the payload itself, never any other content)
-- on every EXISTING row whose own payload carries a non-null
-- structure_needs or a non-empty confirmed_structure array -- exactly the
-- SAME predicate reuseColumnsFor now applies going forward, applied once,
-- retroactively, here.
--
-- Never touches the immutable investigation result itself (payload,
-- generated_at, or any non-reuse column) -- only the reuse-participation
-- columns, the same "this row simply never becomes reusable" degradation
-- reuseColumnsFor already uses for every OTHER reason a row is
-- reuse-ineligible (a punctuation-only question, answer reuse disabled at
-- save time, etc.) -- clearing these columns is byte-identical in effect
-- to that row never having attempted reuse participation.
--
-- No inline BEGIN/COMMIT: migrations/postgres/runner.go's applyMigration
-- already wraps this file in its own transaction.

UPDATE acr.context_fabric_investigation_results
SET question_hash = NULL,
    contract_version = NULL,
    projection_version = NULL,
    model_identity = NULL,
    source_watermarks = NULL,
    invalidation_epoch = NULL
WHERE (question_hash IS NOT NULL OR source_watermarks IS NOT NULL OR invalidation_epoch IS NOT NULL)
  AND (
    (payload ? 'structure_needs')
    OR (
      payload ? 'confirmed_structure'
      AND jsonb_typeof(payload -> 'confirmed_structure') = 'array'
      AND jsonb_array_length(payload -> 'confirmed_structure') > 0
    )
  );
