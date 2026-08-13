-- CHAOS-3786: one-time reuse-epoch cutover for every organization with an
-- existing investigation result (codex round-1 P1 ruling).
--
-- Before this fix, a fallback-produced investigation result was persisted
-- under the PRIMARY model's identity: genkitruntime.mergeFallbackReceipt
-- discarded the fallback leg's own Provider/Model/ModelVersion on a
-- successful fallback call, keeping the primary's (see that function's
-- updated doc comment). Such a row's model_identity column holds a valid,
-- well-formed string -- it just does not name the model that actually
-- produced the row.
--
-- Post-fix, FindReusable's model_identity = ANY(...) chain-membership
-- lookup has no way to detect this: the mislabel lives in the STORED
-- value itself, not in whether that value is still a member of the
-- current chain. A mislabeled row keeps matching for as long as the
-- PRIMARY identity it was (wrongly) saved under stays in the org's
-- current chain, even after the org's actual FALLBACK model -- the one
-- that really produced the row -- is reconfigured or removed.
--
-- Bumping every existing organization's reuse-invalidation epoch ONCE,
-- here, quarantines every row written before this migration ran:
-- FindReusable's own `invalidation_epoch = <organization's CURRENT
-- epoch>` predicate (pginvestigation/store.go) then fails for every
-- pre-cutover row, exactly as if projectionrun.Coordinator had just
-- rebuilt every organization's graph (see contextfabric.RebuildEpoch's
-- doc comment) -- without touching a single immutable payload, and
-- without a real graph rebuild actually being necessary. A FRESH
-- investigation saved after this migration runs captures the CURRENT
-- (bumped) epoch as its own invalidation_epoch, so it is unaffected going
-- forward, and reuse resumes normally for every organization.
--
-- Scoped to organizations that have at least one row in
-- acr.context_fabric_investigation_results: an organization with none has
-- nothing to quarantine. The statement mirrors
-- Store.InvalidateOrganizationReuse's own SQL exactly (same
-- unconditional epoch + 1 on conflict, per that method's own doc comment
-- on why the bump must never be timestamp-gated), so this is safe to
-- reason about as "every affected organization got one ordinary
-- programmatic invalidation call", not a special case.
INSERT INTO acr.context_fabric_reuse_invalidations (org_id, invalidated_at, epoch)
SELECT DISTINCT org_id, clock_timestamp(), 1
FROM acr.context_fabric_investigation_results
ON CONFLICT (org_id) DO UPDATE
    SET invalidated_at = EXCLUDED.invalidated_at,
        epoch = acr.context_fabric_reuse_invalidations.epoch + 1;
