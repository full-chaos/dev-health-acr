package contextfabric

// Acceptance proofs for CHAOS-3781 (TRD §19.8), AC-3781-1 through -7.
//
// These run the real Engine wired to the real RuntimeQuestionInterpreter /
// RuntimeAnswerSynthesizer adapters, exactly as acceptance_test.go does, so
// the temporal label is composed through the production path rather than
// asserted against a stub. The graph reader and fact reader are faked; the
// adapters that actually bind to a time have their own dedicated suites --
// falkorgraph/temporal_live_test.go against real FalkorDB, and
// devhealthfacts/shared_test.go for the three provider tiers.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// acceptanceNow must match buildAcceptanceEngine's clock, or a fixture's
// as-of would read as a request about the future.
var acceptanceNow = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

func historicalAsOf() time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) }

func historicalInterpretation(timeContext TimeContext) InterpretedQuestion {
	interpretation := bootstrapInterpretation()
	interpretation.TimeContext = timeContext
	return interpretation
}

// temporallyDegradedFactBundle is what a Tier C provider's refusal looks
// like once it reaches the engine: the source is healthy, it simply cannot
// speak for the requested time, so it reports not_applicable with the
// reason devhealthfacts emits verbatim.
func temporallyDegradedFactBundle(project SubjectRef) CanonicalFactBundle {
	bundle := bootstrapFactBundle(project)
	bundle.Coverage.Sources = append(bundle.Coverage.Sources, SourceObservation{
		Source: "canonical_fact:status", State: SourceNotApplicable,
		Reason: "devhealthfacts: this fact has no recorded history, so it cannot answer for a past time; only its current value exists",
	})
	return bundle
}

func runHistoricalAcceptance(t *testing.T, timeContext TimeContext, bundle CanonicalFactBundle) InvestigationResult {
	t.Helper()
	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bundle, nil
	})
	engine := buildAcceptanceEngine(t, graph, facts,
		historicalInterpretation(timeContext), bootstrapDraft(project), newMapResultStore())

	request := validInvestigationRequest()
	request.Question = "Was Ask Dev release-ready at the start of March?"
	request.TimeContext = timeContext
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// AC-3781-1: a question on the valid_time, observed_time, or range axis
// returns an answer, not a 400.
func TestAC_3781_1_HistoricalAxesReturnAnAnswerNotARefusal(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	start := asOf.Add(-30 * 24 * time.Hour)
	project := acceptanceProject()
	for _, testCase := range []struct {
		name string
		time TimeContext
	}{
		{"valid_time", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}},
		{"observed_time", TimeContext{Axis: TemporalObservedTime, AsOf: &asOf}},
		{"range", TimeContext{Axis: TemporalRange, Start: &start, End: &asOf}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := runHistoricalAcceptance(t, testCase.time, bootstrapFactBundle(project))
			if result.Status != InvestigationComplete {
				t.Fatalf("Status = %q, want an answer", result.Status)
			}
			if result.DirectJudgment == "" {
				t.Fatal("a historical question produced no judgment")
			}
		})
	}
}

// AC-3781-2: the answer states the as-of time, or the window, in a
// structured field -- and it must be the time the answer SPEAKS FOR, not
// merely the time that was requested.
func TestAC_3781_2_TheAnswerStatesTheTimeItSpeaksFor(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	project := acceptanceProject()
	result := runHistoricalAcceptance(t, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, bootstrapFactBundle(project))

	if result.Temporal == nil {
		t.Fatal("no temporal label; a historical answer with nothing marking it as one is the defect this issue removes")
	}
	if result.Temporal.Requested.Axis != TemporalValidTime || !result.Temporal.Requested.AsOf.Equal(asOf) {
		t.Fatalf("requested = %+v, want valid_time at %v", result.Temporal.Requested, asOf)
	}
	// Effective may only ever narrow. A label claiming to speak for a
	// LATER time than was asked about would be the false historical
	// answer wearing a structured field.
	if result.Temporal.Effective.AsOf.After(asOf) {
		t.Fatalf("effective as-of %v is after the requested %v", result.Temporal.Effective.AsOf, asOf)
	}
	if result.Temporal.Grain == "" {
		t.Fatal("the label states no grain, so a caller cannot tell instant precision from day precision")
	}
	// And it must survive the contract's own validation, which refuses a
	// historical result carrying no label at all.
	if err := result.Validate(); err != nil {
		t.Fatalf("a labeled historical result must be contract-valid: %v", err)
	}
}

// AC-3781-3: a subject that did not exist at the requested time returns a
// clear not-applicable state, NOT a current-state answer.
func TestAC_3781_3_ASubjectAbsentAtThatTimeIsNotAnswered(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	// The graph admits by validity window, so a subject whose window
	// excludes the requested time simply does not resolve -- this is what
	// falkorgraph's admission predicate produces, modeled here at the
	// engine boundary.
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			DriverCandidates: []DriverJudgment{}, EvidenceRefIDs: []string{}, FactRequirements: []FactRequirement{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	factsCalled := false
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		factsCalled = true
		if len(request.Subjects) != 0 {
			t.Errorf("facts were requested for %d subjects, but none existed at the requested time", len(request.Subjects))
		}
		return CanonicalFactBundle{
			Facts:    []CanonicalFact{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			Version:  "ops-v1",
		}, nil
	})
	// A no-match draft: with nothing resolved there is no evidence to
	// cite, so the ordinary bootstrap draft would (correctly) fail
	// value-level closure rather than exercise the temporal path.
	draft := SynthesisDraft{
		Status: InvestigationNoMatch, DirectJudgment: "", CurrentState: "", StrongestPressures: []string{},
		Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations: []string{"No subject existed at the requested time."}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
		DeterministicAnswer: "placeholder", Warnings: []string{},
	}
	engine := buildAcceptanceEngine(t, graph, facts,
		historicalInterpretation(TimeContext{Axis: TemporalValidTime, AsOf: &asOf}),
		draft, newMapResultStore())

	request := validInvestigationRequest()
	request.Question = "Was Ask Dev release-ready at the start of March?"
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match: a subject that did not exist then must not get a current-state answer", result.Status)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed = %#v, want nothing committed for a time before the subject existed", result.SubjectResolution.Committed)
	}
	if !factsCalled {
		t.Fatal("the fact path was skipped entirely; this test cannot prove no current-state facts leaked in")
	}
}

// AC-3781-5: a fact kind that cannot answer for the requested time reports
// its limitation, and the REST of the answer survives (§8.6).
func TestAC_3781_5_AnUnanswerableFactKindDegradesWithoutSinkingTheAnswer(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	project := acceptanceProject()
	result := runHistoricalAcceptance(t, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, temporallyDegradedFactBundle(project))

	// The rest survives.
	if result.DirectJudgment == "" || len(result.Drivers) == 0 {
		t.Fatal("a single temporally-unanswerable fact kind sank the whole answer; §8.6 requires the rest survive")
	}
	// The limitation is reported.
	var reported bool
	for _, source := range result.Coverage.Sources {
		if source.State == SourceNotApplicable && strings.Contains(source.Reason, "cannot answer for a past time") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("coverage does not report the temporal limitation: %#v", result.Coverage.Sources)
	}
	// And the label says coverage was incomplete, so a reader does not
	// have to parse coverage prose to learn it.
	if result.Temporal == nil || result.Temporal.CoverageComplete {
		t.Fatalf("temporal = %#v, want coverage_complete=false when a source could not answer for the requested time", result.Temporal)
	}
}

// AC-3781-6: the refusal is removed from the engine, from every provider,
// and from the route in the same change -- no layer keeps a stale one.
//
// The engine's half is proved here by construction: the retired sentinel
// no longer exists, so this file would not compile if any engine path
// still returned it. What this test adds is the behavioral half -- the
// axes that were refused now complete, and what replaced the refusal is
// strictly narrower.
func TestAC_3781_6_NoStaleRefusalSurvivesInTheEngine(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	project := acceptanceProject()

	result := runHistoricalAcceptance(t, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, bootstrapFactBundle(project))
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want a completed historical investigation", result.Status)
	}

	// The narrower replacement still fires, so the removal did not simply
	// delete the guard.
	future := acceptanceNow.Add(72 * time.Hour)
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	engine := buildAcceptanceEngine(t, graph, facts,
		historicalInterpretation(TimeContext{Axis: TemporalValidTime, AsOf: &future}),
		bootstrapDraft(project), newMapResultStore())
	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &future}
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); !errors.Is(err, ErrInvalidTimeBound) {
		t.Fatalf("Investigate() error = %v, want ErrInvalidTimeBound for a question about the future", err)
	}
}

// AC-3781-7: temporal comparison uses the epoch-nanosecond properties,
// never a string comparison of a timestamp.
//
// The graph's half is proved live in falkorgraph/temporal_live_test.go.
// This covers the reuse key, the other place a timestamp is compared: it
// must key on epoch nanoseconds, so one instant has exactly one key
// however it was formatted or zoned.
func TestAC_3781_7_TemporalKeysUseEpochNanosecondsNotStrings(t *testing.T) {
	t.Parallel()
	instant := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// A whole-second instant and a sub-second one render at DIFFERENT
	// string lengths (time.Format trims trailing zeros), which is exactly
	// why lexicographic comparison is wrong for these -- see nsTimestamp's
	// doc comment in falkorgraph.
	subSecond := instant.Add(time.Nanosecond)
	if TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &instant}) ==
		TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &subSecond}) {
		t.Fatal("two instants one nanosecond apart produced the same key; the key is not nanosecond-precise")
	}
	zoned := instant.In(time.FixedZone("elsewhere", -7*3600))
	if TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &instant}) !=
		TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &zoned}) {
		t.Fatal("the same instant in two zones produced different keys; the key is formatting-dependent, not epoch-based")
	}
}

// TestHistoricalAnswersDiscloseTheDeletedSubjectLimitation covers the
// standing disclosure §2.4 of the design requires: the graph holds only
// the CURRENT projection, so what a historical read can return is bounded
// by what still exists now. Unfixable here, but never silent.
func TestHistoricalAnswersDiscloseTheDeletedSubjectLimitation(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	project := acceptanceProject()
	result := runHistoricalAcceptance(t, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, bootstrapFactBundle(project))

	var disclosed bool
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "deleted at source") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("Limitations = %#v, want the deleted-subject disclosure on a historical answer", result.Limitations)
	}

	// A current-axis answer carries neither the label nor the disclosure.
	current := runHistoricalAcceptance(t, TimeContext{Axis: TemporalCurrent}, bootstrapFactBundle(project))
	if current.Temporal != nil {
		t.Fatal("a current-axis answer must carry no temporal label")
	}
	for _, limitation := range current.Limitations {
		if strings.Contains(limitation, "deleted at source") {
			t.Fatal("a current-axis answer must not carry the historical deleted-subject disclosure")
		}
	}
}

// TestObservedTimeAnswersDoNotClaimToBeObservedTime covers the axis
// substitution honestly.
//
// No source retains observation history, so the graph admits on the
// VALID-time window even when the caller asked about observed time. That
// approximation is better than no filtering, but presenting it AS observed
// time would be the same quiet mislabel the H6 refusal existed to prevent,
// one axis down. The answer must therefore report a grain of none,
// incomplete coverage, and say in words what it substituted.
func TestObservedTimeAnswersDoNotClaimToBeObservedTime(t *testing.T) {
	t.Parallel()
	asOf := historicalAsOf()
	project := acceptanceProject()
	result := runHistoricalAcceptance(t, TimeContext{Axis: TemporalObservedTime, AsOf: &asOf}, bootstrapFactBundle(project))

	if result.Temporal == nil {
		t.Fatal("no temporal label on an observed-time answer")
	}
	if result.Temporal.Grain != GrainNone {
		t.Fatalf("grain = %q, want %q: no source can speak on the observed-time axis", result.Temporal.Grain, GrainNone)
	}
	if result.Temporal.CoverageComplete {
		t.Fatal("coverage_complete = true on an observed-time answer, but no source answered on that axis")
	}
	var disclosed bool
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "not what was KNOWN then") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("Limitations = %#v, want the observed-time substitution stated in words", result.Limitations)
	}

	// A valid-time answer must NOT carry that disclosure -- it is not
	// substituting anything.
	validTime := runHistoricalAcceptance(t, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, bootstrapFactBundle(project))
	for _, limitation := range validTime.Limitations {
		if strings.Contains(limitation, "not what was KNOWN then") {
			t.Fatal("a valid-time answer must not carry the observed-time substitution disclosure")
		}
	}
}

// TestEffectiveRangeNarrowsToWholeDaysWithoutOverNarrowing pins the
// day-boundary rule: a range already starting ON a day boundary covers
// that day in full, so rounding it up anyway would under-report a whole
// day of coverage the answer genuinely has.
func TestEffectiveRangeNarrowsToWholeDaysWithoutOverNarrowing(t *testing.T) {
	t.Parallel()
	onBoundary := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	effective := effectiveTimeContext(TimeContext{Axis: TemporalRange, Start: &onBoundary, End: &end}, GrainDay)
	if !effective.Start.Equal(onBoundary) {
		t.Fatalf("effective start = %v, want the requested %v unchanged: it is already a whole day", effective.Start, onBoundary)
	}

	// A start partway through a day does round up, because that day is
	// not covered in full.
	midDay := time.Date(2026, 6, 1, 13, 30, 0, 0, time.UTC)
	effective = effectiveTimeContext(TimeContext{Axis: TemporalRange, Start: &midDay, End: &end}, GrainDay)
	want := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if !effective.Start.Equal(want) {
		t.Fatalf("effective start = %v, want %v", effective.Start, want)
	}

	// Narrowing must never invert, and must never widen past what was
	// asked for -- the direction the contract validates.
	narrow := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	narrowEnd := time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC)
	effective = effectiveTimeContext(TimeContext{Axis: TemporalRange, Start: &narrow, End: &narrowEnd}, GrainDay)
	if effective.Start.After(*effective.End) {
		t.Fatalf("effective window inverted: %v..%v", effective.Start, effective.End)
	}
	label := TemporalLabel{
		Requested: TimeContext{Axis: TemporalRange, Start: &narrow, End: &narrowEnd},
		Effective: effective, Grain: GrainDay,
	}
	if err := label.Validate(); err != nil {
		t.Fatalf("a sub-day range produced a label the contract rejects: %v", err)
	}
}

// --- CHAOS-3781 codex round-1 regressions ---

func gradedFactBundle(project SubjectRef, grain TemporalGrain) CanonicalFactBundle {
	bundle := bootstrapFactBundle(project)
	bundle.TemporalGrain = grain
	return bundle
}

// TestF1_ComposedGrainIsTheCoarsestContributingSource: the answer's grain
// is composed from what the providers reported, not assumed.
//
// The old code hardcoded day for any answered source, so a Tier B provider
// answering from an exact event timestamp -- a pull request merged at
// 14:00Z -- was reported at day grain, and the label's effective time was
// rounded back to midnight. That understates precision the data actually
// has, and it is a claim about the answer that the answer contradicts.
func TestF1_ComposedGrainIsTheCoarsestContributingSource(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 3, 1, 14, 0, 0, 0, time.UTC)
	project := acceptanceProject()

	// Only exact-grain providers contributed: the answer speaks for the
	// requested INSTANT, and the effective time is not rounded.
	instant := runHistoricalAcceptance(t,
		TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, gradedFactBundle(project, GrainInstant))
	if instant.Temporal.Grain != GrainInstant {
		t.Fatalf("grain = %q, want %q when only exact-grain providers contributed", instant.Temporal.Grain, GrainInstant)
	}
	if !instant.Temporal.Effective.AsOf.Equal(asOf) {
		t.Fatalf("effective as-of = %v, want the exact requested %v -- an instant-grain answer must not be rounded to a day",
			instant.Temporal.Effective.AsOf, asOf)
	}

	// A day-grain provider contributed: the whole answer is only as
	// precise as its least precise source, and the effective time rounds
	// back to the day it can actually speak for.
	day := runHistoricalAcceptance(t,
		TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, gradedFactBundle(project, GrainDay))
	if day.Temporal.Grain != GrainDay {
		t.Fatalf("grain = %q, want %q when a daily rollup contributed", day.Temporal.Grain, GrainDay)
	}
	if !day.Temporal.Effective.AsOf.Before(asOf) {
		t.Fatalf("effective as-of = %v, want it rounded back from %v at day grain", day.Temporal.Effective.AsOf, asOf)
	}

	// No provider reported a grain: nothing spoke for the requested time.
	none := runHistoricalAcceptance(t,
		TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, gradedFactBundle(project, ""))
	if none.Temporal.Grain != GrainNone || none.Temporal.CoverageComplete {
		t.Fatalf("temporal = %#v, want grain=none and incomplete coverage when no provider answered temporally", none.Temporal)
	}
}

// TestF1_CoarsestGrainWins pins the composition rule itself: an answer is
// only as precise as its least precise contributing source.
func TestF1_CoarsestGrainWins(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name          string
		first, second TemporalGrain
		want          TemporalGrain
	}{
		{"day beats instant", GrainInstant, GrainDay, GrainDay},
		{"day beats instant, other order", GrainDay, GrainInstant, GrainDay},
		{"instant only", GrainInstant, GrainInstant, GrainInstant},
		{"an absent grain never coarsens", GrainInstant, "", GrainInstant},
		{"an absent grain never refines", GrainDay, "", GrainDay},
		{"first absent takes the candidate", "", GrainInstant, GrainInstant},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := coarsestGrain(testCase.first, testCase.second); got != testCase.want {
				t.Fatalf("coarsestGrain(%q, %q) = %q, want %q", testCase.first, testCase.second, got, testCase.want)
			}
		})
	}
}

// TestF7_AToleratedFutureInstantIsClampedNotPropagated: the skew tolerance
// forgives a caller's clock, it does not admit a question about the
// future. Before the clamp, a now+30s as_of flowed straight through to the
// graph predicate and the answer's label, so the answer claimed to speak
// for a time that has not happened.
func TestF7_AToleratedFutureInstantIsClampedNotPropagated(t *testing.T) {
	t.Parallel()
	now := acceptanceNow
	slightlyAhead := now.Add(30 * time.Second) // inside futureSkewTolerance

	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	var boundTime TimeContext
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		boundTime = request.Question.TimeContext
		return gradedFactBundle(project, GrainInstant), nil
	})
	engine := buildAcceptanceEngine(t, graph, facts,
		historicalInterpretation(TimeContext{Axis: TemporalValidTime, AsOf: &slightlyAhead}),
		bootstrapDraft(project), newMapResultStore())

	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &slightlyAhead}
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a skewed clock to be tolerated, not refused", err)
	}
	if boundTime.AsOf.After(now) {
		t.Fatalf("the fact read was bound to %v, which is after now (%v); a tolerated instant must be clamped, never propagated", boundTime.AsOf, now)
	}
	if result.Temporal.Requested.AsOf.After(now) {
		t.Fatalf("the label reports %v, which is after now (%v); the answer must not claim to speak for the future", result.Temporal.Requested.AsOf, now)
	}
}

// TestR5_4_AnOutOfRangeInterpretedTimeIsRefusedAtTheEngineBoundary is
// round-5 R5-4, red→green.
//
// R4-4 bounds what a CALLER sent, in the request contract.
// QuestionInterpreter is a PORT, so what an interpreter RETURNS crosses a
// different trust boundary the contract never sees. A different
// implementation -- a future runtime, a test double, a differently-wired
// composition -- can hand back an out-of-range historical time that
// reaches UnixNano in the graph predicate and the reuse key and wraps
// there, exactly as a caller's would have.
//
// This test deliberately uses a BARE interpreterFunc rather than the real
// RuntimeQuestionInterpreter. That is not a shortcut, it IS the finding:
// the shipped adapter validates its own output, so through it the defect
// is unreachable and a test built on it proves only that one
// implementation is careful. The guarantee has to hold for any
// implementation of the port, so the test has to be one.
//
// The lesson is composition-root reachability: a bound enforced only
// inside the implementation that happens to ship today is a property of
// that implementation, not of the system.
func TestR5_4_AnOutOfRangeInterpretedTimeIsRefusedAtTheEngineBoundary(t *testing.T) {
	t.Parallel()
	yearOne := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	yearNineThousand := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	now := acceptanceNow

	for _, testCase := range []struct {
		name string
		time TimeContext
	}{
		{"interpreted as year 1", TimeContext{Axis: TemporalValidTime, AsOf: &yearOne}},
		{"interpreted as year 9999", TimeContext{Axis: TemporalValidTime, AsOf: &yearNineThousand}},
		{"interpreted range out of range", TimeContext{Axis: TemporalRange, Start: &yearOne, End: &yearNineThousand}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// The wire request is perfectly ordinary; ONLY the
			// interpreter returns something unrepresentable.
			engine, probe := mustHistoricalEngine(t, testCase.time, now)
			request := validInvestigationRequest()
			if request.TimeContext.Axis != TemporalCurrent {
				t.Fatalf("fixture axis = %q, want a current-axis wire request so only the interpreter is out of range", request.TimeContext.Axis)
			}

			_, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if !errors.Is(err, ErrInvalidTimeBound) {
				t.Fatalf("Investigate() error = %v, want ErrInvalidTimeBound -- an unrepresentable interpreted time must be refused, not wrapped", err)
			}
			if probe.graph.resolveCalls != 0 || probe.factsRead || probe.synthesized {
				t.Fatal("work ran with an unrepresentable time; the refusal must precede every capability call")
			}
		})
	}
}
