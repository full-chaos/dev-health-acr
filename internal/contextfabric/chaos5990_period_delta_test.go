package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestClassifyPeriodDeltaTransition_Total(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		prior, cur string
		want       PeriodDeltaTransition
	}{
		{"unchanged_low", "low", "low", PeriodDeltaTransitionUnchanged},
		{"unchanged_high", "high", "high", PeriodDeltaTransitionUnchanged},
		{"worsened_low_to_elevated", "low", "elevated", PeriodDeltaTransitionWorsened},
		{"worsened_low_to_high", "low", "high", PeriodDeltaTransitionWorsened},
		{"improved_high_to_elevated", "high", "elevated", PeriodDeltaTransitionImproved},
		{"improved_elevated_to_low", "elevated", "low", PeriodDeltaTransitionImproved},
		{"unknown_prior_real_current", "unknown", "low", PeriodDeltaTransitionUnknownPrior},
		{"unknown_current_real_prior", "high", "unknown", PeriodDeltaTransitionUnknownCurrent},
		{"unknown_both", "unknown", "unknown", PeriodDeltaTransitionUnknownBoth},
		// Out-of-vocabulary strings are treated exactly like "unknown" --
		// classifyPeriodDeltaTransition is TOTAL and never crashes or
		// silently ranks a malformed value against a real band.
		{"garbage_prior", "not-a-band", "low", PeriodDeltaTransitionUnknownPrior},
		{"garbage_both", "not-a-band", "also-not-a-band", PeriodDeltaTransitionUnknownBoth},
		{"empty_strings", "", "", PeriodDeltaTransitionUnknownBoth},
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
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tc   TimeContext
		want time.Time
	}{
		{"current_axis_uses_now", TimeContext{Axis: TemporalCurrent}, now},
		{"valid_time_uses_as_of", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, asOf},
		{"valid_time_nil_as_of_falls_back_to_now", TimeContext{Axis: TemporalValidTime}, now},
		{"range_uses_end", TimeContext{Axis: TemporalRange, End: &end}, end},
		{"range_nil_end_falls_back_to_now", TimeContext{Axis: TemporalRange}, now},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := periodDeltaCurrentAsOf(tc.tc, now)
			if !got.Equal(tc.want) {
				t.Errorf("periodDeltaCurrentAsOf(%+v, %v) = %v, want %v", tc.tc, now, got, tc.want)
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

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, currentAsOf)

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
	wantComposed := statusCategoryFactKindComposition[SubjectTeam]
	if len(event.ComposedKinds) != len(wantComposed) {
		t.Fatalf("event.ComposedKinds = %v, want %v", event.ComposedKinds, wantComposed)
	}
	for i, kind := range wantComposed {
		if event.ComposedKinds[i] != kind {
			t.Errorf("event.ComposedKinds[%d] = %q, want %q", i, event.ComposedKinds[i], kind)
		}
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

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now())

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

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{workItemSubject}, nil, time.Now())

	if len(reader.calls) != 0 {
		t.Errorf("applyPeriodDelta issued %d reads for a work_item-only subject list, want 0 (work_item is never composed by CHAOS-4347)", len(reader.calls))
	}
}

func TestApplyPeriodDelta_PriorReadError_LeavesFactsUntouched(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{err: context.DeadlineExceeded}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	teamSubject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	currentFacts := []CanonicalFact{periodDeltaHealthFact(teamSubject, map[string]FactValue{"severity": StringFactValue("low")})}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now())

	if len(reader.calls) != 1 {
		t.Fatalf("applyPeriodDelta issued %d reads, want exactly 1 attempt", len(reader.calls))
	}
	if _, ok := currentFacts[0].Fields["period_delta_transition"]; ok {
		t.Error("period_delta_transition was set despite a failed prior read -- a transient failure must never fabricate a comparison")
	}
	if len(telemetry.periodDeltaCompositions) != 0 {
		t.Errorf("recorded %d PeriodDeltaCompositionEvents on a failed prior read, want 0", len(telemetry.periodDeltaCompositions))
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

	engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{teamSubject}, currentFacts, time.Now())

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
