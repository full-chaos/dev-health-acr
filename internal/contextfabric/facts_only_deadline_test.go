package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// factsOnlySynth writes a model answer that overruns the item budget and
// composes the production degraded answer when asked.
type factsOnlySynth struct {
	modelCalls *int
	degraded   *int
	claims     int
	// ownSubject writes the claims about the cohort member itself, as a work-item
	// tuple requires.
	ownSubject bool
}

func (s factsOnlySynth) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	*s.modelCalls++
	claims := []ClaimedFact{}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			for claim := 0; claim < s.claims; claim++ {
				claims = append(claims, ClaimedFact{
					ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
					Kind:    FactStatus, Subject: s.claimSubject(member.Subject), Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
		}
	}
	return InvestigationResult{
		Status: InvestigationComplete, DirectJudgment: "Model prose.", CurrentState: "Model prose.",
		StrongestPressures: []string{"Model pressure."}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
		ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
		Limitations: []string{"Model limitation."}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
		Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Model prose.", Warnings: []string{},
		Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
	}, nil
}

func (s factsOnlySynth) claimSubject(member SubjectRef) SubjectRef {
	if s.ownSubject {
		return member
	}
	return retryFixtureClaimSubject(member)
}

func (s factsOnlySynth) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	*s.degraded++
	return RuntimeAnswerSynthesizer{FactCapabilities: k7Capabilities()}.ComposeDegraded(ctx, principal, input, failure)
}

func factsOnlyEngine(t *testing.T, cohort *Cohort, options EngineOptions, modelCalls, degraded *int, claims int, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			facts := []CanonicalFact{}
			for index, member := range cohort.Members {
				if index >= 2 {
					break
				}
				facts = append(facts, CanonicalFact{Kind: FactStatus, Subject: member.Subject, Fields: map[string]FactValue{"status": StringFactValue("green")},
					EvidenceRefIDs: []string{"ref_" + member.Subject.CanonicalID}, Source: "devhealthfacts.status", SourceVersion: "v1"})
			}
			return CanonicalFactBundle{Facts: facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: factsOnlySynth{modelCalls: modelCalls, degraded: degraded, claims: claims},
		Telemetry:   telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func TestAModelAnswerOverBudgetWithNoRoomToRetryIsServedAsFactsOnly(t *testing.T) {
	t.Parallel()
	const reserve = 20 * time.Second
	cases := []struct {
		name      string
		cohort    *Cohort
		items     int
		remaining time.Duration
		reserve   time.Duration
		claims    int
		declined  RetryDeclinedReason
		class     SynthesisFailureClass
		limit     string
	}{
		{"insufficient deadline", budgetStageCohort(6), 12, 10 * time.Second, reserve, 2, RetryDeclinedInsufficientDeadline, contractsv1.ContextFabricSynthesisFailureDeadline, contractsv1.ContextFabricSynthesisDeadlineLimitation},
		{"nothing to narrow", budgetStageCohort(1), 4, time.Minute, time.Second, 20, RetryDeclinedNothingToNarrow, contractsv1.ContextFabricSynthesisFailureNotNarrowable, contractsv1.ContextFabricSynthesisNotNarrowableLimitation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var modelCalls, degraded int
			telemetry := &recordingTelemetry{}
			engine := factsOnlyEngine(t, tc.cohort, budgetStageOptions(tc.items, tc.reserve), &modelCalls, &degraded, tc.claims, telemetry)
			ctx, cancel := context.WithTimeout(context.Background(), tc.remaining)
			defer cancel()

			result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

			if err != nil {
				t.Fatalf("error = %v, want the facts-only answer served", err)
			}
			if modelCalls != 1 || degraded != 1 {
				t.Fatalf("model calls = %d, degraded compositions = %d, want the one model pass and one composition", modelCalls, degraded)
			}
			assertRetryRanSentence(t, result, false)
			if result.Status != InvestigationDegraded || !slices.Contains(result.Limitations, tc.limit) {
				t.Fatalf("status = %q limitations = %q, want degraded and facts only", result.Status, result.Limitations)
			}
			if !slices.Contains(result.Warnings, synthesisFailureWarning(tc.class)) {
				t.Fatalf("warnings = %q, want the %s warning", result.Warnings, tc.class)
			}
			classes := []SynthesisFailureClass{}
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisNotRun {
					classes = append(classes, detail.FailureClass)
				}
			}
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed {
					t.Fatalf("a facts-only answer filed %+v, which says a model call failed", detail)
				}
			}
			if !slices.Equal(classes, []SynthesisFailureClass{tc.class}) {
				t.Fatalf("coverage failure classes = %q, want [%s]", classes, tc.class)
			}
			encoded, marshalErr := json.Marshal(result)
			for _, leaked := range []string{"Model prose.", "Model pressure.", "Model limitation."} {
				if marshalErr != nil || strings.Contains(string(encoded), leaked) {
					t.Fatalf("the served document carries %q from the model draft (marshal err %v)", leaked, marshalErr)
				}
			}
			for _, claim := range result.ClaimedFacts {
				if !strings.HasPrefix(claim.ClaimID, "server:fact:") {
					t.Fatalf("claim %q is not a server-minted canonical fact claim", claim.ClaimID)
				}
			}
			var served *PlanNarrowingEvent
			for index := range telemetry.planNarrowings {
				if telemetry.planNarrowings[index].FactsOnlyServed {
					served = &telemetry.planNarrowings[index]
				}
				if telemetry.planNarrowings[index].RefusalPlanned {
					t.Fatalf("a refusal was planned for a served answer: %+v", telemetry.planNarrowings[index])
				}
			}
			if served == nil || served.RetryDeclined != tc.declined {
				t.Fatalf("served event = %+v, want facts_only_served with retry_declined %q", served, tc.declined)
			}
		})
	}
}

func TestAFactsOnlyAnswerThatDoesNotFitLeavesTheRefusal(t *testing.T) {
	t.Parallel()
	var modelCalls, degraded int
	telemetry := &recordingTelemetry{}
	// The byte ceiling is under what any composition of six members weighs.
	options := budgetStageOptions(12, 20*time.Second)
	options.MaxSerializedBytes = 3000
	engine := factsOnlyEngine(t, budgetStageCohort(6), options, &modelCalls, &degraded, 2, telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want the planned refusal", err)
	}
	var refusal *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].RefusalPlanned {
			refusal = &telemetry.planNarrowings[index]
		}
	}
	if refusal == nil || !refusal.FactsOnlyAttempted || refusal.FactsOnlyServed || refusal.RetryDeclined != RetryDeclinedInsufficientDeadline {
		t.Fatalf("refusal event = %+v, want facts_only_attempted, not served, insufficient_deadline", refusal)
	}
}

func factsOnlyGroupedCohort() *Cohort {
	cohort := groupedCohort(2, 3)
	cohort.Rationale, cohort.Complete = "facts-only fixture", true
	for index := range cohort.Members {
		member := &cohort.Members[index]
		member.Subject.Kind = SubjectProject
		member.Subject.Label = member.Subject.CanonicalID
		member.Rank = index + 1
		member.InclusionReasons = []string{"Graph retrieval associated this subject with the requested condition."}
	}
	for index := range cohort.Groups {
		cohort.Groups[index].Subject.Kind = SubjectTeam
		cohort.Groups[index].Total, cohort.Groups[index].Complete = len(cohort.Groups[index].MemberCanonicalIDs), true
		cohort.Groups[index].Subject.Label = cohort.Groups[index].Subject.CanonicalID
	}
	return &cohort
}

func TestAFactsOnlyAnswerOverAGroupedCohortKeepsEveryGroupAndMemberAndNarrowsNothing(t *testing.T) {
	t.Parallel()
	var modelCalls, degraded int
	telemetry := &recordingTelemetry{}
	cohort := factsOnlyGroupedCohort()
	engine := factsOnlyEngine(t, cohort, budgetStageOptions(12, 20*time.Second), &modelCalls, &degraded, 2, telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if err != nil {
		t.Fatalf("error = %v, want the facts-only answer served", err)
	}
	if modelCalls != 1 || degraded != 1 || result.Status != InvestigationDegraded {
		t.Fatalf("model calls = %d, compositions = %d, status = %q, want one of each and degraded", modelCalls, degraded, result.Status)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != len(cohort.Members) || len(result.Cohort.Groups) != len(cohort.Groups) {
		t.Fatalf("cohort = %+v, want all %d members in %d groups", result.Cohort, len(cohort.Members), len(cohort.Groups))
	}
	var served *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if event := telemetry.planNarrowings[index]; event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult && event.Before != event.After {
			t.Fatalf("assembled_result narrowing %+v, but a facts-only answer narrows nothing", event)
		}
		if telemetry.planNarrowings[index].FactsOnlyServed {
			served = &telemetry.planNarrowings[index]
		}
	}
	if served == nil || served.Before != len(cohort.Members) || served.After != len(cohort.Members) || served.Groups {
		t.Fatalf("served event = %+v, want before == after == %d and no group narrowing", served, len(cohort.Members))
	}
}

func TestAFactsOnlyAnswerWithNoScalarFactNeverSaysAModelCallFailed(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{degradedClaimFact(1, 1, map[string]FactValue{"rows": {Rows: []FactValueRow{{Fields: map[string]FactValue{"x": StringFactValue("y")}}}}})}
	for _, class := range []SynthesisFailureClass{contractsv1.ContextFabricSynthesisFailureDeadline, contractsv1.ContextFabricSynthesisFailureNotNarrowable, contractsv1.ContextFabricSynthesisFailureRowsOverBudget, contractsv1.ContextFabricSynthesisFailureRetryOverBudget} {
		failure := &SynthesisFailure{Class: class, DeferEvent: true, cause: errFactsOnlyNoSynthesisRoom}

		result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, SynthesisInput{Facts: CanonicalFactBundle{Facts: facts}}, failure)

		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result.CurrentState, "model call") || result.CurrentState == "" {
			t.Fatalf("class %s: current_state = %q, want a sentence that does not say a model call failed", class, result.CurrentState)
		}
		if len(result.ClaimedFacts) != 0 || !slices.Contains(result.Warnings, synthesisFailureWarning(class)) {
			t.Fatalf("class %s: claims = %d warnings = %q, want no claims and the class warning", class, len(result.ClaimedFacts), result.Warnings)
		}
	}
}

func TestAWorkItemTupleAnswerOverBudgetWithNoRoomToRetryIsServedAsFactsOnly(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	payload := workItemTuplePayloadFixture(t)
	members := []WorkItemMembershipMember{}
	for i := 0; i < 6; i++ {
		raw := fmt.Sprintf("work-%d", i)
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", raw}, nil)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, WorkItemMembershipMember{CanonicalID: id, WorkItemID: raw})
	}
	var modelCalls, degraded int
	telemetry := &recordingTelemetry{}
	options := budgetStageOptions(250, 20*time.Second)
	options.MaxSerializedBytes = 9000
	engine := factsOnlyEngine(t, budgetStageCohort(6), options, &modelCalls, &degraded, 10, telemetry)
	frame := ValidateFrame(prospectiveTupleFrame(GoalCountOrAggregate), nil, "").Frame
	question := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), question.TimeContext), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
	engine.synthesizer = factsOnlySynth{modelCalls: &modelCalls, degraded: &degraded, claims: 10, ownSubject: true}
	engine.interpreter = familyInterpreter{interpreted: question, outcome: outcome}
	engine.graph = &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
		return true, ""
	}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	engine.workItemMembership = tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
		lease, err := gate.Acquire(ctx)
		return lease, WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: len(members)}, Members: members}, err
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())

	if err != nil {
		t.Fatalf("error = %v, want the facts-only tuple answer served", err)
	}
	if modelCalls != 1 || degraded < 1 || result.Status != InvestigationDegraded || !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisDeadlineLimitation) {
		t.Fatalf("model calls = %d compositions = %d status = %q limitations = %q, want one model pass, a composition, degraded, with the deadline limitation", modelCalls, degraded, result.Status, result.Limitations)
	}
	var served bool
	for _, event := range telemetry.planNarrowings {
		served = served || event.FactsOnlyServed
	}
	if !served {
		t.Fatal("no facts_only_served event: the tuple answer was served by another lever")
	}
	if cohortMemberCount(result.Cohort) != len(members) {
		t.Fatalf("cohort has %d members, want all %d", cohortMemberCount(result.Cohort), len(members))
	}
	if gate.Stats().InFlight != 0 {
		t.Error("facts-only tuple answer leaked a membership lease")
	}
}

// memberCutDegradeCapable gives the member-cut fixture's model synthesizer the degraded composer production has.
type memberCutDegradeCapable struct{ AnswerSynthesizer }

func (m memberCutDegradeCapable) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	return RuntimeAnswerSynthesizer{FactCapabilities: k7Capabilities()}.ComposeDegraded(ctx, principal, input, failure)
}

func factsOnlyServedEvent(telemetry *recordingTelemetry) *PlanNarrowingEvent {
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].FactsOnlyServed {
			return &telemetry.planNarrowings[index]
		}
	}
	return nil
}

// assertRetryRanSentence checks the retry limitation both ways: present exactly when a retry ran.
func assertRetryRanSentence(t *testing.T, result InvestigationResult, retryRan bool) {
	t.Helper()
	if has := slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisRetryRanLimitation); has != retryRan {
		t.Fatalf("retry-ran sentence present=%v, want %v (limitations %q)", has, retryRan, result.Limitations)
	}
}

func assertFactsOnlyLabelled(t *testing.T, result InvestigationResult, class SynthesisFailureClass) {
	t.Helper()

	notRun := 0
	for _, detail := range result.Coverage.Details {
		switch detail.Code {
		case contractsv1.ContextFabricCoverageDetailSynthesisNotRun:
			notRun++
			if detail.FailureClass != class {
				t.Fatalf("not_run detail class %q, want %q", detail.FailureClass, class)
			}
		case contractsv1.ContextFabricCoverageDetailSynthesisModelFailed:
			t.Fatalf("a facts-only answer filed %+v, which says a model call failed", detail)
		}
	}
	if notRun != 1 || result.Status != InvestigationDegraded || !result.Coverage.Partial {
		t.Fatalf("not_run details %d status %q partial %v, want one, degraded, partial", notRun, result.Status, result.Coverage.Partial)
	}
}

// The member cut declines for want of a second synthesis (no deadline reserve in this deployment, so the first
// answer was model-written and over the item budget): the facts are served, labelled, and the cut's trace says why it did not run.
func TestAMemberCutDeclinedForWantOfSynthesisTimeServesTheFactsLabelled(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine := memberCutEngine(t, memberCutGaps, nil, 0, telemetry)
	engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}

	result, err := investigateMemberCut(t, engine)

	if err != nil {
		t.Fatalf("error = %v, want the facts-only answer", err)
	}
	assertFactsOnlyLabelled(t, result, contractsv1.ContextFabricSynthesisFailureDeadline)
	assertRetryRanSentence(t, result, false)
	if len(telemetry.memberCuts) != 1 || telemetry.memberCuts[0].Declined != MemberCutNoSynthesisTime {
		t.Fatalf("member-cut events %+v, want one declined no_synthesis_time", telemetry.memberCuts)
	}
	if served := factsOnlyServedEvent(telemetry); served == nil || served.RetryDeclined != RetryDeclinedNoReserve {
		t.Fatalf("served event %+v, want facts_only_served with retry_declined no_reserve", served)
	}
	if cohortMemberCount(result.Cohort) != memberCutCallerCap {
		t.Fatalf("cohort has %d members, want all %d: the facts-only answer narrows nothing", cohortMemberCount(result.Cohort), memberCutCallerCap)
	}
}

// A bytes overrun is outside the member cut (it cuts members for items only): the facts-only answer is served from the
// early return of the same block, labelled the same way.
func TestABytesOverrunTheMemberCutDoesNotCoverServesTheFactsLabelled(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine := memberCutEngineAtCeiling(t, 10, nil, 0, 29000, memberCutPopulation, telemetry)
	engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}

	result, err := investigateMemberCut(t, engine)

	if err != nil {
		t.Fatalf("error = %v, want the facts-only answer", err)
	}
	assertFactsOnlyLabelled(t, result, contractsv1.ContextFabricSynthesisFailureDeadline)
	if len(telemetry.memberCuts) != 0 {
		t.Fatalf("member-cut events %+v, want none: the cut does not cover a bytes overrun", telemetry.memberCuts)
	}
}

// Refusals the budget block does not own stay refusals: a fact read that aborted never reaches a synthesis, and the retry
// reasons that say a second synthesis could not help (or that the caller wrote the synthesis) never serve facts-only.
func TestRefusalsOutsideTheBudgetBlockStayRefusals(t *testing.T) {
	t.Parallel()
	for _, declined := range []RetryDeclinedReason{RetryDeclinedSuppliedSynthesis, RetryDeclinedNotApplicable} {
		if factsOnlyDeclinedReason(declined) {
			t.Fatalf("retry declined %q serves facts-only", declined)
		}
	}
	for _, declined := range []RetryDeclinedReason{RetryDeclinedInsufficientDeadline, RetryDeclinedNoReserve, RetryDeclinedNothingToNarrow, RetryDeclinedCannotReduceAxis} {
		if !factsOnlyDeclinedReason(declined) {
			t.Fatalf("retry declined %q does not serve facts-only", declined)
		}
	}
	var modelCalls, degraded int
	engine := factsOnlyEngine(t, budgetStageCohort(2), budgetStageOptions(12, 20*time.Second), &modelCalls, &degraded, 2, &recordingTelemetry{})
	engine.facts = factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return CanonicalFactBundle{}, ErrFactReadAborted
	})
	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrFactReadAborted) || modelCalls != 0 || degraded != 0 {
		t.Fatalf("err %v model calls %d compositions %d, want the abort and no synthesis", err, modelCalls, degraded)
	}
}

// A facts-only answer over the byte ceiling is not served and not cut silently: the budget refusal stands with its
// continuation, and the trace says the facts-only answer was tried.
func TestAFactsOnlyAnswerOverTheByteCeilingLeavesTheBudgetRefusal(t *testing.T) {
	telemetry := &recordingTelemetry{}
	engine := memberCutEngineAtCeiling(t, 10, nil, 0, 28000, memberCutPopulation, telemetry)
	engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}

	_, err := investigateMemberCut(t, engine)

	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("err = %v, want the bytes budget refusal", err)
	}
	attempted := false
	for _, event := range telemetry.planNarrowings {
		attempted = attempted || (event.RefusalPlanned && event.FactsOnlyAttempted && !event.FactsOnlyServed)
	}
	if !attempted || factsOnlyServedEvent(telemetry) != nil {
		t.Fatal("the refusal event does not say the facts-only answer was tried and not served")
	}
}

// The rows alone overran and a second synthesis could not shrink them (cannot_reduce_axis): the facts-only answer
// carries scalar values, not tables, so it is served when it fits.
func TestAByteOverrunTheRowsAloneCauseServesTheFactsLabelled(t *testing.T) {
	var calls int
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558Shape{rowPadding: 2000, maxBytes: 40000})
	engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())

	if err != nil {
		t.Fatalf("error = %v, want the facts-only answer", err)
	}
	assertFactsOnlyLabelled(t, result, contractsv1.ContextFabricSynthesisFailureRowsOverBudget)
	assertRetryRanSentence(t, result, false)
	if served := factsOnlyServedEvent(telemetry); served == nil || served.RetryDeclined != RetryDeclinedCannotReduceAxis {
		t.Fatalf("served event %+v, want facts_only_served with retry_declined cannot_reduce_axis", served)
	}
}

// The rows were the blocker and the cohort could still be narrowed: the answer says the rows put it over the budget and
// never says the cohort could not be narrowed, in its limitation, warning, current state or coverage label.
func TestAFactsOnlyAnswerOverRowsNamesTheRowsNotTheCohort(t *testing.T) {
	var calls int
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558Shape{rowPadding: 2000, maxBytes: 40000})
	engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())

	if err != nil {
		t.Fatalf("error = %v, want the facts-only answer", err)
	}
	if cohortMemberCount(result.Cohort) < 2 {
		t.Fatalf("cohort has %d members, want a narrowable cohort", cohortMemberCount(result.Cohort))
	}
	assertServedTexts(t, result, contractsv1.ContextFabricSynthesisRowsOverBudgetLimitation,
		"because of the rows it carried", "narrowed")
}

// assertServedTexts checks every served text of a facts-only answer: the limitation list is exactly the given sentence,
// and the warning, current state and coverage label carry the stated phrase and never the forbidden one.
func assertServedTexts(t *testing.T, result InvestigationResult, limitation, mustSay, mustNotSay string) {
	t.Helper()
	if !slices.Equal(result.Limitations, []string{limitation}) {
		t.Fatalf("limitations = %q, want exactly %q", result.Limitations, limitation)
	}
	texts := append([]string{}, result.Warnings...)
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisNotRun {
			texts = append(texts, detail.Label)
		}
	}
	if len(texts) < 2 {
		t.Fatalf("served texts %q, want a warning and a label", texts)
	}
	for _, text := range append(texts, limitation, result.CurrentState) {
		if strings.Contains(text, mustNotSay) {
			t.Errorf("text %q says %q", text, mustNotSay)
		}
	}
	for _, text := range texts {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(mustSay)) {
			t.Errorf("text %q does not say %q", text, mustSay)
		}
	}
}

// The retry ran and its answer still overran: the facts the first pass read are served instead of the refusal.
func TestARetryThatStillOverranServesTheFactsLabelled(t *testing.T) {
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, time.Second), &calls, telemetry)
	engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if err != nil {
		t.Fatalf("error = %v, want the facts-only answer", err)
	}
	assertFactsOnlyLabelled(t, result, contractsv1.ContextFabricSynthesisFailureRetryOverBudget)
	assertRetryRanSentence(t, result, true)
	assertServedTexts(t, result, contractsv1.ContextFabricSynthesisRetryRanLimitation, "narrower retry also ran", "could not be narrowed")
	served := factsOnlyServedEvent(telemetry)
	if calls != 2 || served == nil || !served.RetryAttempted {
		t.Fatalf("model calls %d served event %+v, want the retry to have run and the served event to say so", calls, served)
	}
	for _, event := range telemetry.planNarrowings {
		if event.RefusalPlanned {
			t.Fatalf("a refusal was planned for a served answer: %+v", event)
		}
	}
}

// supplied_synthesis stays a refusal: the caller wrote the answer, so facts-only is not eligible for a client synthesis.
func TestASuppliedSynthesisIsNeverReplacedByTheFactsOnlyAnswer(t *testing.T) {
	var modelCalls, degraded int
	engine := factsOnlyEngine(t, budgetStageCohort(2), budgetStageOptions(12, 20*time.Second), &modelCalls, &degraded, 2, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	if factsOnlyDeclinedReason(RetryDeclinedSuppliedSynthesis) || engine.factsOnlyEligible(context.Background(), synthesisAssemblyParams{Request: func() InvestigationRequest { r := request; r.SynthesisMode = SynthesisModeClient; return r }()}, InvestigationResult{}) {
		t.Fatal("a supplied synthesis is eligible for the facts-only answer")
	}
}

// faultySynthesizer wraps the fixture's model synthesizer; its degraded composition fails or returns what the test
// plants, so the facts-only answer's own fault paths can be driven.
type faultySynthesizer struct {
	AnswerSynthesizer
	compose func(InvestigationResult) (InvestigationResult, error)
}

func (f faultySynthesizer) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	result, err := RuntimeAnswerSynthesizer{FactCapabilities: k7Capabilities()}.ComposeDegraded(ctx, principal, input, failure)
	if err != nil {
		return result, err
	}
	return f.compose(result)
}

func factsOnlyDeclinedEvent(telemetry *recordingTelemetry) *PlanNarrowingEvent {
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].RefusalPlanned {
			return &telemetry.planNarrowings[index]
		}
	}
	return nil
}

func driveFaultyFactsOnly(t *testing.T, compose func(InvestigationResult) (InvestigationResult, error)) (*recordingTelemetry, string, error) {
	t.Helper()
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	var modelCalls, degraded int
	telemetry := &recordingTelemetry{}
	engine := factsOnlyEngine(t, budgetStageCohort(6), budgetStageOptions(12, 20*time.Second), &modelCalls, &degraded, 2, telemetry)
	engine.synthesizer = faultySynthesizer{AnswerSynthesizer: engine.synthesizer, compose: compose}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	return telemetry, logs.String(), err
}

// A fault while assembling the facts-only answer is named on the refusal event and its class is logged at Warn.
func TestAFactsOnlyAnswerThatCannotBeComposedSaysSoOnTheRefusalEvent(t *testing.T) {
	telemetry, logs, err := driveFaultyFactsOnly(t, func(InvestigationResult) (InvestigationResult, error) {
		return InvestigationResult{}, errors.New("composer fault")
	})

	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("err = %v, want the budget refusal to stand", err)
	}
	event := factsOnlyDeclinedEvent(telemetry)
	if event == nil || !event.FactsOnlyAttempted || event.FactsOnlyDeclined != FactsOnlyComposeFailed {
		t.Fatalf("refusal event %+v, want facts_only_attempted with facts_only_declined=compose_failed", event)
	}
	if !strings.Contains(logs, "facts_only_declined=compose_failed") || !strings.Contains(logs, "error_class=internal") || strings.Contains(logs, "composer fault") {
		t.Fatalf("log %q, want the token and the error class and not the error text", logs)
	}
}

// A fault while measuring the assembled facts-only answer (here an answer that cannot be encoded) says measure_failed.
func TestAFactsOnlyAnswerThatCannotBeMeasuredSaysSoOnTheRefusalEvent(t *testing.T) {
	telemetry, logs, err := driveFaultyFactsOnly(t, func(result InvestigationResult) (InvestigationResult, error) {
		result.Drivers = append(result.Drivers, DriverJudgment{DriverID: "d1", Standing: DriverPrincipal, Category: "relationship", Title: "t", Summary: "s",
			AffectedSubjects: []SubjectRef{}, EvidenceRefIDs: []string{}, Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
			Confidence: math.Inf(1), Current: true})
		return result, nil
	})

	event := factsOnlyDeclinedEvent(telemetry)
	if !errors.Is(err, ErrAnswerExceedsBudget) || event == nil || event.FactsOnlyDeclined != FactsOnlyMeasureFailed {
		t.Fatalf("err %v event %+v, want the refusal and facts_only_declined=measure_failed", err, event)
	}
	if !strings.Contains(logs, "facts_only_declined=measure_failed") {
		t.Fatalf("log %q, want the measure_failed token", logs)
	}
}

// A facts-only answer that does not fit says over_budget, not a fault.
func TestAFactsOnlyAnswerOverBudgetSaysSoOnTheRefusalEvent(t *testing.T) {
	telemetry := &recordingTelemetry{}
	options := budgetStageOptions(12, 20*time.Second)
	options.MaxSerializedBytes = 3000
	var modelCalls, degraded int
	engine := factsOnlyEngine(t, budgetStageCohort(6), options, &modelCalls, &degraded, 2, telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	event := factsOnlyDeclinedEvent(telemetry)
	if !errors.Is(err, ErrAnswerExceedsBudget) || event == nil || event.FactsOnlyDeclined != FactsOnlyOverBudget {
		t.Fatalf("err %v event %+v, want the refusal and facts_only_declined=over_budget", err, event)
	}
}

// An answer that served, or was never tried, carries no declined token.
func TestAServedFactsOnlyAnswerCarriesNoDeclinedToken(t *testing.T) {
	telemetry := &recordingTelemetry{}
	var modelCalls, degraded int
	engine := factsOnlyEngine(t, budgetStageCohort(6), budgetStageOptions(12, 20*time.Second), &modelCalls, &degraded, 2, telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatal(err)
	}
	for _, event := range telemetry.planNarrowings {
		if event.FactsOnlyDeclined != FactsOnlyServedOrUntried {
			t.Fatalf("event %+v carries a declined token on a served answer", event)
		}
	}
}

// scoredFacts adds to an engine's fact read a health fact carrying a declared score (compounding_risk, drivers table
// compounding_risk_drivers) about the subject, beside whatever the engine read.
func scoredFacts(engine *Engine, subject SubjectRef) {
	read := engine.facts
	engine.facts = factReaderFunc(func(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		bundle, err := read.ReadFacts(ctx, principal, request)
		if err != nil {
			return bundle, err
		}
		risk, severity := 0.99, "high"
		bundle.Facts = append(bundle.Facts, CanonicalFact{Kind: FactHealth, Subject: subject, Fields: map[string]FactValue{"compounding_risk": {Number: &risk}, "severity": {String: &severity}},
			EvidenceRefIDs: []string{"ref_score"}, Source: "devhealthfacts.health", SourceVersion: "v1"})
		return bundle, nil
	})
}

// assertScoreWithheld is decision K7 on a served facts-only answer: no claim for the declared score, the plain scalar
// beside it claimed, and one fact_score_withheld detail for the fact kind.
func assertScoreWithheld(t *testing.T, result InvestigationResult, plainClaimed ...bool) {
	t.Helper()
	score, plain, details := 0, 0, 0
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == FactHealth && claim.Field == "compounding_risk" {
			score++
		}
		if claim.Kind == FactHealth && claim.Field == "severity" {
			plain++
		}
	}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailFactScoreWithheld && detail.FactKind == FactHealth {
			details++
		}
	}
	wantPlain := 1
	if len(plainClaimed) > 0 && !plainClaimed[0] {
		wantPlain = plain // an item ceiling with no room for any claim: only the score's absence and the disclosure are asserted
	}
	if score != 0 || plain != wantPlain || details != 1 {
		t.Fatalf("score claims %d, severity claims %d, fact_score_withheld details %d, want 0, 1, 1: a facts-only answer never serves a bare score", score, plain, details)
	}
}

// K7 at every facts-only point (decision K7: never a bare score): the answer served instead of each refusal claims the
// plain scalar of a health fact, never its declared score, and discloses the withheld score once.
func TestEveryFactsOnlyPointWithholdsADeclaredScore(t *testing.T) {
	member := budgetStageCohort(1).Members[0].Subject
	invest := func(t *testing.T, engine *Engine, timeout time.Duration) InvestigationResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
		if err != nil {
			t.Fatalf("error = %v, want the facts-only answer", err)
		}
		if result.Status != InvestigationDegraded {
			t.Fatalf("status %q, want degraded", result.Status)
		}
		return result
	}
	t.Run("insufficient deadline", func(t *testing.T) {
		var modelCalls, degraded int
		engine := factsOnlyEngine(t, budgetStageCohort(6), budgetStageOptions(12, 20*time.Second), &modelCalls, &degraded, 2, &recordingTelemetry{})
		scoredFacts(engine, member)
		assertScoreWithheld(t, invest(t, engine, 10*time.Second))
	})
	t.Run("nothing to narrow", func(t *testing.T) {
		var modelCalls, degraded int
		engine := factsOnlyEngine(t, budgetStageCohort(1), budgetStageOptions(4, time.Second), &modelCalls, &degraded, 20, &recordingTelemetry{})
		scoredFacts(engine, member)
		assertScoreWithheld(t, invest(t, engine, time.Minute))
	})
	t.Run("no reserve, member cut declined", func(t *testing.T) {
		engine := memberCutEngine(t, memberCutGaps, nil, 0, &recordingTelemetry{})
		engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}
		scoredFacts(engine, SubjectRef{Kind: SubjectProject, CanonicalID: "project_000", Label: "project_000"})
		request := validInvestigationRequestWithConfirmedWindow()
		request.Options.MaxCohortMembers = memberCutCallerCap
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
		if err != nil {
			t.Fatalf("error = %v, want the facts-only answer", err)
		}
		assertScoreWithheld(t, result)
	})
	t.Run("bytes overrun, the cut does not apply", func(t *testing.T) {
		engine := memberCutEngineAtCeiling(t, 10, nil, 0, 29600, memberCutPopulation, &recordingTelemetry{})
		engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}
		scoredFacts(engine, SubjectRef{Kind: SubjectProject, CanonicalID: "project_000", Label: "project_000"})
		request := validInvestigationRequestWithConfirmedWindow()
		request.Options.MaxCohortMembers = memberCutCallerCap
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_listing"}, request)
		if err != nil {
			t.Fatalf("error = %v, want the facts-only answer", err)
		}
		assertScoreWithheld(t, result)
	})
	t.Run("rows alone overran", func(t *testing.T) {
		var calls int
		engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, chaos6558Shape{rowPadding: 2000, maxBytes: 40000})
		engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}
		scoredFacts(engine, chaos6558Cohort().Members[0].Subject)
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
		if err != nil {
			t.Fatalf("error = %v, want the facts-only answer", err)
		}
		assertScoreWithheld(t, result)
	})
	t.Run("retry ran and still overran", func(t *testing.T) {
		calls := 0
		engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, time.Second), &calls, &recordingTelemetry{})
		engine.synthesizer = memberCutDegradeCapable{engine.synthesizer}
		scoredFacts(engine, member)
		assertScoreWithheld(t, invest(t, engine, time.Minute), false)
	})
}
