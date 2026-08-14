package pginvestigation_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3781: answer reuse must not collapse two different as-of
// questions onto one key.
//
// Before the TimeAxisKey dimension, ReuseKey.QuestionHash hashed the
// question TEXT only. That was sound while non-current axes were refused,
// because every stored result was implicitly a current-axis answer. The
// moment historical answers became storable, "was Ask Dev ready?" asked
// as of March and as of June produced the SAME key -- and the June answer
// would be served for the March question. A silent wrong answer, strictly
// worse than the refusal CHAOS-3781 removed.

func historicalResult(t *testing.T, resultID, orgID string, timeContext contextfabric.TimeContext) contextfabric.InvestigationResult {
	t.Helper()
	// One shared question TEXT across every fixture -- the collision this
	// dimension prevents only exists when the text is identical.
	result := reusableResult(resultID, orgID, "Was Ask Dev ready to ship?")
	result.Interpretation.TimeContext = timeContext
	if timeContext.Axis != contextfabric.TemporalCurrent {
		result.Temporal = &contextfabric.TemporalLabel{
			Requested: timeContext, Effective: timeContext,
			Grain: contextfabric.GrainDay, CoverageComplete: true,
		}
	}
	require.NoError(t, result.Validate(), "fixture must be a contract-valid result")
	return result
}

// TestReuseKeyDistinguishesTwoAsOfTimes is the core regression: the same
// question text at two different as-of times must not cross-serve.
func TestReuseKeyDistinguishesTwoAsOfTimes(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-reuse-asof"
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: orgID}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")
	store := mustReuseStore(t, db, time.Hour)

	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	marchContext := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &march}
	juneContext := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &june}

	// Only the JUNE answer is stored. Its question text is identical to
	// the March one -- that is the whole point.
	juneResult := historicalResult(t, "result_june_00001", principal.OrgID, juneContext)
	saveWithReuseSnapshot(t, ctx, store, principal, juneResult)

	// Asking the same question as of March must NOT find it.
	marchKey := reuseKeyFor(historicalResult(t, "result_march_0001", principal.OrgID, marchContext))
	_, found, err := store.FindReusable(ctx, principal, marchKey)
	require.NoError(t, err)
	require.False(t, found, "a June answer was served for a March question; the two as-of times must not share a reuse key")

	// And the over-blocking guard: asking as of June DOES find it, so the
	// test above is not passing merely because nothing is ever reusable.
	juneKey := reuseKeyFor(juneResult)
	reused, found, err := store.FindReusable(ctx, principal, juneKey)
	require.NoError(t, err)
	require.True(t, found, "the June answer must still be reusable for the June question")
	require.Equal(t, juneResult.ResultID, reused.ResultID)
}

// TestReuseKeyDistinguishesAxesAtTheSameInstant covers the subtler
// collision: valid_time and observed_time at the SAME instant are
// different questions -- one asks what was true then, the other what was
// known then -- and they answer from different sources.
func TestReuseKeyDistinguishesAxesAtTheSameInstant(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-reuse-axis"
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: orgID}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")
	store := mustReuseStore(t, db, time.Hour)

	instant := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	validTime := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &instant}
	observedTime := contextfabric.TimeContext{Axis: contextfabric.TemporalObservedTime, AsOf: &instant}

	stored := historicalResult(t, "result_valid_0001", principal.OrgID, validTime)
	saveWithReuseSnapshot(t, ctx, store, principal, stored)

	observedKey := reuseKeyFor(historicalResult(t, "result_obs_00001", principal.OrgID, observedTime))
	_, found, err := store.FindReusable(ctx, principal, observedKey)
	require.NoError(t, err)
	require.False(t, found, "a valid-time answer was served for an observed-time question at the same instant")
}

// TestReuseKeyDistinguishesHistoricalFromCurrent is the migration's own
// correctness claim, exercised: a current-axis answer must never be
// served for a historical question, and vice versa.
func TestReuseKeyDistinguishesHistoricalFromCurrent(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-reuse-current"
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: orgID}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")
	store := mustReuseStore(t, db, time.Hour)

	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	currentResult := historicalResult(t, "result_current_001", principal.OrgID, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	saveWithReuseSnapshot(t, ctx, store, principal, currentResult)

	historicalKey := reuseKeyFor(historicalResult(t, "result_hist_0001", principal.OrgID,
		contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}))
	_, found, err := store.FindReusable(ctx, principal, historicalKey)
	require.NoError(t, err)
	require.False(t, found, "a current-state answer was served for a historical question")

	// The current question still reuses its own answer -- the backfilled
	// 'current' default in migration 0013 is what makes this hold for
	// every row written before that migration.
	currentKey := reuseKeyFor(currentResult)
	reused, found, err := store.FindReusable(ctx, principal, currentKey)
	require.NoError(t, err)
	require.True(t, found, "a current-axis answer must stay reusable for a current-axis question")
	require.Equal(t, currentResult.ResultID, reused.ResultID)
}

// TestTimeAxisKeyForIsStableAndCollisionFree pins the canonicalization
// itself, including the trap that a current-axis key must be a FIXED
// literal: deriving it from the wall clock would make every current key
// unique and silently drop the reuse hit rate to zero while every
// CHAOS-3782 test kept passing.
func TestTimeAxisKeyForIsStableAndCollisionFree(t *testing.T) {
	t.Parallel()
	instant := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	other := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	first := contextfabric.TimeAxisKeyFor(current)
	require.NotEmpty(t, first)
	require.Equal(t, first, contextfabric.TimeAxisKeyFor(current),
		"a current-axis key must be stable across calls; a wall-clock-derived key would silently disable reuse entirely")

	// The same instant expressed in a different zone is the same instant,
	// so it must key identically -- epoch nanoseconds, not formatting.
	inZone := instant.In(time.FixedZone("elsewhere", 5*3600))
	require.Equal(t,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &instant}),
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &inZone}),
		"the same instant in a different zone must produce the same key")

	distinct := map[string]string{
		"current":       contextfabric.TimeAxisKeyFor(current),
		"valid march":   contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &instant}),
		"valid june":    contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &other}),
		"observed":      contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalObservedTime, AsOf: &instant}),
		"range":         contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &instant, End: &other}),
		"range flipped": contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &other, End: &instant}),
	}
	seen := map[string]string{}
	for name, key := range distinct {
		require.NotEmpty(t, key, "%s produced an empty key", name)
		if previous, clash := seen[key]; clash {
			t.Fatalf("%q and %q share the reuse key %q", name, previous, key)
		}
		seen[key] = name
	}

	// A historical context missing its own required bounds fails closed.
	for name, malformed := range map[string]contextfabric.TimeContext{
		"valid_time without as_of": {Axis: contextfabric.TemporalValidTime},
		"range without bounds":     {Axis: contextfabric.TemporalRange},
		"empty axis":               {},
	} {
		require.Empty(t, contextfabric.TimeAxisKeyFor(malformed),
			"%s must produce no key, so it can never become reusable", name)
	}
}

// TestF6_AnInterpreterAxisFlipStillReusesForAnIdenticalRequest is round-1
// F6, red-green -- with its rationale corrected for round-3 F1.
//
// The surviving invariant is SYMMETRY: both reuse sides must derive the
// key from a value both sides can compute. Save used to derive it from the
// INTERPRETED result, which the lookup cannot see (tryReuse runs before
// Interpret). So when an interpreter read a current-axis request as
// historical -- exactly what it should do for "what was the status last
// month" -- the row saved under a key no identical future request could
// ever produce, and that whole class of question reused nothing, silently.
//
// Round 1 fixed this by keying both sides on the WIRE request; round-3 F1
// then moved both to the CLAMPED EFFECTIVE context, because the wire value
// stops describing what an answer means once clamping has moved it. The
// symmetry is what survived both rulings, and it is what this test guards.
//
// This test uses the CURRENT axis, where wire and effective coincide --
// TimeAxisKeyFor maps it to a fixed literal and no clamping applies -- so
// it is deliberately independent of that change. Its subject is the
// interpretation flip, not the clamp; the clamp cases live in
// contextfabric's own TestF1_* guards.
//
// Interpretation identity is not lost by keying on the request: condition
// 6 re-resolves every subject against the stored Interpretation before
// anything is served.
func TestF6_AnInterpreterAxisFlipStillReusesForAnIdenticalRequest(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-reuse-flip"
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: orgID}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")
	store := mustReuseStore(t, db, time.Hour)

	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// The caller sent axis=current; the interpreter concluded the
	// question was historical, so the stored result's Interpretation
	// carries valid_time.
	interpretedHistorical := historicalResult(t, "result_flip_00001", principal.OrgID,
		contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf})

	// Engine keys Save and the lookup identically. On the current
	// axis that key is the fixed literal, unaffected by clamping.
	currentAxisKey := contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, principal, interpretedHistorical, snapshot, &epoch, currentAxisKey))

	// A byte-identical follow-up request -- same text, same current axis --
	// must find it. Before F6 this was a permanent miss.
	lookup := contextfabric.ReuseKey{
		QuestionHash:      contextfabric.QuestionHash(interpretedHistorical.Question),
		ContractVersion:   interpretedHistorical.Versions.ContractVersion,
		ProjectionVersion: interpretedHistorical.Versions.ProjectionVersion,
		// A single-member chain (CHAOS-3786): the exact identity this
		// result was stored under.
		ModelIdentities: []string{interpretedHistorical.Versions.ModelIdentity},
		TimeAxisKey:     currentAxisKey,
	}
	reused, found, err := store.FindReusable(ctx, principal, lookup)
	require.NoError(t, err)
	require.True(t, found, "an interpreted-historical answer was unreachable to the identical request that produced it; both reuse sides must derive the key the same way")
	require.Equal(t, interpretedHistorical.ResultID, reused.ResultID)
	// And what was stored still records the historical interpretation, so
	// condition 6 re-resolves against the right question.
	require.Equal(t, contextfabric.TemporalValidTime, reused.Interpretation.TimeContext.Axis)
}
