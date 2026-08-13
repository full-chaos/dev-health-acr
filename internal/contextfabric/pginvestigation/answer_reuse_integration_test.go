package pginvestigation_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// setCheckpointWatermark upserts a minimal, valid
// acr.context_fabric_projection_checkpoints row directly -- these tests
// exercise pginvestigation.Store's reuse machinery, which reads that table
// with plain SQL (see Store.currentSourceWatermarks), not the checkpoint
// store's own Go API.
func setCheckpointWatermark(t *testing.T, ctx context.Context, db *sql.DB, orgID, source, watermark string) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, cursor, source_version, backend_watermark, updated_at)
VALUES ($1, $2, 'cursor', 'v1', $3, now())
ON CONFLICT (org_id, source) DO UPDATE SET backend_watermark = EXCLUDED.backend_watermark, updated_at = now()`,
		orgID, source, watermark)
	require.NoError(t, err)
}

// deleteCheckpoint removes a checkpoint row entirely, for F3's
// source-removed/replaced scenarios.
func deleteCheckpoint(t *testing.T, ctx context.Context, db *sql.DB, orgID, source string) {
	t.Helper()
	_, err := db.ExecContext(ctx, `DELETE FROM acr.context_fabric_projection_checkpoints WHERE org_id = $1 AND source = $2`, orgID, source)
	require.NoError(t, err)
}

// reusableResult returns a valid InvestigationResult sharing validResult's
// shape, scoped to a specific org/question so each test can control
// exactly what a ReuseKey lookup should and should not match.
func reusableResult(resultID, orgID, question string) contextfabric.InvestigationResult {
	result := validResult(resultID)
	result.Question = question
	return result
}

func mustReuseStore(t *testing.T, db *sql.DB, maxAge time.Duration) *pginvestigation.Store {
	t.Helper()
	store, err := pginvestigation.NewStore(db, pginvestigation.WithAnswerReuse(maxAge))
	require.NoError(t, err)
	return store
}

func reuseKeyFor(result contextfabric.InvestigationResult) contextfabric.ReuseKey {
	return contextfabric.ReuseKey{
		QuestionHash:      contextfabric.QuestionHash(result.Question),
		ContractVersion:   result.Versions.ContractVersion,
		ProjectionVersion: result.Versions.ProjectionVersion,
		// A single-member chain: the exact identity this result was
		// stored under. Most tests in this file want the baseline "the
		// key that was actually stored still matches" case; CHAOS-3786
		// tests below build a wider chain explicitly.
		ModelIdentities: []string{result.Versions.ModelIdentity},
		// CHAOS-3781: derived from the result's own interpreted axis, the
		// same way Save derives the column it is matched against.
		TimeAxisKey: contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext),
	}
}

// saveWithReuseSnapshot mirrors exactly what Engine does in production
// (CHAOS-3782 Codex round-1 F1, round-2 finding #7, and team-lead's veto
// of context-smuggled snapshots): capture the CURRENT source-watermark
// snapshot AND the CURRENT rebuild epoch, both via Store's own
// snapshot-time methods, then pass both to Save as its own explicit
// parameters. Every test in this file that wants a result to actually
// become reusable must go through this helper, not call store.Save
// directly -- Save no longer queries either of these itself (that was
// the F1 bug, and reuseEpoch is the same discipline applied to a second
// piece of snapshot-time state), so a bare
// store.Save(ctx, principal, result, nil, nil) with nothing passed now
// deliberately leaves every reuse column NULL (see
// TestF1_SaveLeavesReuseColumnsNullWithoutAThreadedSnapshot).
func saveWithReuseSnapshot(t *testing.T, ctx context.Context, store *pginvestigation.Store, principal storage.Principal, result contextfabric.InvestigationResult) {
	t.Helper()
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch))
}

// TestFindReusable_HappyPathRoundTrip proves the baseline: a result saved
// through a reuse-enabled Store, with an unchanged checkpoint watermark
// and inside the staleness window, is found by FindReusable using the
// exact ReuseKey a fresh Investigate call would compute for the same
// question.
func TestFindReusable_HappyPathRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-happy"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_happy01", principal.OrgID, "Is the auth migration on track?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	found, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate")
	require.Equal(t, result.ResultID, found.ResultID)
}

// TestF1_SaveLeavesReuseColumnsNullWithoutAThreadedSnapshot is the Codex
// round-1 F1 regression at the store level: Save must NEVER fall back to
// querying "current" watermarks itself when no snapshot was threaded
// through ctx -- that would reopen the exact TOCTOU race F1 closes. A
// plain store.Save with no snapshot attached must leave the result
// permanently unreusable, even though checkpoints exist and answer reuse
// is enabled on the Store.
func TestF1_SaveLeavesReuseColumnsNullWithoutAThreadedSnapshot(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-nosnapshot"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_nosnap01", principal.OrgID, "Did this get saved without a snapshot?")
	// Deliberately NOT using saveWithReuseSnapshot -- plain Save with a
	// nil reuse snapshot and a nil epoch, exactly what a Save call from a
	// caller that doesn't know about answer reuse would pass.
	require.NoError(t, store.Save(ctx, principal, result, nil, nil))

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a Save with no threaded snapshot to never be reusable")
}

// TestAC_3782_3_WatermarkAdvanceForcesFreshInvestigation binds AC-3782-3 at
// the store level: advancing the projection watermark of a used source
// forces FindReusable to miss, even though nothing else about the
// candidate changed.
func TestAC_3782_3_WatermarkAdvanceForcesFreshInvestigation(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-watermark"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_watermark01", principal.OrgID, "What is blocking the release?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate before the watermark advances")

	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-2")

	_, ok, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected no reusable candidate once the source watermark advanced")
}

// TestAC_3782_3_NewSourceAppearingAlsoForcesFresh proves the conservative
// reading of condition 3 documented on
// migrations/postgres/0011_context_fabric_answer_reuse.sql and
// Store.watermarksStillMatch: a source appearing AFTER the candidate was
// generated -- one Save could not have snapshotted -- must also count as
// a mismatch, not be silently ignored.
func TestAC_3782_3_NewSourceAppearingAlsoForcesFresh(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-newsource"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_newsource01", principal.OrgID, "What changed in the last sprint?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	setCheckpointWatermark(t, ctx, db, principal.OrgID, "github", "wm-1")

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected no reusable candidate once a new source appeared")
}

// TestAC_3782_3_ReplacedSourceWithEmptyWatermarkForcesFresh is the Codex
// round-1 F3 regression: a source REMOVED and a DIFFERENT source ADDED
// (net checkpoint count unchanged) must still be caught, even when the
// removed source's stored watermark was the empty string -- the schema's
// own default (migration 0006) and therefore a genuinely valid value, not
// a sentinel for "absent." Value-equality alone (current[source] == "" by
// zero-value when the key is simply missing) would wrongly treat this as
// unchanged; explicit key presence is required.
func TestAC_3782_3_ReplacedSourceWithEmptyWatermarkForcesFresh(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-replaced"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "") // empty, valid, default watermark

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_replaced01", principal.OrgID, "Is the replaced source still tracked?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "sanity: unchanged empty-string watermark must still match")

	// Replace "linear" with "github" -- same count (1), different source
	// name, and the new source also happens to have an empty watermark.
	deleteCheckpoint(t, ctx, db, principal.OrgID, "linear")
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "github", "")

	_, ok, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a replaced source (even with an empty watermark on both sides) to force a mismatch")
}

// TestAC_3782_4_RebuildInvalidationForcesFreshInvestigation binds
// AC-3782-4 at the store level: InvalidateOrganizationReuse makes every
// previously-reusable result for that organization unreusable, with the
// projection watermark left completely unchanged -- the exact D15 gap
// watermark-only comparison cannot close on its own.
func TestAC_3782_4_RebuildInvalidationForcesFreshInvestigation(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-rebuild"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-stable")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_rebuild01", principal.OrgID, "What is the current status?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate before the rebuild invalidation")

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, principal.OrgID))

	_, ok, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected no reusable candidate once the organization's rebuild invalidation was recorded")
}

// TestAC_3782_4_RebuildInvalidationIsOrganizationScoped proves the
// invalidation does not leak across organizations -- a rebuild for one
// organization must not affect another's reusable results.
func TestAC_3782_4_RebuildInvalidationIsOrganizationScoped(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	rebuiltOrg := storage.Principal{OrgID: "org-reuse-rebuilt"}
	otherOrg := storage.Principal{OrgID: "org-reuse-untouched"}
	setCheckpointWatermark(t, ctx, db, rebuiltOrg.OrgID, "linear", "wm-a")
	setCheckpointWatermark(t, ctx, db, otherOrg.OrgID, "linear", "wm-b")

	store := mustReuseStore(t, db, time.Hour)
	rebuiltResult := reusableResult("result_reuse_rebuilt01", rebuiltOrg.OrgID, "Same question")
	otherResult := reusableResult("result_reuse_untouched01", otherOrg.OrgID, "Same question")
	saveWithReuseSnapshot(t, ctx, store, rebuiltOrg, rebuiltResult)
	saveWithReuseSnapshot(t, ctx, store, otherOrg, otherResult)

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, rebuiltOrg.OrgID))

	_, ok, err := store.FindReusable(ctx, rebuiltOrg, reuseKeyFor(rebuiltResult))
	require.NoError(t, err)
	require.False(t, ok, "expected the rebuilt organization's result to be invalidated")

	_, ok, err = store.FindReusable(ctx, otherOrg, reuseKeyFor(otherResult))
	require.NoError(t, err)
	require.True(t, ok, "expected the other organization's result to remain reusable")
}

// TestAC_3782_7_VersionMismatchNeverReused binds AC-3782-7: a result
// stored under an older contract version, projection version, or model
// identity is never reused. Each subtest changes exactly one dimension of
// the lookup key away from what was actually stored.
func TestAC_3782_7_VersionMismatchNeverReused(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-versions"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_versions01", principal.OrgID, "Are we release-ready?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	baseline := reuseKeyFor(result)

	t.Run("contract_version", func(t *testing.T) {
		key := baseline
		key.ContractVersion = "context_fabric_investigation_result.v2"
		_, ok, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("projection_version", func(t *testing.T) {
		key := baseline
		key.ProjectionVersion = "projection-v2"
		_, ok, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("model_identity", func(t *testing.T) {
		key := baseline
		key.ModelIdentities = []string{"openai-compatible/gpt-5-mini"}
		_, ok, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("unchanged_key_still_matches", func(t *testing.T) {
		_, ok, err := store.FindReusable(ctx, principal, baseline)
		require.NoError(t, err)
		require.True(t, ok, "sanity: the unmodified key must still match")
	})
}

// TestChaos3786_FindReusableMatchesAnyIdentityInTheCurrentChain is the
// hit-rate probe: a result stored under the FALLBACK model's identity must
// be found when the lookup key's ModelIdentities chain contains BOTH the
// primary and the fallback -- proving the predicate is chain membership
// (`model_identity = ANY(...)`), not a single equality.
func TestChaos3786_FindReusableMatchesAnyIdentityInTheCurrentChain(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-chain-hit"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_chain01", principal.OrgID, "Did the fallback model answer this?")
	result.Versions.ModelIdentity = "openai/gpt-5-fallback"
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	key := reuseKeyFor(result)
	key.ModelIdentities = []string{"openai/gpt-5-nano", "openai/gpt-5-fallback"}

	found, ok, err := store.FindReusable(ctx, principal, key)
	require.NoError(t, err)
	require.True(t, ok, "expected the fallback-produced result to be reusable while the fallback is still in the current chain")
	require.Equal(t, result.ResultID, found.ResultID)
}

// TestChaos3786_FindReusableMissesWhenChainNoLongerNamesTheStoredIdentity is
// the correctness probe: a result stored under an OLD fallback identity
// must stop matching once the lookup chain no longer names it -- even
// though the primary identity in the chain is completely unchanged. This
// is defect (b) from the CHAOS-3786 issue: a fallback reconfiguration must
// invalidate reuse for what the OLD fallback produced.
func TestChaos3786_FindReusableMissesWhenChainNoLongerNamesTheStoredIdentity(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-chain-miss"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_chain02", principal.OrgID, "Did the OLD fallback model answer this?")
	result.Versions.ModelIdentity = "openai/gpt-5-fallback-old"
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	key := reuseKeyFor(result)
	// Primary unchanged; the org reconfigured its fallback model.
	key.ModelIdentities = []string{"openai/gpt-5-nano", "openai/gpt-5-fallback-new"}

	_, ok, err := store.FindReusable(ctx, principal, key)
	require.NoError(t, err)
	require.False(t, ok, "expected a miss: the stored result's producing model is no longer in the current chain")
}

// TestChaos3786_InvalidateOrganizationReuseCatchesWhatChainMembershipCannot
// is the codex round-1 P1(b) red->green probe: a config change that does
// NOT alter the provider/model identity strings at all (e.g. a BaseURL- or
// credential-only change -- modeled here as simply re-saving the SAME
// fallback identity) leaves chain membership blind: the row's identity is
// still, and remains, a member of the chain, so FindReusable keeps hitting
// on identity/chain grounds alone. Only an explicit
// InvalidateOrganizationReuse call -- which the model-config PUT/DELETE
// routes now make on every write (internal/api/context_fabric_model_config_routes.go)
// -- closes this gap, by bumping the epoch unconditionally rather than
// relying on the identity dimension to have detected anything.
func TestChaos3786_InvalidateOrganizationReuseCatchesWhatChainMembershipCannot(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-epoch-catches-chain-blind-spot"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_epochblind01", principal.OrgID, "Did the fallback model answer this before a credential rotation?")
	result.Versions.ModelIdentity = "openai/gpt-5-fallback"
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	key := reuseKeyFor(result)
	key.ModelIdentities = []string{"openai/gpt-5-nano", "openai/gpt-5-fallback"}

	// Before any config change: chain membership hits, exactly like
	// TestChaos3786_FindReusableMatchesAnyIdentityInTheCurrentChain.
	_, ok, err := store.FindReusable(ctx, principal, key)
	require.NoError(t, err)
	require.True(t, ok, "sanity: the candidate must be reusable before any invalidation")

	// The organization writes a new model configuration whose
	// provider/model identity strings are UNCHANGED (e.g. only the
	// credential or BaseURL changed) -- the model-config PUT route calls
	// this on every successful write, regardless of which fields changed.
	require.NoError(t, store.InvalidateOrganizationReuse(ctx, principal.OrgID))

	// The SAME key -- chain membership alone would still match, since
	// neither identity string moved -- must now miss: the epoch bump is
	// what actually invalidates it, not a chain change.
	_, ok, err = store.FindReusable(ctx, principal, key)
	require.NoError(t, err)
	require.False(t, ok, "expected a miss: InvalidateOrganizationReuse must quarantine the candidate even though its identity is still in the chain")
}

// TestFindReusable_OutsideStalenessWindowIsAMiss proves condition 4's age
// bound independent of rebuild invalidation: a candidate created before
// (now - maxAge) is never reused, even with an unchanged watermark and a
// matching key on every other dimension. Codex round-1 F6: this bounds
// against created_at (DB clock_timestamp()), not the app-supplied
// generated_at -- both happen to be "now" for a freshly-saved row in this
// test, so it does not by itself distinguish the two, but it does prove
// the bound is enforced at all.
func TestFindReusable_OutsideStalenessWindowIsAMiss(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-stale"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	// A Store with reuse enabled but effectively zero staleness tolerance:
	// by the time FindReusable runs, the just-saved row is already older
	// than 1 nanosecond.
	store := mustReuseStore(t, db, time.Nanosecond)
	result := reusableResult("result_reuse_stale01", principal.OrgID, "How is the migration going?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected the candidate to already be outside a near-zero staleness window")
}

// TestF6_StalenessWindowUsesCreatedAtNotAppSuppliedGeneratedAt is the
// Codex round-1 F6 regression, REWORKED per Codex round-2 finding #6.
//
// The ORIGINAL version of this test set GeneratedAt 24h in the future,
// inside a generous one-hour window, and asserted reuse succeeded. That
// assertion is a FALSE PASS: a future-dated generated_at is "inside the
// window" under EITHER clock -- the real created_at (comfortably fresh)
// AND a hypothetical generated_at-based check (a claimed future
// generation time always looks fresh) both say "reusable" there, so the
// test could never have caught a regression back to the untrustworthy
// app clock. It was asserting a property every implementation shares,
// not the one this test exists to bind.
//
// This version instead uses two cases specifically chosen so a
// generated_at-based implementation would give the WRONG answer while a
// created_at-based one gives the right one:
//
//  1. GeneratedAt set far in the PAST, well outside the window --
//     created_at (real save time) is fresh, so the correct
//     implementation still finds the row reusable; a generated_at-based
//     implementation would see the stale claimed generation time and
//     wrongly miss.
//  2. GeneratedAt set far in the FUTURE, but with a near-zero staleness
//     window (mirroring TestFindReusable_OutsideStalenessWindowIsAMiss)
//     -- created_at is already outside that window by the time
//     FindReusable runs, so the correct implementation misses; a
//     generated_at-based implementation would see the far-future claimed
//     generation time as eternally "inside" any window and wrongly hit.
func TestF6_StalenessWindowUsesCreatedAtNotAppSuppliedGeneratedAt(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)

	t.Run("past_generated_at_is_still_reusable_via_created_at", func(t *testing.T) {
		principal := storage.Principal{OrgID: "org-reuse-clockskew-past"}
		setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

		store := mustReuseStore(t, db, time.Hour)
		result := reusableResult("result_reuse_clockskew_past01", principal.OrgID, "Is the past-dated result still reusable via created_at?")
		result.GeneratedAt = time.Now().UTC().Add(-48 * time.Hour) // app clock claims "two days ago" -- well outside a 1h window
		saveWithReuseSnapshot(t, ctx, store, principal, result)

		_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
		require.NoError(t, err)
		require.True(t, ok, "expected created_at (real save time), not the stale app-supplied generated_at, to govern the staleness window")
	})

	t.Run("future_generated_at_is_still_stale_via_created_at", func(t *testing.T) {
		principal := storage.Principal{OrgID: "org-reuse-clockskew-future"}
		setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

		// Near-zero staleness tolerance, exactly like
		// TestFindReusable_OutsideStalenessWindowIsAMiss: by the time
		// FindReusable runs, the just-saved row is already older than 1
		// nanosecond of REAL time, regardless of what GeneratedAt claims.
		store := mustReuseStore(t, db, time.Nanosecond)
		result := reusableResult("result_reuse_clockskew_future01", principal.OrgID, "Is the future-dated result still stale via created_at?")
		result.GeneratedAt = time.Now().UTC().Add(24 * time.Hour) // app clock claims "tomorrow" -- would look eternally fresh under generated_at
		saveWithReuseSnapshot(t, ctx, store, principal, result)

		_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
		require.NoError(t, err)
		require.False(t, ok, "expected created_at (real save time), not the future-dated app-supplied generated_at, to govern the staleness window")
	})
}

// TestFindReusable_DoesNotCrossOrganizations proves org scoping holds for
// the reuse lookup itself, mirroring InvestigationResultStore.Get's own
// binding precondition (see that interface's doc comment).
func TestFindReusable_DoesNotCrossOrganizations(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	owner := storage.Principal{OrgID: "org-reuse-owner"}
	other := storage.Principal{OrgID: "org-reuse-stranger"}
	setCheckpointWatermark(t, ctx, db, owner.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_scoped01", owner.OrgID, "What is the status?")
	saveWithReuseSnapshot(t, ctx, store, owner, result)

	_, ok, err := store.FindReusable(ctx, other, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected no cross-organization reuse")
}

// TestSnapshotRebuildEpoch_StartsAtZeroAndAdvancesOnEachInvalidation is
// the direct probe for RebuildEpoch's baseline and monotonic-advance
// contract (Codex round-2 finding #7): a never-invalidated organization
// reads epoch 0, and each InvalidateOrganizationReuse call advances it by
// exactly one.
func TestSnapshotRebuildEpoch_StartsAtZeroAndAdvancesOnEachInvalidation(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store := mustReuseStore(t, db, time.Hour)
	orgID := "org-reuse-epoch-baseline"

	epoch, err := store.SnapshotRebuildEpoch(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, int64(0), epoch, "expected a never-invalidated organization to read epoch 0")

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, orgID))
	epoch, err = store.SnapshotRebuildEpoch(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, int64(1), epoch, "expected the first invalidation to advance epoch to 1")

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, orgID))
	epoch, err = store.SnapshotRebuildEpoch(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, int64(2), epoch, "expected a second invalidation to advance epoch again")
}

// TestAC_3782_4_RebuildBetweenSnapshotAndSaveIsCaughtByEpochNotTimestamp
// is the Codex round-2 finding #7 probe. It reproduces the race a
// created_at-vs-invalidated_at timestamp comparison cannot close: Engine
// captures the reuse epoch BEFORE reading the graph; a concurrent
// rebuild's invalidation lands while that read (and the synthesis after
// it) is still in flight; Save only happens once all of that finishes,
// so its row's created_at ends up AFTER the invalidation's timestamp --
// exactly the shape a timestamp-only check would have wrongly called
// "fresh." Before this fix (a timestamp-only check), this test would have
// failed: FindReusable would have returned ok=true.
func TestAC_3782_4_RebuildBetweenSnapshotAndSaveIsCaughtByEpochNotTimestamp(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-epoch-race"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)

	// Engine-side capture, BEFORE the (simulated) graph read: watermark
	// snapshot and epoch, both as of this moment.
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)
	require.Equal(t, int64(0), epoch, "sanity: organization has never been invalidated yet")

	// A rebuild completes WHILE the (simulated) graph read/synthesis for
	// this investigation is still in flight -- i.e. strictly between this
	// investigation's snapshot capture and its eventual Save.
	require.NoError(t, store.InvalidateOrganizationReuse(ctx, principal.OrgID))

	// Save lands AFTER the invalidation (created_at > invalidated_at) --
	// exactly what a timestamp-only check would have called "fresh" --
	// but carries the STALE epoch captured before the rebuild.
	result := reusableResult("result_reuse_epoch_race01", principal.OrgID, "Did the mid-flight rebuild get caught?")
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch))

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected the epoch fence to catch a rebuild that landed between snapshot capture and Save, even though created_at > invalidated_at")
}

// TestAC_3782_4_InvestigationStartedAfterRebuildIsReusable is the control
// case: an investigation whose epoch snapshot is captured AFTER a
// rebuild completes (the ordinary, non-racy case) must still be
// reusable -- proving the epoch fence does not over-invalidate every
// investigation that merely happens to run after SOME rebuild, only ones
// racing a rebuild that lands DURING their own snapshot-to-save window.
func TestAC_3782_4_InvestigationStartedAfterRebuildIsReusable(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-epoch-post-rebuild"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	require.NoError(t, store.InvalidateOrganizationReuse(ctx, principal.OrgID))

	result := reusableResult("result_reuse_epoch_postrebuild01", principal.OrgID, "Is a post-rebuild investigation still reusable?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected an investigation snapshotted AFTER the rebuild to remain reusable")
}

// TestFindReusable_NilEpochAtSaveIsNeverReusable is the store-side F7
// analog of TestF1_SaveLeavesReuseColumnsNullWithoutAThreadedSnapshot: a
// Save with a real watermark snapshot but NO epoch (the Engine-side epoch
// read failed independently of the watermark read) must leave the row
// permanently unreusable, never falling back to treating a nil epoch as
// "matches everything."
func TestFindReusable_NilEpochAtSaveIsNeverReusable(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-epoch-nil"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)

	result := reusableResult("result_reuse_epoch_nil01", principal.OrgID, "Was the no-epoch save left unreusable?")
	require.NoError(t, store.Save(ctx, principal, result, snapshot, nil))

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a Save with a watermark snapshot but no epoch to never be reusable")
}

// TestInvalidateOrganizationReuse_AdvancesEpochEvenWhenInvalidatedAtDoesNotMoveForward
// is CHAOS-3782 Codex round-3 finding 3. epoch is a COUNTER of
// invalidation EVENTS, not a derivative of the invalidated_at clock: every
// call to InvalidateOrganizationReuse represents a real rebuild, and must
// bump the epoch, whether or not clock_timestamp() happens to advance past
// the previously recorded invalidated_at. The old UPSERT only bumped epoch
// under `WHERE invalidated_at < EXCLUDED.invalidated_at`, so two
// invalidations landing at (or the second at/before) the same recorded
// timestamp silently skipped the bump -- leaving a stale result reusable
// through a rebuild that, from the epoch's perspective, never happened.
//
// This reproduces that directly: the invalidations row is seeded with
// invalidated_at set into the FUTURE relative to real clock_timestamp(),
// so the guard's `<` comparison is false on the very next call -- the same
// shape a same-timestamp race collapses to. Before the fix this call left
// epoch unchanged; it must now advance by exactly one every time.
func TestInvalidateOrganizationReuse_AdvancesEpochEvenWhenInvalidatedAtDoesNotMoveForward(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	store := mustReuseStore(t, db, time.Hour)
	orgID := "org-reuse-epoch-clock-stall"

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, orgID))
	epoch, err := store.SnapshotRebuildEpoch(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, int64(1), epoch, "sanity: first invalidation advances epoch to 1")

	// Push the recorded invalidated_at into the future so the next real
	// InvalidateOrganizationReuse call's clock_timestamp() cannot exceed
	// it -- the same "no forward movement" condition an exact-tie
	// timestamp produces.
	_, err = db.ExecContext(ctx,
		`UPDATE acr.context_fabric_reuse_invalidations SET invalidated_at = now() + interval '1 hour' WHERE org_id = $1`,
		orgID)
	require.NoError(t, err)

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, orgID))
	epoch, err = store.SnapshotRebuildEpoch(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, int64(2), epoch, "expected epoch to advance even though invalidated_at did not move forward")

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, orgID))
	epoch, err = store.SnapshotRebuildEpoch(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, int64(3), epoch, "expected a third invalidation to advance epoch again")
}

// TestSave_EmptyModelIdentityPersistsAsNeverReusable is CHAOS-3782 Codex
// round-3 finding 4. validate_context_fabric_result.go allows an empty
// ModelIdentity (it is optional -- see ContextFabricVersionSet's doc
// comment), but migration 0011's CHECK on the model_identity column
// rejects an empty string (only NULL or 1-513 chars). reuseColumnsFor used
// to write ” as Valid:true whenever reuse was enabled, so a
// contract-valid legacy-shaped result (no model identity) failed to Save
// at all instead of persisting as an ordinary, never-reusable row.
//
// Before the fix, this Save call failed with a CHECK-violation error; it
// must now succeed, and the row must never surface from FindReusable.
func TestSave_EmptyModelIdentityPersistsAsNeverReusable(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-empty-model-identity"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)

	result := reusableResult("result_reuse_no_model_id01", principal.OrgID, "Was the empty model identity save left unreusable?")
	result.Versions.ModelIdentity = ""
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch))

	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a Save with an empty model identity to never be reusable")
}
