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
		ModelIdentity:     result.Versions.ModelIdentity,
	}
}

// saveWithReuseSnapshot mirrors exactly what Engine does in production
// (CHAOS-3782 Codex round-1 F1, and team-lead's veto of context-smuggled
// snapshots): capture the CURRENT source-watermark snapshot via
// Store.SnapshotSourceWatermarks, then pass it to Save as its own
// explicit parameter. Every test in this file that wants a result to
// actually become reusable must go through this helper, not call
// store.Save directly -- Save no longer queries watermarks itself (that
// was the F1 bug), so a bare store.Save(ctx, principal, result, nil) with
// no snapshot passed now deliberately leaves every reuse column NULL
// (see TestF1_SaveLeavesReuseColumnsNullWithoutAThreadedSnapshot).
func saveWithReuseSnapshot(t *testing.T, ctx context.Context, store *pginvestigation.Store, principal storage.Principal, result contextfabric.InvestigationResult) {
	t.Helper()
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, principal, result, snapshot))
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
	// nil reuse snapshot, exactly what a Save call from a caller that
	// doesn't know about answer reuse would pass.
	require.NoError(t, store.Save(ctx, principal, result, nil))

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
		key.ModelIdentity = "openai-compatible/gpt-5-mini"
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
// Codex round-1 F6 regression, isolating created_at from generated_at
// specifically: a result whose APP-SUPPLIED GeneratedAt is deliberately
// set far in the future (a value this store's Save never validates or
// corrects) must still be judged by when the row was actually written
// (created_at, DB clock_timestamp()), not by that untrustworthy
// generated_at. If the staleness window used generated_at, this
// far-future value would make the row look arbitrarily fresh forever;
// bounding on created_at means the tiny real-world save latency alone
// determines staleness, unaffected by what GeneratedAt claims.
func TestF6_StalenessWindowUsesCreatedAtNotAppSuppliedGeneratedAt(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-clockauthority"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_clockskew01", principal.OrgID, "Is the future-dated result still reusable?")
	result.GeneratedAt = time.Now().UTC().Add(24 * time.Hour) // app clock claims "tomorrow"
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	// created_at is real "now" regardless of GeneratedAt, so this is
	// still comfortably inside a one-hour window.
	_, ok, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected created_at (DB clock), not the future-dated app-supplied generated_at, to govern the staleness window")
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
