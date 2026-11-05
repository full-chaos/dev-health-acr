package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// readFallbackInterpreter resolves the cycle-time question of result_da32d7dc:
// family discovered_cohort_ranking over a validated discovered_kind team
// frame, judgment kind from the case.
type readFallbackInterpreter struct{ interpretation InterpretedQuestion }

func (i readFallbackInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	frame := QuestionFrame{
		Version: QuestionFrameVersion,
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam},
		},
		Obligations: []AnswerObligation{ObligationRanking, ObligationState},
		Goals:       []InvestigationGoal{GoalRankOrSurvey},
		Temporal:    TemporalIntentCurrent,
	}
	validated := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	accepted := validated.Frame
	return i.interpretation, QuestionFamilyOutcome{
		Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel,
		Frame: &accepted, Gate: DecideFrameGate(validated, true),
	}, nil
}

// capabilityFactReader is a fact reader that declares capabilities, as the
// registry does, and records every request it is asked.
type capabilityFactReader struct {
	capabilities []FactCapability
	requested    *[][]FactKind
}

func (r capabilityFactReader) Capabilities() []FactCapability { return r.capabilities }

func (r capabilityFactReader) ReadFacts(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	var kinds []FactKind
	for _, requirement := range request.Requirements {
		kinds = append(kinds, requirement.Kind)
	}
	*r.requested = append(*r.requested, kinds)
	return CanonicalFactBundle{
		Facts: []CanonicalFact{healthFact("ALPHA", "high"), healthFact("BETA", "low")},
		Coverage: Coverage{
			Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
			DegradedReasons: []string{},
		},
		Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
	}, nil
}

type scriptedReadPlanner struct {
	result ReadPlanningResult
	inputs []ReadPlanningInput
}

func (p *scriptedReadPlanner) PlanReads(_ context.Context, _ storage.Principal, input ReadPlanningInput) ReadPlanningResult {
	p.inputs = append(p.inputs, input)
	return p.result
}

type fixedWatermarks struct{}

func (fixedWatermarks) SnapshotSourceWatermarks(context.Context, string) (SourceWatermarkSnapshot, error) {
	return SourceWatermarkSnapshot{"canonical_fact:health": "w1"}, nil
}

type fixedEpoch struct{}

func (fixedEpoch) SnapshotRebuildEpoch(context.Context, string) (int64, error) { return 7, nil }

func readFallbackTeamCapabilities() []FactCapability {
	team := []SubjectKind{SubjectTeam, SubjectProject}
	return []FactCapability{
		{Kind: FactBlockers, SupportedSubjectKinds: append([]SubjectKind{SubjectWorkItem}, team...)},
		{Kind: FactMetrics, SupportedSubjectKinds: append([]SubjectKind{SubjectRepository}, team...)},
		{Kind: FactHealth, SupportedSubjectKinds: append([]SubjectKind{SubjectRepository}, team...)},
		{Kind: FactWorkload, SupportedSubjectKinds: team},
		{Kind: FactInvestment, SupportedSubjectKinds: team},
		{Kind: FactReadiness, SupportedSubjectKinds: team},
		{Kind: FactOperationalDeficiencies, SupportedSubjectKinds: []SubjectKind{SubjectTeam}},
		{Kind: FactFileHotspots, SupportedSubjectKinds: []SubjectKind{SubjectRepository}},
	}
}

type readFallbackRun struct {
	result    InvestigationResult
	telemetry *recordingTelemetry
	store     *resultStoreStub
	requested [][]FactKind
}

// runReadFallback drives the real Engine.Investigate chain for the cycle-time
// question. planner nil means no planner is wired at all.
func runReadFallback(t *testing.T, kind RequestedJudgmentKind, enabled bool, planner ReadPlanner) readFallbackRun {
	t.Helper()
	run := readFallbackRun{telemetry: &recordingTelemetry{}, store: &resultStoreStub{}}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "the team with the highest cycle time, stuck the most", RequestedJudgmentKind: kind,
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactBlockers}},
	}
	cohort := &Cohort{
		Kind: SubjectTeam, Rationale: "kind census match",
		Members: []CohortMember{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:ALPHA", Label: "Alpha"}, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:BETA", Label: "Beta"}, Rank: 2, InclusionReasons: []string{"matched"}},
		},
	}
	dependencies := EngineDependencies{
		Interpreter: readFallbackInterpreter{interpretation: interpretation},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: capabilityFactReader{capabilities: readFallbackTeamCapabilities(), requested: &run.requested},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Alpha appears to lead.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Alpha appears to lead.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: run.store, Telemetry: run.telemetry,
		ReuseSnapshotter: fixedWatermarks{}, ReuseEpochSnapshotter: fixedEpoch{},
		ReadPlanner: planner,
	}
	engine, err := NewEngine(dependencies, EngineOptions{
		ServiceVersion: "acr-test", ReadFallbackEnabled: enabled,
		Now: func() time.Time { return time.Unix(1300200000, 0).UTC() }, NewResultID: func() string { return "result_13002001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_13002001"
	request.Question = "What team has the highest cycle time and appears to be stuck the most?"
	run.result, err = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if run.result.AnswerPlan == nil {
		t.Fatal("the served result carries no plan")
	}
	return run
}

func planJSON(t *testing.T, result InvestigationResult) string {
	t.Helper()
	raw, err := json.Marshal(result.AnswerPlan)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func resultJSON(t *testing.T, result InvestigationResult) string {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func requireOneReadFallbackDecision(t *testing.T, run readFallbackRun, want ReadFallbackDecisionEvent) {
	t.Helper()
	if len(run.telemetry.readFallbackDecisions) != 1 || run.telemetry.readFallbackDecisions[0] != want {
		t.Fatalf("read fallback decisions = %+v, want exactly %+v", run.telemetry.readFallbackDecisions, want)
	}
}

// TestReadFallbackPlansTheCycleTimeQuestionsReads is the motivating question
// through the real chain: the ranking family cannot serve a performance
// judgment, the planner picks metrics, and the fact read asks for it.
func TestReadFallbackPlansTheCycleTimeQuestionsReads(t *testing.T) {
	t.Parallel()
	planner := &scriptedReadPlanner{result: ReadPlanningResult{Kinds: []FactKind{FactMetrics}}}
	run := runReadFallback(t, RequestedJudgmentKindPerformance, true, planner)
	plan := run.result.AnswerPlan
	if plan.Family != QuestionFamilyDiscoveredCohortRanking || run.result.Cohort == nil || len(run.result.Cohort.Members) != 2 {
		t.Fatalf("fixture drift: family %q cohort %+v, want the discovered team ranking", plan.Family, run.result.Cohort)
	}
	if len(planner.inputs) != 1 {
		t.Fatalf("planner calls = %d, want 1", len(planner.inputs))
	}
	input := planner.inputs[0]
	if input.Reason != ReadFallbackReasonJudgmentUnservable || input.MemberKind != SubjectTeam || input.RequestID != "request_13002001" ||
		!slices.Contains(input.OfferedKinds, FactMetrics) || slices.Contains(input.OfferedKinds, FactFileHotspots) || len(input.Capabilities) == 0 {
		t.Fatalf("planner input = %+v", input)
	}
	if len(plan.FactKinds) == 0 || plan.FactKinds[len(plan.FactKinds)-1] != FactMetrics {
		t.Fatalf("plan fact kinds = %v, want metrics appended last", plan.FactKinds)
	}
	want := &AnswerPlanFallback{Reason: ReadFallbackReasonJudgmentUnservable, PlannedKinds: []FactKind{FactMetrics}, Outcome: contractsv1.ContextFabricReadFallbackOutcomeApplied}
	if plan.Fallback == nil || plan.Fallback.Reason != want.Reason || plan.Fallback.Outcome != want.Outcome || !slices.Equal(plan.Fallback.PlannedKinds, want.PlannedKinds) {
		t.Fatalf("plan fallback = %+v, want %+v", plan.Fallback, want)
	}
	if !slices.Contains(run.result.Limitations, contractsv1.ContextFabricReadFallbackLimitation) {
		t.Fatalf("limitations = %q, want the read fallback sentence", run.result.Limitations)
	}
	if err := run.result.Validate(); err != nil {
		t.Fatalf("the served result does not validate: %v", err)
	}
	read := false
	for _, kinds := range run.requested {
		read = read || slices.Contains(kinds, FactMetrics)
	}
	if !read {
		t.Fatalf("fact requests = %v, want metrics read", run.requested)
	}
	if run.store.saved.AnswerPlan == nil || run.store.saved.AnswerPlan.Fallback == nil {
		t.Fatal("the saved result lost the fallback record")
	}
	if run.store.savedSnapshot != nil || run.store.savedEpoch != nil {
		t.Fatalf("saved reuse snapshot %v epoch %v, want none: an answer whose reads the model chose is never reused", run.store.savedSnapshot, run.store.savedEpoch)
	}
	requireOneReadFallbackDecision(t, run, ReadFallbackDecisionEvent{Reason: ReadFallbackReasonJudgmentUnservable, Outcome: ReadFallbackOutcomeApplied, PlannedKinds: 1})
}

// TestReadFallbackThatDoesNotApplyLeavesTheAnswerAsToday holds every path
// that does not apply the planner's kinds against the same question answered
// by an engine with no planner and the switch off: the plan, and the whole
// served result, are byte-identical, and the result stays reusable.
func TestReadFallbackThatDoesNotApplyLeavesTheAnswerAsToday(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		kind    RequestedJudgmentKind
		enabled bool
		planner *scriptedReadPlanner
		calls   int
		event   ReadFallbackDecisionEvent
	}{
		{
			name: "switch off", kind: RequestedJudgmentKindPerformance,
			planner: &scriptedReadPlanner{result: ReadPlanningResult{Kinds: []FactKind{FactMetrics}}},
			event:   ReadFallbackDecisionEvent{Reason: ReadFallbackReasonJudgmentUnservable, Outcome: ReadFallbackOutcomeSuppressedFlagOff},
		},
		{
			name: "planner error", kind: RequestedJudgmentKindPerformance, enabled: true,
			planner: &scriptedReadPlanner{result: ReadPlanningResult{Err: ErrModelUnavailable}}, calls: 1,
			event: ReadFallbackDecisionEvent{Reason: ReadFallbackReasonJudgmentUnservable, Outcome: ReadFallbackOutcomePlannerFailed, ErrorClass: "model_unavailable"},
		},
		{
			name: "refused plan", kind: RequestedJudgmentKindPerformance, enabled: true,
			planner: &scriptedReadPlanner{result: ReadPlanningResult{Rejection: ReadPlanRejectionKindNotOffered}}, calls: 1,
			event: ReadFallbackDecisionEvent{Reason: ReadFallbackReasonJudgmentUnservable, Outcome: ReadFallbackOutcomePlanInvalid, Rejection: ReadPlanRejectionKindNotOffered},
		},
		{
			name: "fit family", kind: RequestedJudgmentKindAttention, enabled: true,
			planner: &scriptedReadPlanner{result: ReadPlanningResult{Kinds: []FactKind{FactMetrics}}},
			event:   ReadFallbackDecisionEvent{Outcome: ReadFallbackOutcomeNotApplicable},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseline := runReadFallback(t, tc.kind, false, nil)
			run := runReadFallback(t, tc.kind, tc.enabled, tc.planner)
			if len(tc.planner.inputs) != tc.calls {
				t.Fatalf("planner calls = %d, want %d", len(tc.planner.inputs), tc.calls)
			}
			if got, want := planJSON(t, run.result), planJSON(t, baseline.result); got != want {
				t.Fatalf("plan differs from today's:\n got %s\nwant %s", got, want)
			}
			if got, want := resultJSON(t, run.result), resultJSON(t, baseline.result); got != want {
				t.Fatalf("served result differs from today's:\n got %s\nwant %s", got, want)
			}
			if run.store.savedSnapshot == nil || run.store.savedEpoch == nil {
				t.Fatal("a family answer lost its reuse watermarks")
			}
			if slices.Contains(run.result.Limitations, contractsv1.ContextFabricReadFallbackLimitation) {
				t.Fatal("an answer the planner did not plan says the model chose its reads")
			}
			requireOneReadFallbackDecision(t, run, tc.event)
		})
	}
}

func TestReadFallbackWithNoPlannerWiredKeepsTheFamilyPlan(t *testing.T) {
	t.Parallel()
	baseline := runReadFallback(t, RequestedJudgmentKindPerformance, false, nil)
	run := runReadFallback(t, RequestedJudgmentKindPerformance, true, nil)
	if got, want := resultJSON(t, run.result), resultJSON(t, baseline.result); got != want {
		t.Fatalf("served result differs from today's:\n got %s\nwant %s", got, want)
	}
	requireOneReadFallbackDecision(t, run, ReadFallbackDecisionEvent{Reason: ReadFallbackReasonJudgmentUnservable, Outcome: ReadFallbackOutcomePlannerFailed, ErrorClass: readPlannerNoRuntime})
}

// TestReadPlanKindsOnlyWidenThePlanLast is the differential over PlanAnswer:
// for every family, with and without interpreter kinds and requirement rows,
// ReadPlanKinds leaves every field of the plan as it was and only appends the
// kinds no earlier source named, after all of them.
func TestReadPlanKindsOnlyWidenThePlanLast(t *testing.T) {
	t.Parallel()
	interpretations := []InterpretedQuestion{
		{},
		{FactRequirements: []FactRequirement{{Kind: FactBlockers}, {Kind: FactMetrics}}},
	}
	for _, family := range QuestionFamilyVocabulary() {
		for _, interpretation := range interpretations {
			input := PlanAnswerInput{Family: QuestionFamilyOutcome{Family: family, Source: QuestionFamilySourceModel}, Interpretation: interpretation}
			before := PlanAnswer(input)
			input.ReadPlanKinds = []FactKind{FactMetrics, FactFlow, FactHealth}
			after := PlanAnswer(input)
			if !slices.Equal(after.FactKinds[:len(before.FactKinds)], before.FactKinds) {
				t.Fatalf("%s: plan kinds %v do not keep %v as their prefix", family, after.FactKinds, before.FactKinds)
			}
			var added []FactKind
			for _, kind := range input.ReadPlanKinds {
				if !slices.Contains(before.FactKinds, kind) {
					added = append(added, kind)
				}
			}
			if !slices.Equal(after.FactKinds[len(before.FactKinds):], added) {
				t.Fatalf("%s: appended %v, want %v", family, after.FactKinds[len(before.FactKinds):], added)
			}
			after.FactKinds = before.FactKinds
			beforeJSON, _ := json.Marshal(before)
			afterJSON, _ := json.Marshal(after)
			if string(beforeJSON) != string(afterJSON) {
				t.Fatalf("%s: read plan kinds changed more than the kind list:\n%s\n%s", family, beforeJSON, afterJSON)
			}
		}
	}
}

func TestReadFallbackOutcomeVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	seen := map[ReadFallbackOutcome]bool{}
	for _, outcome := range ReadFallbackOutcomeVocabulary() {
		if outcome == "" || seen[outcome] {
			t.Fatalf("outcome %q is empty or repeated", outcome)
		}
		seen[outcome] = true
	}
	if string(ReadFallbackOutcomeApplied) != string(contractsv1.ContextFabricReadFallbackOutcomeApplied) {
		t.Fatal("the telemetry applied outcome and the wire one differ")
	}
	if !errors.Is(ErrModelUnavailable, ErrModelUnavailable) {
		t.Fatal("unreachable")
	}
}

func TestReadPlannerDeadlineIsCappedAndLeavesTheSynthesisReserve(t *testing.T) {
	t.Parallel()
	engine := &Engine{synthesisDeadlineReserve: 10 * time.Second}
	if got := engine.readPlannerDeadline(context.Background()); got != readPlannerDeadlineCap {
		t.Fatalf("no request deadline: %v, want the %v cap", got, readPlannerDeadlineCap)
	}
	far, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := engine.readPlannerDeadline(far); got != readPlannerDeadlineCap {
		t.Fatalf("a far deadline: %v, want the %v cap", got, readPlannerDeadlineCap)
	}
	near, cancelNear := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancelNear()
	if got := engine.readPlannerDeadline(near); got > 8*time.Second || got < 7*time.Second {
		t.Fatalf("an 18s deadline with a 10s reserve: %v, want about 8s", got)
	}
	spent, cancelSpent := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSpent()
	if got := engine.readPlannerDeadline(spent); got != 0 {
		t.Fatalf("a deadline inside the reserve: %v, want 0", got)
	}
}
