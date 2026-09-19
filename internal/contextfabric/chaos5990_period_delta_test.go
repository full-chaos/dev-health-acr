package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// periodDeltaKnownBandOrder is the test's OWN independent statement of the
// real-band ranking, deliberately re-derived rather than imported from
// periodDeltaBandOrder -- an expectation computed from the thing under
// test cannot fail (cf-lane-rules pins/telemetry rule), so this table is a
// second, hand-written source of the same ordering the production code
// happens to also encode.
var periodDeltaKnownBandOrder = map[string]int{"low": 1, "elevated": 2, "high": 3}

func TestClassifyPeriodDeltaTransition_Total(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		prior, cur string
		want       PeriodDeltaTransition
	}
	var cases []tc
	// The FULL 3x3 real-band matrix, generated rather than hand-picked, so
	// "full coverage" is a property of the loop, not a claim that can drift
	// from what is actually listed.
	realBands := []string{"low", "elevated", "high"}
	for _, prior := range realBands {
		for _, cur := range realBands {
			want := PeriodDeltaTransitionUnchanged
			switch {
			case periodDeltaKnownBandOrder[cur] > periodDeltaKnownBandOrder[prior]:
				want = PeriodDeltaTransitionWorsened
			case periodDeltaKnownBandOrder[cur] < periodDeltaKnownBandOrder[prior]:
				want = PeriodDeltaTransitionImproved
			}
			cases = append(cases, tc{name: "matrix_" + prior + "_to_" + cur, prior: prior, cur: cur, want: want})
		}
	}
	cases = append(cases,
		tc{"unknown_prior_real_current", "unknown", "low", PeriodDeltaTransitionUnknownPrior},
		tc{"unknown_current_real_prior", "high", "unknown", PeriodDeltaTransitionUnknownCurrent},
		tc{"unknown_both", "unknown", "unknown", PeriodDeltaTransitionUnknownBoth},
		// Out-of-vocabulary strings are treated exactly like "unknown" --
		// classifyPeriodDeltaTransition is TOTAL and never crashes or
		// silently ranks a malformed value against a real band.
		tc{"garbage_prior", "not-a-band", "low", PeriodDeltaTransitionUnknownPrior},
		tc{"garbage_both", "not-a-band", "also-not-a-band", PeriodDeltaTransitionUnknownBoth},
		tc{"empty_strings", "", "", PeriodDeltaTransitionUnknownBoth},
	)
	if len(cases) != len(realBands)*len(realBands)+6 {
		t.Fatalf("generated %d cases, want %d (a 3x3 matrix + 6 unknown/garbage cases) -- the case-generation loop itself is broken", len(cases), len(realBands)*len(realBands)+6)
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyPeriodDeltaTransition(tc.prior, tc.cur)
			if got != tc.want {
				t.Errorf("classifyPeriodDeltaTransition(%q, %q) = %q, want %q", tc.prior, tc.cur, got, tc.want)
			}
			if !ValidPeriodDeltaTransition(got) {
				t.Errorf("classifyPeriodDeltaTransition(%q, %q) returned %q, not a member of the closed vocabulary", tc.prior, tc.cur, got)
			}
		})
	}
}

// TestClassifyPeriodDeltaTransition_NeverUnknownWhenBothKnown asserts a
// domain-wide property rather than sampled cases: whenever both inputs are
// real bands, the transition is NEVER one of the three unknown_* members --
// a mutation that widened periodDeltaBandOrder's "unknown" handling, or
// weakened either !known branch's condition, would let a genuinely known
// pair fall through to an unknown_* member, which this sweeps the whole
// (3x3) domain to catch.
func TestClassifyPeriodDeltaTransition_NeverUnknownWhenBothKnown(t *testing.T) {
	t.Parallel()
	realBands := []string{"low", "elevated", "high"}
	for _, prior := range realBands {
		for _, current := range realBands {
			got := classifyPeriodDeltaTransition(prior, current)
			switch got {
			case PeriodDeltaTransitionUnknownPrior, PeriodDeltaTransitionUnknownCurrent, PeriodDeltaTransitionUnknownBoth:
				t.Errorf("classifyPeriodDeltaTransition(%q, %q) = %q -- both inputs are real bands, an unknown_* member is wrong", prior, current, got)
			}
		}
	}
}

func TestPeriodDeltaEligibleSubjects(t *testing.T) {
	t.Parallel()
	subjects := []SubjectRef{
		{Kind: SubjectWorkItem, CanonicalID: "work_item:W1"},
		{Kind: SubjectTeam, CanonicalID: "team:T1"},
		{Kind: SubjectRepository, CanonicalID: "repository:REPOALPHA"},
		{Kind: SubjectProject, CanonicalID: "project:PROJALPHA"},
	}
	got := periodDeltaEligibleSubjects(subjects)
	if len(got) != 2 {
		t.Fatalf("periodDeltaEligibleSubjects returned %d subjects, want 2 (team, project only): %+v", len(got), got)
	}
	if got[0].CanonicalID != "team:T1" || got[1].CanonicalID != "project:PROJALPHA" {
		t.Errorf("periodDeltaEligibleSubjects = %+v, want team then project in the order they were passed in", got)
	}
}

func TestPeriodDeltaEligibleSubjectKinds(t *testing.T) {
	t.Parallel()
	kinds := periodDeltaEligibleSubjectKinds()
	want := map[SubjectKind]bool{SubjectTeam: true, SubjectProject: true}
	if len(kinds) != len(want) {
		t.Fatalf("periodDeltaEligibleSubjectKinds returned %v, want exactly %v", kinds, want)
	}
	for _, kind := range kinds {
		if !want[kind] {
			t.Errorf("periodDeltaEligibleSubjectKinds returned %q, which is not team or project", kind)
		}
	}
}

func TestPeriodDeltaCurrentAsOf(t *testing.T) {
	t.Parallel()
	wall := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		tc     TimeContext
		want   time.Time
		wantOK bool
	}{
		{"current_axis_states_no_instant_uses_engine_clock", TimeContext{Axis: TemporalCurrent}, wall, true},
		{"valid_time_uses_as_of", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, asOf, true},
		{"observed_time_uses_as_of", TimeContext{Axis: TemporalObservedTime, AsOf: &asOf}, asOf, true},
		{"range_uses_end", TimeContext{Axis: TemporalRange, End: &end}, end, true},
		// A non-current axis with no instant is never defaulted to a clock.
		{"valid_time_without_instant_unresolved", TimeContext{Axis: TemporalValidTime}, time.Time{}, false},
		{"observed_time_without_instant_unresolved", TimeContext{Axis: TemporalObservedTime}, time.Time{}, false},
		{"range_without_end_unresolved", TimeContext{Axis: TemporalRange}, time.Time{}, false},
		{"unknown_axis_unresolved", TimeContext{Axis: "no_such_axis"}, time.Time{}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := periodDeltaCurrentAsOf(tc.tc, wall)
			if ok != tc.wantOK || !got.Equal(tc.want) {
				t.Errorf("periodDeltaCurrentAsOf(%+v) = (%v, %v), want (%v, %v)", tc.tc, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// recordingFactReader is a minimal CanonicalFactReader fake that records
// every call's Question.TimeContext (so a test can assert a SECOND,
// GENUINELY DISTINCT as-of was requested -- kill-needle: an offset of 0d
// instead of PeriodDeltaWindowDays must fail this) and returns a
// caller-supplied bundle or error.
type recordingFactReader struct {
	calls  []CanonicalFactRequest
	bundle CanonicalFactBundle
	err    error
}

func (r *recordingFactReader) ReadFacts(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	r.calls = append(r.calls, request)
	return r.bundle, r.err
}

func factValueString(v FactValue) string {
	if v.String == nil {
		return ""
	}
	return *v.String
}

func factValueInt(v FactValue) int64 {
	if v.Integer == nil {
		return 0
	}
	return *v.Integer
}

func periodDeltaTestPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-1"}
}

func periodDeltaHealthFact(subject SubjectRef, fields map[string]FactValue) CanonicalFact {
	return CanonicalFact{
		Kind:           FactHealth,
		Subject:        subject,
		Fields:         fields,
		EvidenceRefIDs: []string{"acr:v1:team:T1"},
		SourceState:    SourceAvailable,
		Source:         "devhealthfacts.health",
		SourceVersion:  "test",
	}
}

func TestApplyPeriodDelta_IssuesGenuineSecondReadAndClassifiesTransition(t *testing.T) {
	t.Parallel()
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	currentAsOf := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	reader := &recordingFactReader{
		bundle: CanonicalFactBundle{
			Facts: []CanonicalFact{
				periodDeltaHealthFact(teamSubject, map[string]FactValue{
					"severity":       StringFactValue("high"),
					"severity_as_of": StringFactValue("2026-08-19"),
				}),
			},
		},
	}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}

	currentFacts := []CanonicalFact{
		periodDeltaHealthFact(teamSubject, map[string]FactValue{
			"severity":       StringFactValue("elevated"),
			"severity_as_of": StringFactValue("2026-09-18"),
		}),
	}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, currentAsOf, true)

	if len(reader.calls) != 1 {
		t.Fatalf("applyPeriodDelta issued %d fact reads, want exactly 1", len(reader.calls))
	}
	got := reader.calls[0].Question.TimeContext
	if got.Axis != TemporalValidTime {
		t.Errorf("prior read Axis = %q, want %q", got.Axis, TemporalValidTime)
	}
	if got.AsOf == nil {
		t.Fatal("prior read AsOf is nil, want the current as-of minus PeriodDeltaWindowDays")
	}
	wantPrior := currentAsOf.Add(-PeriodDeltaWindowDays * 24 * time.Hour)
	if !got.AsOf.Equal(wantPrior) {
		t.Errorf("prior read AsOf = %v, want %v (currentAsOf - %d days) -- a 0-day offset would silently collapse the two reads to the same point", *got.AsOf, wantPrior, PeriodDeltaWindowDays)
	}

	fields := currentFacts[0].Fields
	if got := factValueString(fields["period_delta_transition"]); got != string(PeriodDeltaTransitionImproved) {
		t.Errorf("period_delta_transition = %q, want improved (high -> elevated)", got)
	}
	if got := factValueString(fields["period_delta_prior_band"]); got != "high" {
		t.Errorf("period_delta_prior_band = %q, want high", got)
	}
	if got := factValueString(fields["period_delta_prior_as_of"]); got != "2026-08-19" {
		t.Errorf("period_delta_prior_as_of = %q, want 2026-08-19", got)
	}
	if got := factValueInt(fields["period_delta_window_days"]); got != PeriodDeltaWindowDays {
		t.Errorf("period_delta_window_days = %d, want %d", got, PeriodDeltaWindowDays)
	}
	if _, ok := fields["period_delta_unavailable_reason"]; ok {
		t.Error("period_delta_unavailable_reason is set on a comparison that ran")
	}
	// The read that produced this fact's OWN severity/severity_as_of must
	// be untouched -- period-delta is additive only.
	if got := factValueString(fields["severity"]); got != "elevated" {
		t.Errorf("severity was overwritten to %q, want the pre-existing elevated value untouched", got)
	}

	if len(telemetry.periodDeltaCompositions) != 1 {
		t.Fatalf("recorded %d PeriodDeltaCompositionEvents, want 1", len(telemetry.periodDeltaCompositions))
	}
	event := telemetry.periodDeltaCompositions[0]
	if event.SubjectKind != SubjectTeam || event.Grain != SubjectTeam {
		t.Errorf("event SubjectKind/Grain = %q/%q, want team/team", event.SubjectKind, event.Grain)
	}
	if !event.PriorReadIssued {
		t.Error("event.PriorReadIssued = false, want true -- a read genuinely ran")
	}
	// ComposedKinds is the set the prior read REQUESTED, derived from the
	// status composition -- never a hard-coded kind.
	wantComposed := statusCategoryFactKindComposition[SubjectTeam]
	if len(event.ComposedKinds) != len(wantComposed) {
		t.Fatalf("event.ComposedKinds = %v, want %v", event.ComposedKinds, wantComposed)
	}
	for i, kind := range wantComposed {
		if event.ComposedKinds[i] != kind {
			t.Errorf("event.ComposedKinds[%d] = %q, want %q", i, event.ComposedKinds[i], kind)
		}
	}
	requested := map[FactKind]bool{}
	for _, requirement := range reader.calls[0].Requirements {
		requested[requirement.Kind] = true
	}
	for _, kind := range wantComposed {
		if !requested[kind] {
			t.Errorf("prior read did not request composed kind %q: requested %v", kind, reader.calls[0].Requirements)
		}
	}
	if len(requested) != len(wantComposed) {
		t.Errorf("prior read requested %d kinds, want exactly the composition's %d", len(requested), len(wantComposed))
	}
	if event.TransitionCounts[PeriodDeltaTransitionImproved] != 1 {
		t.Errorf("event.TransitionCounts[improved] = %d, want 1", event.TransitionCounts[PeriodDeltaTransitionImproved])
	}
	for transition, count := range event.TransitionCounts {
		if transition != PeriodDeltaTransitionImproved && count != 0 {
			t.Errorf("event.TransitionCounts[%q] = %d, want 0 -- every non-observed member must still be present at zero", transition, count)
		}
	}
}

func TestApplyPeriodDelta_NoObligation_NeverReads(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{}
	engine := &Engine{facts: reader, telemetry: &recordingTelemetry{}}
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	currentFacts := []CanonicalFact{periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("low")})}
	frame := QuestionFrame{} // no ObligationPeriodDelta

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now(), true)

	if len(reader.calls) != 0 {
		t.Errorf("applyPeriodDelta issued %d reads with no ObligationPeriodDelta, want 0", len(reader.calls))
	}
	if _, ok := currentFacts[0].Fields["period_delta_transition"]; ok {
		t.Error("period_delta_transition was set with no ObligationPeriodDelta on the frame")
	}
}

func TestApplyPeriodDelta_NoEligibleSubjects_NeverReads(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{}
	engine := &Engine{facts: reader, telemetry: &recordingTelemetry{}}
	workItemSubject := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item:W1"}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{workItemSubject}, nil, time.Now(), true)

	if len(reader.calls) != 0 {
		t.Errorf("applyPeriodDelta issued %d reads for a work_item-only subject list, want 0 (work_item is never composed by CHAOS-4347)", len(reader.calls))
	}
}

func TestApplyPeriodDelta_PriorReadError_DisclosedAsTypedOutcome(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantReason PeriodDeltaFailureReason
	}{
		{"deadline", context.DeadlineExceeded, PeriodDeltaFailureDeadlineExceeded},
		{"canceled", context.Canceled, PeriodDeltaFailureCanceled},
		{"other", errors.New("provider exploded"), PeriodDeltaFailureReadFailed},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &recordingFactReader{err: tc.err}
			telemetry := &recordingTelemetry{}
			engine := &Engine{facts: reader, telemetry: telemetry}
			teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
			currentFacts := []CanonicalFact{periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("low")})}
			frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

			engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now(), true)

			if len(reader.calls) != 1 {
				t.Fatalf("issued %d reads, want exactly 1 attempt", len(reader.calls))
			}
			fields := currentFacts[0].Fields
			if got := factValueString(fields["period_delta_transition"]); got != string(PeriodDeltaTransitionPriorReadFailed) {
				t.Errorf("served period_delta_transition = %q, want prior_read_failed", got)
			}
			if got := factValueString(fields["period_delta_unavailable_reason"]); got != string(tc.wantReason) {
				t.Errorf("served period_delta_unavailable_reason = %q, want %q", got, tc.wantReason)
			}
			if got := factValueString(fields["severity"]); got != "low" {
				t.Errorf("severity = %q, want the producer's low untouched", got)
			}
			if len(telemetry.periodDeltaCompositions) != 1 {
				t.Fatalf("recorded %d events, want 1", len(telemetry.periodDeltaCompositions))
			}
			event := telemetry.periodDeltaCompositions[0]
			if event.PriorReadIssued || event.FailureReason != tc.wantReason {
				t.Errorf("event PriorReadIssued=%v FailureReason=%q, want false/%q", event.PriorReadIssued, event.FailureReason, tc.wantReason)
			}
			if event.TransitionCounts[PeriodDeltaTransitionPriorReadFailed] != 1 {
				t.Errorf("event counts prior_read_failed = %d, want 1", event.TransitionCounts[PeriodDeltaTransitionPriorReadFailed])
			}
		})
	}
}

func TestApplyPeriodDelta_UnresolvedAnchor_NeverReadsAndDisclosed(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	currentFacts := []CanonicalFact{periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("low")})}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Time{}, false)

	if len(reader.calls) != 0 {
		t.Fatalf("issued %d reads with no anchor, want 0", len(reader.calls))
	}
	fields := currentFacts[0].Fields
	if got := factValueString(fields["period_delta_transition"]); got != string(PeriodDeltaTransitionAnchorUnresolved) {
		t.Errorf("served transition = %q, want anchor_unresolved", got)
	}
	if got := factValueString(fields["period_delta_unavailable_reason"]); got != string(PeriodDeltaFailureAnchorUnresolved) {
		t.Errorf("served reason = %q, want anchor_unresolved", got)
	}
	if len(telemetry.periodDeltaCompositions) != 1 || telemetry.periodDeltaCompositions[0].FailureReason != PeriodDeltaFailureAnchorUnresolved {
		t.Errorf("events = %+v, want one event with anchor_unresolved", telemetry.periodDeltaCompositions)
	}
}

func TestApplyPeriodDelta_MissingPriorFact_UnknownPrior(t *testing.T) {
	t.Parallel()
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	// The prior read succeeds but returns no row for this team at all --
	// distinct from an error, and distinct from a row carrying
	// severity="unknown".
	reader := &recordingFactReader{bundle: CanonicalFactBundle{}}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	currentFacts := []CanonicalFact{periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("low")})}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now(), true)

	fields := currentFacts[0].Fields
	if got := factValueString(fields["period_delta_transition"]); got != string(PeriodDeltaTransitionUnknownPrior) {
		t.Errorf("period_delta_transition = %q, want unknown_prior (no prior row at all)", got)
	}
	if got := factValueString(fields["period_delta_prior_band"]); got != PeriodDeltaBandUnknown {
		t.Errorf("period_delta_prior_band = %q, want %q", got, PeriodDeltaBandUnknown)
	}
	if _, ok := fields["period_delta_prior_as_of"]; ok {
		t.Error("period_delta_prior_as_of is set despite no prior row -- an unknown band never carries an as-of day")
	}
}

// TestApplyPeriodDelta_NoCurrentFact_MintedAndDisclosed covers a subject the
// primary read produced no fact of any kind for: the outcome is carried by a
// minimal minted fact naming its producer, and counted on the event.
func TestApplyPeriodDelta_NoCurrentFact_MintedAndDisclosed(t *testing.T) {
	t.Parallel()
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1", Label: "Team One"}
	reader := &recordingFactReader{bundle: CanonicalFactBundle{Facts: []CanonicalFact{
		periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("high")}),
	}}}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	served := engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, nil, time.Now(), true)

	if len(served) != 1 {
		t.Fatalf("served %d facts, want 1 minted", len(served))
	}
	fact := served[0]
	if fact.Source != PeriodDeltaProducer || fact.SourceVersion != PeriodDeltaVersion || fact.SourceState != SourceNoData {
		t.Errorf("minted fact source = %q/%q/%q", fact.Source, fact.SourceVersion, fact.SourceState)
	}
	if err := fact.Validate(true); err != nil {
		t.Errorf("minted fact does not validate: %v", err)
	}
	if got := factValueString(fact.Fields["period_delta_transition"]); got != string(PeriodDeltaTransitionUnknownCurrent) {
		t.Errorf("minted transition = %q, want unknown_current", got)
	}
	if got := factValueString(fact.Fields["period_delta_prior_band"]); got != "high" {
		t.Errorf("minted prior band = %q, want high", got)
	}
	event := telemetry.periodDeltaCompositions[0]
	if event.TransitionCounts[PeriodDeltaTransitionUnknownCurrent] != 1 || event.UnservedCount != 0 {
		t.Errorf("event counts=%v unserved=%d, want unknown_current=1 unserved=0", event.TransitionCounts, event.UnservedCount)
	}
}

// A subject whose identity cannot mint an evidence ref has no fact to
// carry its outcome: the event says so instead of dropping it.
func TestApplyPeriodDelta_NoCurrentFactAndNoRef_CountedUnserved(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project:not-a-v2-id"}
	reader := &recordingFactReader{}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	served := engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{subject}, nil, time.Now(), true)

	if len(served) != 0 {
		t.Fatalf("served %d facts, want 0 (no ref derivable)", len(served))
	}
	event := telemetry.periodDeltaCompositions[0]
	if event.UnservedCount != 1 || event.TransitionCounts[PeriodDeltaTransitionUnknownBoth] != 1 {
		t.Errorf("unserved=%d counts=%v, want unserved=1 unknown_both=1", event.UnservedCount, event.TransitionCounts)
	}
}

// A subject with a non-health current fact carries its outcome on that
// fact rather than minting a second one.
func TestApplyPeriodDelta_CarrierFallsBackToAnyCurrentFact(t *testing.T) {
	t.Parallel()
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	workload := periodDeltaHealthFact(teamSubject, map[string]FactValue{})
	workload.Kind = FactWorkload
	reader := &recordingFactReader{}
	engine := &Engine{facts: reader, telemetry: &recordingTelemetry{}}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}
	facts := []CanonicalFact{workload}

	served := engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, facts, time.Now(), true)

	if len(served) != 1 {
		t.Fatalf("served %d facts, want the one existing carrier", len(served))
	}
	if got := factValueString(served[0].Fields["period_delta_transition"]); got != string(PeriodDeltaTransitionUnknownBoth) {
		t.Errorf("carrier transition = %q, want unknown_both", got)
	}
}

func TestApplyPeriodDeltaForTurn_AnchorsToTheRequestNotADefaultClock(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	frame := &QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1", Label: "Team One"}
	cases := []struct {
		name      string
		tc        TimeContext
		wantReads int
		wantPrior time.Time
		wantTrans PeriodDeltaTransition
	}{
		{"current_uses_engine_clock", TimeContext{Axis: TemporalCurrent}, 1, clock.Add(-30 * 24 * time.Hour), PeriodDeltaTransitionUnknownBoth},
		{"observed_time_uses_its_instant", TimeContext{Axis: TemporalObservedTime, AsOf: &observed}, 1, observed.Add(-30 * 24 * time.Hour), PeriodDeltaTransitionUnknownBoth},
		{"observed_time_without_instant_is_disclosed_not_defaulted", TimeContext{Axis: TemporalObservedTime}, 0, time.Time{}, PeriodDeltaTransitionAnchorUnresolved},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &recordingFactReader{}
			engine := &Engine{facts: reader, telemetry: &recordingTelemetry{}, now: func() time.Time { return clock }}
			served := engine.applyPeriodDeltaForTurn(context.Background(), periodDeltaTestPrincipal(), frame, tc.tc, []SubjectRef{team}, nil)
			if len(reader.calls) != tc.wantReads {
				t.Fatalf("reads = %d, want %d", len(reader.calls), tc.wantReads)
			}
			if tc.wantReads == 1 {
				got := reader.calls[0].Question.TimeContext.AsOf
				if got == nil || !got.Equal(tc.wantPrior) {
					t.Errorf("prior as-of = %v, want %v", got, tc.wantPrior)
				}
			}
			if len(served) != 1 || factValueString(served[0].Fields["period_delta_transition"]) != string(tc.wantTrans) {
				t.Errorf("served = %+v, want one minted fact with transition %q", served, tc.wantTrans)
			}
		})
	}
}

func TestApplyPeriodDeltaForTurn_NoValidatedFrame_ChangesNothing(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{}
	engine := &Engine{facts: reader, telemetry: &recordingTelemetry{}, now: time.Now}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1", Label: "Team One"}
	served := engine.applyPeriodDeltaForTurn(context.Background(), periodDeltaTestPrincipal(), nil, TimeContext{Axis: TemporalCurrent}, []SubjectRef{team}, nil)
	if len(reader.calls) != 0 || len(served) != 0 {
		t.Errorf("reads=%d served=%d, want 0/0 without a frame", len(reader.calls), len(served))
	}
}

// One requirement per DISTINCT composed fact kind, even when team and project
// subjects share kinds (health, workload, ... appear under both).
func TestApplyPeriodDelta_RequirementsAreDistinctAndDerivedFromTheComposition(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{}
	engine := &Engine{facts: reader, telemetry: &recordingTelemetry{}}
	subjects := []SubjectRef{
		{Kind: SubjectTeam, CanonicalID: "team:T1", Label: "Team One"},
		{Kind: SubjectProject, CanonicalID: "project.v2:linear:PROJALPHA", Label: "Project Alpha"},
	}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}
	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, subjects, nil, time.Now(), true)
	want := map[FactKind]bool{}
	for _, subjectKind := range []SubjectKind{SubjectTeam, SubjectProject} {
		for _, kind := range statusCategoryFactKindComposition[subjectKind] {
			want[kind] = true
		}
	}
	seen := map[FactKind]int{}
	for _, requirement := range reader.calls[0].Requirements {
		seen[requirement.Kind]++
	}
	if len(seen) != len(want) {
		t.Fatalf("requested kinds %v, want the composition's %d distinct kinds", seen, len(want))
	}
	for kind, n := range seen {
		if !want[kind] || n != 1 {
			t.Errorf("kind %q requested %d times (in composition: %v), want once", kind, n, want[kind])
		}
	}
}
