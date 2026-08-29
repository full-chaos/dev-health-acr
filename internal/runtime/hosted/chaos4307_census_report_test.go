package hosted_test

// CHAOS-4307: carry the CHAOS-4155 shadow kind-scoped vector census's own
// aggregate outcome counts in the trial report schema itself (v30), so a
// future measurement run reads the merged JSON artifact like every other
// trial metric -- no grep across N shard .gotest.log files at DebugContext
// level, no dependency on the two-turn harness remembering to tee its
// in-memory tracer to slog (see chaos4155_confirmed_kind_vector_scope.go's
// own doc comment and cf-rulings.md 2026-08-25 22:27 for the reachability
// gap this closes for good).
//
// foldConfirmedKindVectorCensus is the SINGLE aggregation point: it reads
// every "confirmed_kind_scope" stage event out of one already-captured
// []graphrank.ResolutionTraceEvent slice (a resolve call's own trace, still
// in memory -- see twoTurnTraceCapture.snapshot()/`.events`) and folds each
// one's ConfirmedKindVectorScope* fields into the report's run-level rollup.
// Called exactly ONCE per real resolve() call this harness makes (turn 1,
// each arm's own turn-2 call, the inferred-tier arm's separate baseline
// pass, each mutation-probe result) -- NEVER via twoTurnStampTurn1Facts,
// which stamps the SAME turn-1 facts onto every arm's own row and would
// silently multiply-count turn 1's contribution once per arm otherwise.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestFoldConfirmedKindVectorCensus_AggregatesByState(t *testing.T) {
	t.Parallel()
	report := twoTurnReport{}
	events := []graphrank.ResolutionTraceEvent{
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: "complete",
			ConfirmedKindVectorScopePopulationCount: 20, ConfirmedKindVectorScopeComparisonCount: 44,
			ConfirmedKindVectorScopeQueryCount: 2, ConfirmedKindVectorScopeRivalCountAboveTau: 5,
			ConfirmedKindVectorScopeDurationMS: 100},
		// A different stage on the same call must never be read.
		{Stage: "kind_offer", ConfirmedKindVectorScopeState: "complete"},
		// A confirmed_kind_scope event whose vector census never even
		// attempted (plain lexical completeness, no live vector mechanism)
		// carries an empty state -- ConfirmedKindScopeComplete's own case in
		// buildConfirmedKindScopedSnapshot never calls
		// attemptConfirmedKindVectorCensus at all, so this is the ordinary,
		// frequent, NOT-a-bug shape and must be silently skipped, never
		// counted as a state.
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: ""},
	}

	foldConfirmedKindVectorCensus(&report, events)

	if got := report.ConfirmedKindVectorCensusStateCount["complete"]; got != 1 {
		t.Errorf(`ConfirmedKindVectorCensusStateCount["complete"] = %d, want 1`, got)
	}
	if len(report.ConfirmedKindVectorCensusStateCount) != 1 {
		t.Errorf("ConfirmedKindVectorCensusStateCount = %+v, want exactly one key (the kind_offer event and the empty-state event must not appear)", report.ConfirmedKindVectorCensusStateCount)
	}
	if report.ConfirmedKindVectorCensusPopulationSum != 20 {
		t.Errorf("ConfirmedKindVectorCensusPopulationSum = %d, want 20", report.ConfirmedKindVectorCensusPopulationSum)
	}
	if report.ConfirmedKindVectorCensusComparisonSum != 44 {
		t.Errorf("ConfirmedKindVectorCensusComparisonSum = %d, want 44", report.ConfirmedKindVectorCensusComparisonSum)
	}
	if report.ConfirmedKindVectorCensusQueryCountSum != 2 {
		t.Errorf("ConfirmedKindVectorCensusQueryCountSum = %d, want 2", report.ConfirmedKindVectorCensusQueryCountSum)
	}
	if report.ConfirmedKindVectorCensusRivalCountAboveTauSum != 5 {
		t.Errorf("ConfirmedKindVectorCensusRivalCountAboveTauSum = %d, want 5", report.ConfirmedKindVectorCensusRivalCountAboveTauSum)
	}
	if report.ConfirmedKindVectorCensusDurationMSSum != 100 {
		t.Errorf("ConfirmedKindVectorCensusDurationMSSum = %d, want 100", report.ConfirmedKindVectorCensusDurationMSSum)
	}
}

// TestFoldConfirmedKindVectorCensus_SumsAcrossMultipleEvents pins the
// "aggregate, not last-wins" contract: buildConfirmedKindScopedSnapshot can
// run more than once inside a single captured call (e.g. a stalled
// resolution's evidence-census re-resolve), and each attempt is a
// genuinely independent census outcome -- unlike kindCoverageFloorEvent's
// own deliberate last-wins reduction, every occurrence here must count.
//
// All FIVE scalar fields vary independently and asymmetrically across the
// three events (codex round 2, Low, confirmed: an earlier version of this
// test varied only PopulationCount, so a regression that ASSIGNED instead
// of ADDED for comparison/query/rival/duration could still pass).
func TestFoldConfirmedKindVectorCensus_SumsAcrossMultipleEvents(t *testing.T) {
	t.Parallel()
	report := twoTurnReport{}
	events := []graphrank.ResolutionTraceEvent{
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: "complete",
			ConfirmedKindVectorScopePopulationCount: 10, ConfirmedKindVectorScopeComparisonCount: 1,
			ConfirmedKindVectorScopeQueryCount: 100, ConfirmedKindVectorScopeRivalCountAboveTau: 1000,
			ConfirmedKindVectorScopeDurationMS: 10000},
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: "complete",
			ConfirmedKindVectorScopePopulationCount: 8, ConfirmedKindVectorScopeComparisonCount: 2,
			ConfirmedKindVectorScopeQueryCount: 200, ConfirmedKindVectorScopeRivalCountAboveTau: 2000,
			ConfirmedKindVectorScopeDurationMS: 20000},
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: "over_budget",
			ConfirmedKindVectorScopePopulationCount: 500, ConfirmedKindVectorScopeComparisonCount: 3,
			ConfirmedKindVectorScopeQueryCount: 300, ConfirmedKindVectorScopeRivalCountAboveTau: 3000,
			ConfirmedKindVectorScopeDurationMS: 30000},
	}

	foldConfirmedKindVectorCensus(&report, events)

	if got := report.ConfirmedKindVectorCensusStateCount["complete"]; got != 2 {
		t.Errorf(`ConfirmedKindVectorCensusStateCount["complete"] = %d, want 2`, got)
	}
	if got := report.ConfirmedKindVectorCensusStateCount["over_budget"]; got != 1 {
		t.Errorf(`ConfirmedKindVectorCensusStateCount["over_budget"] = %d, want 1`, got)
	}
	if report.ConfirmedKindVectorCensusPopulationSum != 518 {
		t.Errorf("ConfirmedKindVectorCensusPopulationSum = %d, want 518 (10+8+500)", report.ConfirmedKindVectorCensusPopulationSum)
	}
	if report.ConfirmedKindVectorCensusComparisonSum != 6 {
		t.Errorf("ConfirmedKindVectorCensusComparisonSum = %d, want 6 (1+2+3)", report.ConfirmedKindVectorCensusComparisonSum)
	}
	if report.ConfirmedKindVectorCensusQueryCountSum != 600 {
		t.Errorf("ConfirmedKindVectorCensusQueryCountSum = %d, want 600 (100+200+300)", report.ConfirmedKindVectorCensusQueryCountSum)
	}
	if report.ConfirmedKindVectorCensusRivalCountAboveTauSum != 6000 {
		t.Errorf("ConfirmedKindVectorCensusRivalCountAboveTauSum = %d, want 6000 (1000+2000+3000)", report.ConfirmedKindVectorCensusRivalCountAboveTauSum)
	}
	if report.ConfirmedKindVectorCensusDurationMSSum != 60000 {
		t.Errorf("ConfirmedKindVectorCensusDurationMSSum = %d, want 60000 (10000+20000+30000)", report.ConfirmedKindVectorCensusDurationMSSum)
	}
}

// TestFoldConfirmedKindVectorCensus_NilEventsIsANoOp guards the call sites
// that pass a possibly-nil slice (a redacted/never-populated
// BaselineTraceEvents, or a nil traceCapture guard upstream) -- must never
// panic, must never allocate a non-nil-but-empty map when there is nothing
// to fold (matches every other map-valued report field's own nil-is-empty
// convention, e.g. OfferMissCount before any offer miss).
func TestFoldConfirmedKindVectorCensus_NilEventsIsANoOp(t *testing.T) {
	t.Parallel()
	report := twoTurnReport{}
	foldConfirmedKindVectorCensus(&report, nil)
	if report.ConfirmedKindVectorCensusStateCount != nil {
		t.Errorf("ConfirmedKindVectorCensusStateCount = %+v, want nil after folding nil events", report.ConfirmedKindVectorCensusStateCount)
	}
	if report.ConfirmedKindVectorCensusPopulationSum != 0 {
		t.Errorf("ConfirmedKindVectorCensusPopulationSum = %d, want 0", report.ConfirmedKindVectorCensusPopulationSum)
	}
}

// TestFoldConfirmedKindVectorCensus_AccumulatesAcrossCalls pins that folding
// is a += operation, not an assignment -- the real call sites invoke this
// once per arm/turn across an entire corpus run into the SAME report.
func TestFoldConfirmedKindVectorCensus_AccumulatesAcrossCalls(t *testing.T) {
	t.Parallel()
	report := twoTurnReport{}
	foldConfirmedKindVectorCensus(&report, []graphrank.ResolutionTraceEvent{
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: "complete", ConfirmedKindVectorScopePopulationCount: 3},
	})
	foldConfirmedKindVectorCensus(&report, []graphrank.ResolutionTraceEvent{
		{Stage: "confirmed_kind_scope", ConfirmedKindVectorScopeState: "complete", ConfirmedKindVectorScopePopulationCount: 4},
	})
	if got := report.ConfirmedKindVectorCensusStateCount["complete"]; got != 2 {
		t.Errorf(`ConfirmedKindVectorCensusStateCount["complete"] = %d, want 2 across two folds`, got)
	}
	if report.ConfirmedKindVectorCensusPopulationSum != 7 {
		t.Errorf("ConfirmedKindVectorCensusPopulationSum = %d, want 7 across two folds", report.ConfirmedKindVectorCensusPopulationSum)
	}
}

// TestTwoTurnReport_SchemaVersionPin pins the CURRENT schema version -- a
// bare literal the report-construction site sets; this pin makes a future
// silent revert (or a rebase that forgets to recount both constants,
// exactly the failure mode a codex xhigh review caught live during
// CHAOS-4313's own rebase onto CHAOS-4307) a red test instead of a passing
// report someone has to notice by eye. Originally CHAOS-4307's own
// "...IsThirty" (pinned v30); renamed on CHAOS-4313's own bump to a
// version-stable name so the test's own name never hardcodes a historical
// version the live constant has since moved past -- update the expected
// literal (never the assertion's SHAPE) on every future bump, same
// discipline reportSchemaVersion's own doc comment already documents.
// CHAOS-4314's own rebase onto CHAOS-4313's second bump (#287, v32,
// responder_effort) is exactly that next bump: v33. CHAOS-4386's own bump to
// v40 (result_bytes/est_tokens plus the run-level result-bytes distribution)
// is the next one after that; its own answer-rate follow-up bump to v41
// (terminal_status/claimed_facts_count/rows_count/terminal_reason plus
// answer_rate) is the one after that.
func TestTwoTurnReport_SchemaVersionPin(t *testing.T) {
	t.Parallel()
	if reportSchemaVersion != "42" {
		t.Errorf("reportSchemaVersion = %q, want %q (CHAOS-4525: cohort_answer_expected per row, widening the answer_rate denominator to the discovered-cohort class)", reportSchemaVersion, "42")
	}
}

// chaos4307FakeInvestigator (codex round 2, Medium, confirmed) simulates the
// exact shape a real production Investigate() call can take: a
// confirmed_kind_scope trace event fires during resolution, and the call
// STILL fails later at a downstream stage (graph/fact/synthesis) --
// buildConfirmedKindScopedSnapshot has already run and returned before
// engine.go's own later stages get a chance to fail. trace is the SAME
// *twoTurnTraceCapture the arm function under test was given, mirroring
// exactly how the real engine's ResolutionTracer hook and the harness's
// error-returning Investigate() call share one object in production.
type chaos4307FakeInvestigator struct {
	trace *twoTurnTraceCapture
	err   error
}

func (f *chaos4307FakeInvestigator) Investigate(_ context.Context, _ storage.Principal, _ contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
	if f.trace != nil {
		f.trace.Trace(graphrank.ResolutionTraceEvent{
			Stage:                                      "confirmed_kind_scope",
			ConfirmedKindVectorScopeState:              "complete",
			ConfirmedKindVectorScopePopulationCount:    7,
			ConfirmedKindVectorScopeComparisonCount:    14,
			ConfirmedKindVectorScopeRivalCountAboveTau: 1,
		})
	}
	return contractsv1.ContextFabricInvestigationResult{}, f.err
}

// TestRunTwoTurnPositiveArm_CensusEventSurvivesAnInvestigateError (codex
// round 2, Medium, confirmed) pins the CHAOS-4307 fix at
// runTwoTurnPositiveArm's own turn-2 call site: before the fix,
// res.TraceEvents was set ONLY by twoTurnStampDecision on the success path,
// so a confirmed_kind_scope event the engine emitted before a LATER failure
// was silently dropped on the early error return, undercounting the report's
// own census rollup with no observable symptom. Red/green proved by hand
// (revert the `res.TraceEvents = trace.snapshot()` line added alongside the
// synthesis-override fold in runTwoTurnPositiveArm, confirm this exact
// assertion fails with an empty TraceEvents slice, restore, confirm green).
func TestRunTwoTurnPositiveArm_CensusEventSurvivesAnInvestigateError(t *testing.T) {
	t.Parallel()
	trace := &twoTurnTraceCapture{}
	fake := &chaos4307FakeInvestigator{trace: trace, err: errors.New("boom: downstream stage failure after resolution already ran")}
	tc := trialCase{Question: "does this test question ever matter for a stubbed investigator"}
	entry := twoTurnOracleEntry{Index: 0, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "work_item"}
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result00000000000000000000000000000turn1",
		StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
			KindOptions: []contractsv1.ContextFabricKindOption{{ReceiptID: "kindr_test000000000000000000000000", Kind: "work_item"}},
		},
	}

	res := runTwoTurnPositiveArm(t, context.Background(), fake, storage.Principal{OrgID: "org1"}, 0, tc, entry, turn1, time.Second, trace, "")

	if res.ArmInvalidReason == "" {
		t.Fatalf("res.ArmInvalidReason = %q, want a non-empty investigate-error reason (res=%+v)", res.ArmInvalidReason, res)
	}
	if len(res.TraceEvents) != 1 || res.TraceEvents[0].Stage != "confirmed_kind_scope" || res.TraceEvents[0].ConfirmedKindVectorScopeState != "complete" {
		t.Fatalf("res.TraceEvents = %+v, want the one confirmed_kind_scope/complete event captured before the Investigate() error, not dropped by the early return", res.TraceEvents)
	}
}

// chaos4307ConfirmedWrongFakeInvestigator is
// chaos4307FakeInvestigator's own call-counting twin for
// runTwoTurnConfirmedWrongArm specifically: that arm makes TWO Investigate()
// calls for a non-anchor member (an unconfirmed setup call that redeems no
// receipt and cannot reach the confirmed-kind-scoped census path, then the
// real receipt-redeeming call that can) -- the setup call must SUCCEED
// (offering the negative back) so the test actually reaches the second call
// this fix targets, rather than exiting early on the first.
type chaos4307ConfirmedWrongFakeInvestigator struct {
	trace   *twoTurnTraceCapture
	calls   int
	setupOK contractsv1.ContextFabricInvestigationResult
	mainErr error
}

func (f *chaos4307ConfirmedWrongFakeInvestigator) Investigate(_ context.Context, _ storage.Principal, _ contractsv1.ContextFabricInvestigationRequest) (contractsv1.ContextFabricInvestigationResult, error) {
	f.calls++
	if f.calls == 1 {
		return f.setupOK, nil
	}
	if f.trace != nil {
		f.trace.Trace(graphrank.ResolutionTraceEvent{
			Stage:                                      "confirmed_kind_scope",
			ConfirmedKindVectorScopeState:              "complete",
			ConfirmedKindVectorScopePopulationCount:    7,
			ConfirmedKindVectorScopeComparisonCount:    14,
			ConfirmedKindVectorScopeRivalCountAboveTau: 1,
		})
	}
	return contractsv1.ContextFabricInvestigationResult{}, f.mainErr
}

// TestRunTwoTurnConfirmedWrongArm_CensusEventSurvivesAnInvestigateError is
// TestRunTwoTurnPositiveArm_CensusEventSurvivesAnInvestigateError's own twin
// for runTwoTurnConfirmedWrongArm's main (receipt-redeeming) turn-2 call --
// the ONE call site in that arm that can reach the confirmed-kind-scoped
// census path at all (the unconfirmed setup call above redeems no receipt,
// so it cannot trigger buildConfirmedKindScopedSnapshot -- see that arm's
// own CHAOS-4307 comment). Red/green proved by hand (revert the
// `res.TraceEvents = trace.snapshot()` line added alongside the
// synthesis-override fold at this arm's main call site, confirm this exact
// assertion fails with an empty TraceEvents slice, restore, confirm green).
func TestRunTwoTurnConfirmedWrongArm_CensusEventSurvivesAnInvestigateError(t *testing.T) {
	t.Parallel()
	trace := &twoTurnTraceCapture{}
	entry := twoTurnOracleEntry{
		Index: 0, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind),
		NegativeKind: "repository", NegativeCommittable: true,
	}
	fake := &chaos4307ConfirmedWrongFakeInvestigator{
		trace: trace,
		setupOK: contractsv1.ContextFabricInvestigationResult{
			ResultID: "result00000000000000000000000000000setup",
			StructureNeeds: &contractsv1.ContextFabricStructureNeeds{
				KindOptions: []contractsv1.ContextFabricKindOption{{ReceiptID: "kindr_test000000000000000000000000", Kind: "repository"}},
			},
		},
		mainErr: errors.New("boom: downstream stage failure after resolution already ran"),
	}
	tc := trialCase{Question: "does this test question ever matter for a stubbed investigator"}

	res := runTwoTurnConfirmedWrongArm(t, context.Background(), fake, nil, storage.Principal{OrgID: "org1"}, 0, tc, entry, time.Second, nil, "runtoken", trace)

	if res.ArmInvalidReason == "" {
		t.Fatalf("res.ArmInvalidReason = %q, want a non-empty error reason (res=%+v) -- the setup call must have offered the negative back for this test's fixture to be valid", res.ArmInvalidReason, res)
	}
	if len(res.TraceEvents) != 1 || res.TraceEvents[0].Stage != "confirmed_kind_scope" || res.TraceEvents[0].ConfirmedKindVectorScopeState != "complete" {
		t.Fatalf("res.TraceEvents = %+v, want the one confirmed_kind_scope/complete event captured before the Investigate() error, not dropped by the early return", res.TraceEvents)
	}
}
