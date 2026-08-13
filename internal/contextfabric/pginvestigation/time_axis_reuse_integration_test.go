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
