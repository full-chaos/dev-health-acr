package pginvestigation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	// epoch defaults to 0 (CHAOS-3898 S2a re-key, migration 0020) -- these
	// tests exercise the pre-lifecycle, legacy-epoch reuse path, so the
	// conflict target names epoch explicitly, matching the table's current
	// primary key (org_id, epoch, source).
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, cursor, source_version, backend_watermark, updated_at)
VALUES ($1, $2, 'cursor', 'v1', $3, now())
ON CONFLICT (org_id, epoch, source) DO UPDATE SET backend_watermark = EXCLUDED.backend_watermark, updated_at = now()`,
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

// testReuseRetrievalIdentity is the CHAOS-3833 deployment-current retrieval
// discriminator pair every reuse-participating Save and lookup in this file
// shares -- mirroring how production threads ONE EngineOptions value to both
// sides. Tests that want the retrieval dimension to MISS build a divergent
// value explicitly.
var testReuseRetrievalIdentity = contextfabric.ReuseRetrievalIdentity{
	EmbedRetrievalIdentity: "none",
	RetrievalPolicyVersion: "rp1",
}

// testReusePromptVersions is the CHAOS-3862 deployment-current
// interpretation/synthesis prompt version pair every reuse-participating
// Save and lookup in this file shares -- mirroring testReuseRetrievalIdentity
// exactly. Tests that want the prompt-version dimension to MISS build a
// divergent value explicitly.
var testReusePromptVersions = contextfabric.ReusePromptVersions{
	InterpretationPromptVersion: "context-fabric-interpretation.v7",
	SynthesisPromptVersion:      "context-fabric-synthesis.v9",
}

// testReuseVersionAuthorities is the CHAOS-3862 round-2 deployment-current
// trio (query shape, canonical fact registry, model-output schema) every
// reuse-participating Save and lookup in this file shares -- mirroring
// testReuseRetrievalIdentity/testReusePromptVersions exactly. Tests that
// want one of these dimensions to MISS build a divergent value explicitly.
var testReuseVersionAuthorities = contextfabric.ReuseVersionAuthorities{
	QueryVersion:             "devhealthfacts.clickhouse.v1",
	CanonicalServiceVersion:  "context-fabric-facts.v1",
	ModelOutputSchemaVersion: "context-fabric-model-output.v1",
	// CHAOS-3884: same mirrored discipline, one more dimension.
	IdentityNormalizationVersion: "identity_norm_v1",
	// CHAOS-3900 W1: same mirrored discipline, one more dimension.
	WindowInferenceVersion: contextfabric.WindowInferenceVersion,
	// CHAOS-4085: same mirrored discipline, one more dimension -- the
	// commit-gate fence. FindReusable fails CLOSED on an unset value, so
	// leaving this blank would make every test in this file miss.
	CommitGateVersion: contextfabric.CommitGateVersion,
	// CHAOS-4398 PR3 (R4 ruling): same mirrored discipline, one more
	// dimension -- the cohort ranking formula fence. FindReusable fails
	// CLOSED on an unset value, so leaving this blank would make every
	// test in this file miss.
	RankingFormulaVersion: contextfabric.RankingFormulaVersion,
	// CHAOS-4634 (S4): same mirrored discipline, one more dimension -- the
	// family definition table fence. FindReusable fails CLOSED on an
	// unset value, so leaving this blank would make every test in this
	// file miss.
	QuestionFamilyVersion: contextfabric.QuestionFamilyTableVersion,
}

func reuseKeyFor(result contextfabric.InvestigationResult) contextfabric.ReuseKey {
	return contextfabric.ReuseKey{
		// CHAOS-3833: the same pair Save persisted, compared conjunctively.
		EmbedRetrievalIdentity: testReuseRetrievalIdentity.EmbedRetrievalIdentity,
		RetrievalPolicyVersion: testReuseRetrievalIdentity.RetrievalPolicyVersion,
		// CHAOS-3862: same conjunctive-equality mirror, one dimension over.
		InterpretationPromptVersion: testReusePromptVersions.InterpretationPromptVersion,
		SynthesisPromptVersion:      testReusePromptVersions.SynthesisPromptVersion,
		// CHAOS-3862 round 2: same mirror, three MORE dimensions.
		QueryVersion:             testReuseVersionAuthorities.QueryVersion,
		CanonicalServiceVersion:  testReuseVersionAuthorities.CanonicalServiceVersion,
		ModelOutputSchemaVersion: testReuseVersionAuthorities.ModelOutputSchemaVersion,
		// CHAOS-3884: same mirror, one more dimension.
		IdentityNormalizationVersion: testReuseVersionAuthorities.IdentityNormalizationVersion,
		// CHAOS-3900 W1: same mirror, one more dimension.
		WindowInferenceVersion: testReuseVersionAuthorities.WindowInferenceVersion,
		// CHAOS-4085: same mirror, one more dimension (the commit-gate
		// fence).
		CommitGateVersion: testReuseVersionAuthorities.CommitGateVersion,
		// CHAOS-4398 PR3 (R4 ruling): same mirror, one more dimension (the
		// cohort ranking formula fence).
		RankingFormulaVersion: testReuseVersionAuthorities.RankingFormulaVersion,
		// CHAOS-4634 (S4): same mirror, one more dimension (the family
		// definition table fence).
		QuestionFamilyVersion: testReuseVersionAuthorities.QuestionFamilyVersion,
		QuestionHash:          contextfabric.QuestionHash(result.Question),
		ContractVersion:       result.Versions.ContractVersion,
		ProjectionVersion:     result.Versions.ProjectionVersion,
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
// store.Save(ctx, principal, result, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})) with nothing passed now
// deliberately leaves every reuse column NULL (see
// TestF1_SaveLeavesReuseColumnsNullWithoutAThreadedSnapshot).
func saveWithReuseSnapshot(t *testing.T, ctx context.Context, store *pginvestigation.Store, principal storage.Principal, result contextfabric.InvestigationResult) {
	t.Helper()
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch, contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext), testReuseRetrievalIdentity, testReusePromptVersions, testReuseVersionAuthorities, 0, ""))
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

	found, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate")
	require.Equal(t, result.ResultID, found.ResultID)
}

// TestFindReusable_CohortMintedClaimedFactsSurviveAReuseHit is CHAOS-4398
// PR3b's own confirmation requirement (team-lead ruling): a stored result
// carrying RankCohort-minted ClaimedFacts (the ranking-time provenance
// citations, cohort driver SourceClaimedFactIDs' own field) must come back
// from a reuse hit with those SAME claims intact -- a reuse hit serves the
// stored payload verbatim, so a driver's SourceClaimedFactIDs must still
// resolve against the SAME claims the served copy carries, byte for byte,
// never a re-derived or dropped set.
func TestFindReusable_CohortMintedClaimedFactsSurviveAReuseHit(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-cohort-claims"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_cohort_claims01", principal.OrgID, "Which team is struggling most?")
	mintedValue := "high"
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{
		ClaimID: "claim_cohort_team:CHAOS_health.compounding_risk_current_cohort-ranking.v2",
		Kind:    contractsv1.ContextFabricFactHealth,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"},
		Field:   "severity", Value: contractsv1.ContextFabricScalarValue{String: &mintedValue},
	}}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	found, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate")
	require.Equal(t, result.ClaimedFacts, found.ClaimedFacts, "a reuse hit must serve the SAME cohort-minted claims the stored result carried -- a driver's SourceClaimedFactIDs must still resolve")
}

// TestFindReusable_RejectsAnExistingRowWhosePayloadCarriesPriorSubjectReceiptDispositions
// is a codex CHAOS-3813 round-1 finding (Medium, fixed): reuseKeyColumns'
// write-side guard (store.go) stops FUTURE saves from populating reuse
// columns for a disposition-bearing result, but it is a property of the
// SAVE call, not of the stored row -- it cannot retroactively protect a
// row saved before that guard existed, or one written by any path that
// skips it. This raw-updates a normally-saved (and therefore reusable) row
// so its PAYLOAD carries PriorSubjectReceiptDispositions while its reuse
// columns stay exactly as the original Save left them, simulating that
// bypass, and proves FindReusable still misses it.
func TestFindReusable_RejectsAnExistingRowWhosePayloadCarriesPriorSubjectReceiptDispositions(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-psrd"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_psrd01", principal.OrgID, "Is the auth migration on track, again?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	// Premise: the row is reusable before it is tainted.
	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "premise: the freshly saved row must be reusable before the raw payload update")

	tainted := result
	tainted.SubjectResolution.PriorSubjectReceiptDispositions = []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_x", ReceiptID: "receipt_x", Disposition: contractsv1.ContextFabricPriorSubjectReceiptApplied},
	}
	payload, err := json.Marshal(tainted)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_investigation_results SET payload = $1 WHERE result_id = $2`, payload, result.ResultID)
	require.NoError(t, err)

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "a row whose payload carries PriorSubjectReceiptDispositions must never be served as a reuse hit, even with its reuse columns intact")
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
	require.NoError(t, store.Save(ctx, principal, result, nil, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), testReuseRetrievalIdentity, testReusePromptVersions, testReuseVersionAuthorities, 0, ""))

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a Save with no threaded snapshot to never be reusable")
}

// TestF5_FindReusableClassifiesGraphEpochMismatchDistinctlyFromNoCandidate
// is the CHAOS-3898 v4.1 F5 regression: a row that matches every OTHER
// reuse dimension but was saved under a different graph_epoch must report
// ReuseMissStaleGraphEpoch, not the ordinary ReuseMissNoCandidate --
// proving the metadata-only classifier (matchesExceptGraphEpoch) actually
// distinguishes the two misses, which today's single payload-bearing
// SELECT could never do.
func TestF5_FindReusableClassifiesGraphEpochMismatchDistinctlyFromNoCandidate(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-graphepoch"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_graphepoch01", principal.OrgID, "Did the graph epoch change?")
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)
	// Saved under graph_epoch 3 -- the ResolvedGraphBinding.Epoch Engine
	// would have resolved for this investigation.
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch, contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext), testReuseRetrievalIdentity, testReusePromptVersions, testReuseVersionAuthorities, 3, ""))

	// A lookup at the SAME graph_epoch (3) hits.
	sameEpochKey := reuseKeyFor(result)
	sameEpochKey.GraphEpoch = 3
	found, ok, _, err := store.FindReusable(ctx, principal, sameEpochKey)
	require.NoError(t, err)
	require.True(t, ok, "expected a hit when the lookup's graph epoch matches the saved row's")
	require.Equal(t, result.ResultID, found.ResultID)

	// A lookup at a DIFFERENT graph_epoch (4) -- every other dimension
	// identical -- misses, and the miss classifies as stale_graph_epoch
	// specifically (a build/flip moved the active epoch since this row
	// was saved), not the ordinary miss_no_candidate.
	differentEpochKey := reuseKeyFor(result)
	differentEpochKey.GraphEpoch = 4
	_, ok, reason, err := store.FindReusable(ctx, principal, differentEpochKey)
	require.NoError(t, err)
	require.False(t, ok, "expected a miss when the lookup's graph epoch differs from the saved row's")
	require.Equal(t, contextfabric.ReuseMissStaleGraphEpoch, reason)

	// A genuinely absent candidate (a question that was never saved at
	// all) must still classify as the ordinary miss_no_candidate -- proving
	// the classifier distinguishes the two misses rather than always
	// reporting stale_graph_epoch once ANY row exists for the organization.
	neverSavedKey := reuseKeyFor(result)
	neverSavedKey.QuestionHash = contextfabric.QuestionHash("a question nobody ever asked")
	neverSavedKey.GraphEpoch = 3
	_, ok, reason, err = store.FindReusable(ctx, principal, neverSavedKey)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, contextfabric.ReuseMissNoCandidate, reason)
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

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate before the watermark advances")

	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-2")

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
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

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "sanity: unchanged empty-string watermark must still match")

	// Replace "linear" with "github" -- same count (1), different source
	// name, and the new source also happens to have an empty watermark.
	deleteCheckpoint(t, ctx, db, principal.OrgID, "linear")
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "github", "")

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
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

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected a reusable candidate before the rebuild invalidation")

	require.NoError(t, store.InvalidateOrganizationReuse(ctx, principal.OrgID))

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
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

	_, ok, _, err := store.FindReusable(ctx, rebuiltOrg, reuseKeyFor(rebuiltResult))
	require.NoError(t, err)
	require.False(t, ok, "expected the rebuilt organization's result to be invalidated")

	_, ok, _, err = store.FindReusable(ctx, otherOrg, reuseKeyFor(otherResult))
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
		_, ok, _, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("projection_version", func(t *testing.T) {
		key := baseline
		key.ProjectionVersion = "projection-v2"
		_, ok, _, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("model_identity", func(t *testing.T) {
		key := baseline
		key.ModelIdentities = []string{"openai-compatible/gpt-5-mini"}
		_, ok, _, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok)
	})
	// CHAOS-4085: the commit-gate fence. This is the dimension whose
	// absence would be a SAFETY hole rather than a staleness one -- the
	// reuse lookup runs before Interpret and before synthesis, so a row
	// saved under an older gate would otherwise be served with its stored
	// Committed list having never passed through the current gate.
	t.Run("commit_gate_version", func(t *testing.T) {
		key := baseline
		key.CommitGateVersion = "cg_v99"
		_, ok, _, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok, "a row produced under a different commit gate must never be replayed under this one")
	})
	// CHAOS-4085: the fail-closed half. An unwired dimension must MISS
	// rather than run a lookup that silently ignores the fence.
	t.Run("commit_gate_version_unset_misses", func(t *testing.T) {
		key := baseline
		key.CommitGateVersion = ""
		_, ok, _, err := store.FindReusable(ctx, principal, key)
		require.NoError(t, err)
		require.False(t, ok, "an unconfigured commit-gate dimension must fail closed")
	})
	t.Run("unchanged_key_still_matches", func(t *testing.T) {
		_, ok, _, err := store.FindReusable(ctx, principal, baseline)
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

	found, ok, _, err := store.FindReusable(ctx, principal, key)
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

	_, ok, _, err := store.FindReusable(ctx, principal, key)
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
	_, ok, _, err := store.FindReusable(ctx, principal, key)
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
	_, ok, _, err = store.FindReusable(ctx, principal, key)
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

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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

		_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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

		_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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

	_, ok, _, err := store.FindReusable(ctx, other, reuseKeyFor(result))
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
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch, contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext), testReuseRetrievalIdentity, testReusePromptVersions, testReuseVersionAuthorities, 0, ""))

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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
	require.NoError(t, store.Save(ctx, principal, result, snapshot, nil, contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext), testReuseRetrievalIdentity, testReusePromptVersions, testReuseVersionAuthorities, 0, ""))

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
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
	require.NoError(t, store.Save(ctx, principal, result, snapshot, &epoch, contextfabric.TimeAxisKeyFor(result.Interpretation.TimeContext), testReuseRetrievalIdentity, testReusePromptVersions, testReuseVersionAuthorities, 0, ""))

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a Save with an empty model identity to never be reusable")
}

// TestFindReusable_EmbedRetrievalIdentityIsConjunctive is the CHAOS-3833
// P1-2 closure at the store level: the embed retrieval identity is a
// dedicated EQUALITY dimension, so a stored answer stops matching the
// moment the deployment's embed-text semantics move (a new composition
// tag) -- during the deploy->rebuild window included, which is exactly
// the window the epoch fence cannot cover (the epoch only bumps when the
// operator eventually rebuilds).
func TestFindReusable_EmbedRetrievalIdentityIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-embed-identity"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_embed_id01", principal.OrgID, "Does an embed semantic change invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	// Same everything, different embed retrieval identity -- what a
	// post-deploy binary computes after a composition-tag flip.
	changed := reuseKeyFor(result)
	changed.EmbedRetrievalIdentity = "openai/text-embedding-3-large#t2:r2000:b1:pnone"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed embed retrieval identity to miss conjunctively")

	// And the unchanged identity still hits -- the dimension
	// discriminates, it does not blanket-disable.
	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical embed retrieval identity to still match")
}

// TestFindReusable_RetrievalPolicyVersionIsConjunctive: the policy-version
// twin of the test above (spec §4 R3). A tau/K/HNSW default change bumps
// the constant, moves the lookup value, and every answer stored under the
// old policy stops matching -- with no node stamp moving and no rebuild.
func TestFindReusable_RetrievalPolicyVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-policy-version"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_policy01", principal.OrgID, "Does a retrieval policy change invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	changed := reuseKeyFor(result)
	changed.RetrievalPolicyVersion = "rp2"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed retrieval policy version to miss conjunctively")
}

// TestFindReusable_PreMigrationNullRetrievalColumnsNeverMatch pins the
// per-replica fail-closed property migration 0014's header documents:
// every pre-migration row holds NULL in both new columns, and NULL never
// satisfies an equality predicate, so a predicate-carrying binary can
// never reuse a pre-change answer. Simulated by NULLing the columns on a
// row saved through the current binary -- byte-for-byte what a pre-0014
// row looks like to this query.
func TestFindReusable_PreMigrationNullRetrievalColumnsNeverMatch(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-pre-migration"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_premigration01", principal.OrgID, "Is a pre-migration row unreusable?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, err := db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_results
   SET embed_retrieval_identity = NULL, retrieval_policy_version = NULL
 WHERE result_id = $1`, result.ResultID)
	require.NoError(t, err)

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a pre-0014-shaped row (NULL retrieval columns) to never match the conjunctive predicates")
}

// TestFindReusable_EmptyRetrievalKeyFieldsMissWithoutQuerying: a
// composition that never supplied the discriminators must produce an
// ordinary miss, never a lookup that silently ignores the dimensions --
// the same fail-closed convention as an empty question hash.
func TestFindReusable_EmptyRetrievalKeyFieldsMissWithoutQuerying(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-empty-retrieval-key"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_emptykey01", principal.OrgID, "Does an empty retrieval key fail closed?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	missing := reuseKeyFor(result)
	missing.EmbedRetrievalIdentity = ""
	_, ok, _, err := store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty embed retrieval identity in the key to miss")

	missing = reuseKeyFor(result)
	missing.RetrievalPolicyVersion = ""
	_, ok, _, err = store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty retrieval policy version in the key to miss")
}

// TestFindReusable_InterpretationPromptVersionIsConjunctive is CHAOS-3862's
// store-level closure, the interpretation twin of
// TestFindReusable_EmbedRetrievalIdentityIsConjunctive above: the
// interpretation prompt version is a dedicated EQUALITY dimension, so a
// stored answer stops matching the moment the deployment's interpretation
// prompt bumps (e.g. v6->v7) -- for the rest of the row's staleness
// window, not merely until the next rebuild. This is the RED-FIRST proof
// for the ticket's own reproduction case: "a v6-stamped stored answer must
// NOT serve a v7 lookup."
func TestFindReusable_InterpretationPromptVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-interpretation-prompt"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_interp_prompt01", principal.OrgID, "Does an interpretation prompt bump invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	// Same everything, different interpretation prompt version -- what a
	// post-deploy binary (v6->v7) computes.
	changed := reuseKeyFor(result)
	changed.InterpretationPromptVersion = "context-fabric-interpretation.v6"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed interpretation prompt version to miss conjunctively")

	// And the unchanged version still hits -- the dimension discriminates,
	// it does not blanket-disable.
	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical interpretation prompt version to still match")
}

// TestFindReusable_SynthesisPromptVersionIsConjunctive: the synthesis twin
// of the test above. Answer reuse skips Synthesize on a hit too (Engine.
// tryReuse runs before Interpret AND serves without ever calling
// Synthesize), so a synthesis prompt bump is exactly the same hazard from
// the other end of the same mechanism.
func TestFindReusable_SynthesisPromptVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-synthesis-prompt"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_synth_prompt01", principal.OrgID, "Does a synthesis prompt bump invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	changed := reuseKeyFor(result)
	changed.SynthesisPromptVersion = "context-fabric-synthesis.v8"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed synthesis prompt version to miss conjunctively")

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical synthesis prompt version to still match")
}

// TestFindReusable_PreMigrationNullPromptVersionColumnsNeverMatch pins the
// per-replica fail-closed property migration 0015's header documents:
// every pre-migration row holds NULL in both new columns, and NULL never
// satisfies an equality predicate, so a predicate-carrying binary can
// never reuse a pre-change answer. Simulated by NULLing the columns on a
// row saved through the current binary -- byte-for-byte what a pre-0015
// row looks like to this query.
func TestFindReusable_PreMigrationNullPromptVersionColumnsNeverMatch(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-prompt-pre-migration"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_prompt_premigration01", principal.OrgID, "Is a pre-migration prompt-version row unreusable?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, err := db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_results
   SET interpretation_prompt_version = NULL, synthesis_prompt_version = NULL
 WHERE result_id = $1`, result.ResultID)
	require.NoError(t, err)

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a pre-0015-shaped row (NULL prompt-version columns) to never match the conjunctive predicates")
}

// TestFindReusable_EmptyPromptVersionKeyFieldsMissWithoutQuerying: a
// composition that never supplied the prompt-version discriminators must
// produce an ordinary miss, never a lookup that silently ignores the
// dimensions -- the same fail-closed convention as an empty question hash
// or an empty retrieval key field.
func TestFindReusable_EmptyPromptVersionKeyFieldsMissWithoutQuerying(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-empty-prompt-key"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_prompt_emptykey01", principal.OrgID, "Does an empty prompt-version key fail closed?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	missing := reuseKeyFor(result)
	missing.InterpretationPromptVersion = ""
	_, ok, _, err := store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty interpretation prompt version in the key to miss")

	missing = reuseKeyFor(result)
	missing.SynthesisPromptVersion = ""
	_, ok, _, err = store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty synthesis prompt version in the key to miss")
}

// TestFindReusable_QueryVersionIsConjunctive is CHAOS-3862 round 2's
// store-level closure (sol review class-close): the ClickHouse query-shape
// version is a dedicated EQUALITY dimension, so a stored answer stops
// matching the moment devhealthfacts.QueryVersion moves.
func TestFindReusable_QueryVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-query-version"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_query_ver01", principal.OrgID, "Does a query-shape change invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	changed := reuseKeyFor(result)
	changed.QueryVersion = "devhealthfacts.clickhouse.v2"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed query version to miss conjunctively")

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical query version to still match")
}

// TestFindReusable_CanonicalServiceVersionIsConjunctive: the
// canonical-fact-registry twin of the test above.
func TestFindReusable_CanonicalServiceVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-canonical-svc-version"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_canon_svc01", principal.OrgID, "Does a canonical-service-version change invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	changed := reuseKeyFor(result)
	changed.CanonicalServiceVersion = "context-fabric-facts.v2"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed canonical service version to miss conjunctively")

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical canonical service version to still match")
}

// TestFindReusable_ModelOutputSchemaVersionIsConjunctive: the genkit
// model-output-schema twin. Unlike the prompt-version pair, this ONE
// dimension governs BOTH interpretation and synthesis output shape (a
// single shared genkitruntime.Config.SchemaVersion) -- so a bump here
// invalidates reuse regardless of which operation's schema actually
// changed.
func TestFindReusable_ModelOutputSchemaVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-model-output-schema"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_schema_ver01", principal.OrgID, "Does a model-output schema bump invalidate reuse?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	changed := reuseKeyFor(result)
	changed.ModelOutputSchemaVersion = "context-fabric-model-output.v2"
	_, ok, _, err := store.FindReusable(ctx, principal, changed)
	require.NoError(t, err)
	require.False(t, ok, "expected a changed model-output schema version to miss conjunctively")

	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical model-output schema version to still match")
}

// TestFindReusable_PreMigrationNullVersionAuthorityColumnsNeverMatch pins
// the round-2 twin of TestFindReusable_PreMigrationNullPromptVersionColumnsNeverMatch:
// every pre-migration row holds NULL in all three new columns, and NULL
// never satisfies an equality predicate.
func TestFindReusable_PreMigrationNullVersionAuthorityColumnsNeverMatch(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-version-authority-pre-migration"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_va_premigration01", principal.OrgID, "Is a pre-migration version-authority row unreusable?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, err := db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_results
   SET query_version = NULL, canonical_service_version = NULL, model_output_schema_version = NULL
 WHERE result_id = $1`, result.ResultID)
	require.NoError(t, err)

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a pre-0015-shaped row (NULL version-authority columns) to never match the conjunctive predicates")
}

// TestFindReusable_EmptyVersionAuthorityKeyFieldsMissWithoutQuerying: the
// round-2 twin of TestFindReusable_EmptyPromptVersionKeyFieldsMissWithoutQuerying --
// a composition that never supplied one of these three discriminators
// must produce an ordinary miss, never a lookup that silently ignores it.
func TestFindReusable_EmptyVersionAuthorityKeyFieldsMissWithoutQuerying(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-empty-version-authority-key"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_va_emptykey01", principal.OrgID, "Does an empty version-authority key fail closed?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	missing := reuseKeyFor(result)
	missing.QueryVersion = ""
	_, ok, _, err := store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty query version in the key to miss")

	missing = reuseKeyFor(result)
	missing.CanonicalServiceVersion = ""
	_, ok, _, err = store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty canonical service version in the key to miss")

	missing = reuseKeyFor(result)
	missing.ModelOutputSchemaVersion = ""
	_, ok, _, err = store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty model-output schema version in the key to miss")
}

// TestFindReusable_RankingFormulaVersionIsConjunctive is the CHAOS-4398 PR3
// R4-ruling regression: RankCohort runs AFTER FindReusable (engine.go), so
// a stored cohort answer computed under an OLD ranking formula version must
// NOT be served as a reuse hit once the deployment's formula version has
// moved on -- exactly the bug this dimension exists to close (a stale
// cohort answer's Score/Outcome/RankingBasis silently reused under the NEW
// formula's meaning). Mirrors QueryVersionIsConjunctive's own shape: same
// key, only this ONE dimension diverges.
func TestFindReusable_RankingFormulaVersionIsConjunctive(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-ranking-formula-version"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	// Saved under the deployment-current formula version (simulating a
	// cohort answer computed by THIS binary's RankCohort).
	result := reusableResult("result_reuse_ranking_formula01", principal.OrgID, "Which teams in this cohort are struggling most?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	// A later binary deploys a bumped formula version (a new weight, a new
	// threshold, a new signal -- design doc §8's own v1 -> v2 change). The
	// stored row -- computed under the OLD formula -- must miss.
	bumped := reuseKeyFor(result)
	bumped.RankingFormulaVersion = "cohort-ranking.v3"
	_, ok, _, err := store.FindReusable(ctx, principal, bumped)
	require.NoError(t, err)
	require.False(t, ok, "expected a stale cohort answer computed under an old ranking formula version to miss after the formula version changed, not be silently reused under the new formula's semantics")

	// The identical formula version still matches -- this dimension does
	// not defeat reuse for an unrelated, unchanged deploy.
	_, ok, _, err = store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.True(t, ok, "expected the identical ranking formula version to still match")
}

// TestFindReusable_PreMigrationNullRankingFormulaVersionColumnNeverMatches
// is the CHAOS-4398 PR3 twin of
// TestFindReusable_PreMigrationNullVersionAuthorityColumnsNeverMatch: a
// pre-migration-0035 row holds NULL in ranking_formula_version, and NULL
// never satisfies an equality predicate -- so a genuinely pre-PR3 stored
// row (this binary never populated the column at all) is permanently
// excluded from reuse on this dimension, no backfill required.
func TestFindReusable_PreMigrationNullRankingFormulaVersionColumnNeverMatches(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-ranking-formula-pre-migration"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_rf_premigration01", principal.OrgID, "Is a pre-migration ranking-formula row unreusable?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	_, err := db.ExecContext(ctx, `
UPDATE acr.context_fabric_investigation_results
   SET ranking_formula_version = NULL
 WHERE result_id = $1`, result.ResultID)
	require.NoError(t, err)

	_, ok, _, err := store.FindReusable(ctx, principal, reuseKeyFor(result))
	require.NoError(t, err)
	require.False(t, ok, "expected a pre-0035-shaped row (NULL ranking_formula_version) to never match the conjunctive predicate")
}

// TestFindReusable_EmptyRankingFormulaVersionKeyFieldMissesWithoutQuerying
// is the CHAOS-4398 PR3 twin of
// TestFindReusable_EmptyVersionAuthorityKeyFieldsMissWithoutQuerying: a
// composition that never wired RankingFormulaVersion must produce an
// ordinary miss, never a lookup that silently ignores the dimension.
func TestFindReusable_EmptyRankingFormulaVersionKeyFieldMissesWithoutQuerying(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-reuse-empty-ranking-formula-key"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")

	store := mustReuseStore(t, db, time.Hour)
	result := reusableResult("result_reuse_rf_emptykey01", principal.OrgID, "Does an empty ranking-formula key fail closed?")
	saveWithReuseSnapshot(t, ctx, store, principal, result)

	missing := reuseKeyFor(result)
	missing.RankingFormulaVersion = ""
	_, ok, _, err := store.FindReusable(ctx, principal, missing)
	require.NoError(t, err)
	require.False(t, ok, "expected an empty ranking formula version in the key to miss")
}
