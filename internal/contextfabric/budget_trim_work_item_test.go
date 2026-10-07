package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Prod shape (helm 41): a work-item members question over a project. 14
// members, a claimed-facts list of about 42 member-attributed facts, a spent
// deadline so the narrowing retry is declined, and a 30-item ceiling. The
// listed members are outside the ceiling (a walk list), so the claims alone
// overrun it by the amount members plus claims overran it when members were
// counted.
type budgetTrimShape struct {
	members     int
	claims      int
	maxItems    int
	citedClaims int
	reserve     time.Duration
	deadline    time.Duration
	findings    int
	maxBytes    int64
	noEvidence  bool
	symmetric   bool
	scoped      bool
	// evidenceMembers makes the synthesis cite the evidence of the first N members it read.
	evidenceMembers int
}

var budgetTrimProdShape = budgetTrimShape{members: 14, claims: 42, maxItems: 30, reserve: time.Second, deadline: 50 * time.Millisecond}

func budgetTrimFindings(count int, ids []string) []Finding {
	findings := []Finding{}
	for index := 0; index < count; index++ {
		findings = append(findings, Finding{FindingID: fmt.Sprintf("finding_%02d", index), Kind: "status", Summary: "work remains", Subjects: []SubjectRef{{Kind: SubjectWorkItem, CanonicalID: ids[index%len(ids)], Label: "Work item"}}, EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo-1:work-00")}, ClaimedFactIDs: []string{"claim_00"}})
	}
	return findings
}

func budgetTrimInvestigate(t *testing.T, shape budgetTrimShape) (InvestigationResult, error, int) {
	t.Helper()
	frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), nil, "").Frame
	payload := workItemTuplePayloadFixture(t)
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	members := make([]WorkItemMembershipMember, 0, shape.members)
	ids := make([]string, 0, shape.members)
	for index := 0; index < shape.members; index++ {
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", fmt.Sprintf("work-%02d", index)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		members = append(members, WorkItemMembershipMember{CanonicalID: id, WorkItemID: fmt.Sprintf("work-%02d", index)})
	}
	syntheses := 0
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: shape.members}, Members: members}, err
		}),
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			syntheses++
			claims := make([]ClaimedFact, 0, shape.claims)
			served := ids
			if input.Graph.Cohort != nil && len(input.Graph.Cohort.Members) > 0 {
				served = served[:0:0]
				for _, member := range input.Graph.Cohort.Members {
					served = append(served, member.Subject.CanonicalID)
				}
			}
			for index := 0; index < shape.claims; index++ {
				subject := SubjectRef{Kind: SubjectWorkItem, CanonicalID: served[index%len(served)], Label: "Work item"}
				value := fmt.Sprintf("state-%02d", index)
				claims = append(claims, ClaimedFact{ClaimID: fmt.Sprintf("claim_%02d", index), Kind: FactStatus, Subject: subject, Field: "status", Value: ScalarValue{String: &value}})
			}
			evidence := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "repo-1:work-00")
			driverEvidence := []string{evidence}
			if shape.noEvidence {
				driverEvidence = []string{}
			}
			drivers := []DriverJudgment{}
			if shape.citedClaims > 0 {
				cited := make([]string, 0, shape.citedClaims)
				for index := 0; index < shape.citedClaims; index++ {
					cited = append(cited, claims[index].ClaimID)
				}
				drivers = append(drivers, DriverJudgment{
					DriverID: "driver_status01", Standing: DriverPrincipal, Category: "status", Title: "Work item status",
					Summary: "The work items appear open.", AffectedSubjects: []SubjectRef{claims[0].Subject},
					EvidenceRefIDs: driverEvidence, ClaimedFactIDs: cited,
					Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved, Confidence: 0.9, Current: true,
				})
			}
			resultEvidence := []string{evidence}
			for index := 0; index < shape.evidenceMembers && index < len(served); index++ {
				if ref, ok := canonicalWorkItemEvidenceRef(SubjectRef{Kind: SubjectWorkItem, CanonicalID: served[index]}); ok && ref != evidence {
					resultEvidence = append(resultEvidence, ref)
				}
			}
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Work items of the project.", CurrentState: "Work items of the project.", DeterministicAnswer: "Work items of the project.", StrongestPressures: []string{}, Drivers: drivers, RemainingWork: budgetTrimFindings(shape.findings, ids), ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: resultEvidence, ClaimedFacts: claims, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}), Results: &staticResultStore{results: map[string]InvestigationResult{}}, Requirements: registryDeriver{},
	}, EngineOptions{ServiceVersion: "test", MaxItems: shape.maxItems, MaxSerializedBytes: budgetTrimMaxBytes(shape), SynthesisDeadlineReserve: shape.reserve, ServerCompletenessAuthorityEnabled: shape.symmetric, ServerCompletenessAuthoritySymmetricEnabled: shape.symmetric, NewResultID: func() string { return "result_budget_trim" }, Now: func() time.Time { return time.Unix(1000, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if shape.scoped {
		ctx = WithWorkItemCensusRepositoryScopeRecorder(ctx)
		RecordWorkItemCensusRepositoryScope(ctx)
	}
	if shape.deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, shape.deadline)
		defer cancel()
	}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	return result, err, syntheses
}

func budgetTrimServed(t *testing.T, shape budgetTrimShape) (InvestigationResult, int) {
	t.Helper()
	result, err, syntheses := budgetTrimInvestigate(t, shape)
	var refusal AnswerBudgetRefusal
	if errors.As(err, &refusal) {
		t.Fatalf("refused after %d syntheses: overrun=%s items=%d max=%d", syntheses, refusal.Overrun, refusal.MeasuredItems, refusal.MaxItems)
	}
	if err != nil {
		t.Fatal(err)
	}
	return result, syntheses
}

func budgetTrimTrimLines(result InvestigationResult) int {
	lines := 0
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricBudgetTrimLimitation(limitation) {
			lines++
		}
	}
	return lines
}

func TestBudgetTrimServesAnOverItemMembersAnswerWhenNoRetryIsPossible(t *testing.T) {
	result, syntheses := budgetTrimServed(t, budgetTrimProdShape)
	if syntheses != 1 {
		t.Fatalf("syntheses=%d want 1: no retry fits the deadline", syntheses)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 14 {
		t.Fatalf("members not kept: %+v", result.Cohort)
	}
	if got := budgetTrimTrimLines(result); got != 1 {
		t.Fatalf("trim limitation lines=%d want 1: %v", got, result.Limitations)
	}
	if result.Status != InvestigationPartial || result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete || !result.Coverage.Partial {
		t.Fatalf("status=%q completeness=%q coverage.partial=%v: a trimmed answer is partial", result.Status, result.Completeness.State, result.Coverage.Partial)
	}
	memberClaims, otherClaims := 0, 0
	for _, claim := range result.ClaimedFacts {
		if claim.Subject.Kind == SubjectWorkItem {
			memberClaims++
		} else {
			otherClaims++
		}
	}
	if memberClaims != 0 || otherClaims != 1 {
		t.Fatalf("member claims=%d other claims=%d: want the member claims cut and the census count kept", memberClaims, otherClaims)
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil || measurement.Items.Budgeted() > 30 {
		t.Fatalf("served %d items against 30: %v", measurement.Items.Budgeted(), err)
	}
	if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetTrimIsDeterministic(t *testing.T) {
	first, _ := budgetTrimServed(t, budgetTrimProdShape)
	second, _ := budgetTrimServed(t, budgetTrimProdShape)
	a, errA := json.Marshal(first)
	b, errB := json.Marshal(second)
	if errA != nil || errB != nil || !bytes.Equal(a, b) {
		at := 0
		for at < len(a) && at < len(b) && a[at] == b[at] {
			at++
		}
		lo := max(at-80, 0)
		t.Fatalf("two runs differ at byte %d: %q vs %q (%v %v)", at, a[lo:min(at+80, len(a))], b[lo:min(at+80, len(b))], errA, errB)
	}
}

func TestBudgetTrimStillRefusesWhenTheMinimumAnswerDoesNotFit(t *testing.T) {
	shape := budgetTrimProdShape
	shape.findings = 40
	_, err, _ := budgetTrimInvestigate(t, shape)
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("err=%v want an items budget refusal: 40 findings alone exceed the ceiling", err)
	}
}

func TestBudgetTrimLeavesAnAnswerThatFitsUntouched(t *testing.T) {
	shape := budgetTrimProdShape
	shape.claims = 5
	result, _ := budgetTrimServed(t, shape)
	if budgetTrimTrimLines(result) != 0 || result.Status != InvestigationComplete || result.Coverage.Partial {
		t.Fatalf("a fitting answer was changed: limitations=%v status=%q", result.Limitations, result.Status)
	}
	if len(result.ClaimedFacts) != 6 {
		t.Fatalf("claims=%d want 5 drafted plus the census count", len(result.ClaimedFacts))
	}
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "shortened") {
			t.Fatalf("unexpected limitation %q", limitation)
		}
	}
}

func TestBudgetTrimCutsCitedClaimsWhenTheUncitedOnesAreNotEnough(t *testing.T) {
	shape := budgetTrimProdShape
	shape.citedClaims = 36
	result, _ := budgetTrimServed(t, shape)
	if budgetTrimTrimLines(result) != 1 || len(result.Cohort.Members) != 14 {
		t.Fatalf("limitations=%v members=%d", result.Limitations, len(result.Cohort.Members))
	}
	if len(result.ClaimedFacts) != 2 {
		t.Fatalf("claims=%d want the one anchor claim the driver keeps plus the census count", len(result.ClaimedFacts))
	}
	for _, row := range result.Completeness.Outcomes {
		if row.Impact == contractsv1.ContextFabricAnswerImpactDepth && row.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems && (row.Served != 1 || row.Declared != 42) {
			t.Fatalf("outcome row counts member claims %d of %d, want 1 of 42 (the census count is not a member claim)", row.Served, row.Declared)
		}
	}
	ids := map[string]bool{}
	for _, claim := range result.ClaimedFacts {
		ids[claim.ClaimID] = true
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.ClaimedFactIDs {
			if !ids[id] {
				t.Fatalf("driver %s cites the cut claim %s", driver.DriverID, id)
			}
		}
	}
}

func TestBudgetTrimServesAfterARetryThatStillOverruns(t *testing.T) {
	shape := budgetTrimProdShape
	shape.deadline = 0
	result, syntheses := budgetTrimServed(t, shape)
	if syntheses != 2 {
		t.Fatalf("syntheses=%d want the retry to have run", syntheses)
	}
	if budgetTrimTrimLines(result) != 1 {
		t.Fatalf("limitations=%v", result.Limitations)
	}
}

func budgetTrimMaxBytes(shape budgetTrimShape) int64 {
	if shape.maxBytes > 0 {
		return shape.maxBytes
	}
	return 1 << 20
}

func TestBudgetTrimKeepsCitedClaimsWhenTheUncitedOnesAreEnough(t *testing.T) {
	shape := budgetTrimProdShape
	shape.citedClaims = 3
	result, _ := budgetTrimServed(t, shape)
	if budgetTrimTrimLines(result) != 1 {
		t.Fatalf("limitations=%v", result.Limitations)
	}
	kept := map[string]bool{}
	for _, claim := range result.ClaimedFacts {
		kept[claim.ClaimID] = true
	}
	for _, id := range []string{"claim_00", "claim_01", "claim_02"} {
		if !kept[id] {
			t.Fatalf("cited claim %s was cut while the uncited claims were enough: %v", id, kept)
		}
	}
	if len(result.ClaimedFacts) != 4 {
		t.Fatalf("claims=%d want the 3 cited plus the census count", len(result.ClaimedFacts))
	}
	for _, driver := range result.Drivers {
		if len(driver.ClaimedFactIDs) != 3 {
			t.Fatalf("driver citations were rewritten: %v", driver.ClaimedFactIDs)
		}
	}
}

func TestBudgetTrimDisclosesTheCutAsANarrowedDepthOutcome(t *testing.T) {
	result, _ := budgetTrimServed(t, budgetTrimProdShape)
	found := false
	for _, row := range result.Completeness.Outcomes {
		if row.Outcome == contractsv1.ContextFabricRequirementNarrowed && row.Impact == contractsv1.ContextFabricAnswerImpactDepth && row.CauseOverrun == contractsv1.ContextFabricBudgetOverrunItems && row.Served == 0 && row.Declared == 42 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no narrowed depth outcome row for the cut: %+v", result.Completeness.Outcomes)
	}
}

func TestBudgetTrimLeavesAByteOverrunToItsOwnRefusal(t *testing.T) {
	shape := budgetTrimProdShape
	shape.maxItems = 1000
	_, err, _ := budgetTrimInvestigate(t, shape)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	trimmedBytes := int64(0)
	{
		probe := budgetTrimProdShape
		result, _ := budgetTrimServed(t, probe)
		measurement, _ := contractsv1.MeasureContextFabricResponse(result)
		trimmedBytes = measurement.Bytes
	}
	shape.maxBytes = trimmedBytes + 200
	_, err, _ = budgetTrimInvestigate(t, shape)
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("err=%v want a bytes refusal: this lever only answers an items overrun", err)
	}
}

func TestBudgetTrimRefusesWhenTheTrimmedAnswerDoesNotValidate(t *testing.T) {
	shape := budgetTrimProdShape
	shape.citedClaims = 20
	shape.noEvidence = true
	_, err, _ := budgetTrimInvestigate(t, shape)
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err=%v want a refusal: a trimmed document that fails validation must not be served", err)
	}
}

func TestBudgetTrimServedUnderTheSymmetricAuthorityStaysPartialOrDegradedNeverComplete(t *testing.T) {
	shape := budgetTrimProdShape
	shape.symmetric = true
	shape.scoped = true
	result, _ := budgetTrimServed(t, shape)
	if result.Status == InvestigationComplete || result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("status=%q completeness=%q: the authority switch must not rewrite a trimmed answer to complete", result.Status, result.Completeness.State)
	}
	if budgetTrimTrimLines(result) != 1 {
		t.Fatalf("limitations=%v: the trim disclosure was lost", result.Limitations)
	}
	scope := 0
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) && limitation == contractsv1.ContextFabricWorkItemCensusRepositoryScopeLimitation {
			scope++
		}
	}
	if scope != 1 {
		t.Fatalf("repository scope limitation lines=%d", scope)
	}
}

func TestBudgetTrimServesAnAnswerTrimmedToExactlyTheCeilingWithTheScopeLimitation(t *testing.T) {
	shape := budgetTrimProdShape
	shape.symmetric = true
	shape.scoped = true
	shape.findings = 27
	shape.maxItems = 30
	result, _ := budgetTrimServed(t, shape)
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil || measurement.Items.Budgeted() != 30 || len(result.Cohort.Members) != 14 {
		t.Fatalf("items=%d members=%d err=%v: want 14 members served at exactly the ceiling", measurement.Items.Budgeted(), len(result.Cohort.Members), err)
	}
	if budgetTrimTrimLines(result) != 1 {
		t.Fatalf("limitations=%v", result.Limitations)
	}
}

// The lever decides the fit on the document finalizeServed will serve, so a
// byte ceiling that only the census repository-scope limitation breaks is
// refused here, on the axis the first measurement saw, and not served and then
// refused by the final assertion on bytes.
func TestBudgetTrimDecidesTheFitOnTheScopedServedDocument(t *testing.T) {
	plain, _ := budgetTrimServed(t, budgetTrimProdShape)
	measurement, err := contractsv1.MeasureContextFabricResponse(plain)
	if err != nil {
		t.Fatal(err)
	}
	shape := budgetTrimProdShape
	shape.scoped = true
	shape.maxBytes = measurement.Bytes + 20
	_, err, _ = budgetTrimInvestigate(t, shape)
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("err=%v refusal=%+v: want the items refusal the lever planned, not a byte refusal from the final assertion", err, refusal)
	}
}
