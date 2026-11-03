package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12885: "How is the Fullchaos team's investment split across its
// repositories over the last 90 days?" is a children_of_scope frame under a
// team anchor with the one goal allocate_investment. The CHAOS-12606 route
// answers it from the TEAM's own investment fact and reads no member set, so a
// requirement on each member repository has no population and lowered the
// answer to degraded (allocation_breakdown/member/repository, unavailable 0/0,
// read_population_unverified). The lead's ruling (option B): on this route the
// requirement is the anchor's own, allocation_breakdown/subject/team, the way
// the organization route's is allocation_breakdown/subject/organization.

const (
	scopeAllocationAnchorRequirement = "allocation_breakdown/subject/team"
	scopeAllocationMemberRequirement = "allocation_breakdown/member/repository"
)

// scopeAllocationCapabilities is the investment producer's allocation
// declaration for the two kinds this question touches: the team and its
// repositories both declare allocation_breakdown and a breakdown table, as
// devhealthfacts declares them.
func scopeAllocationCapabilities() []FactCapability {
	return []FactCapability{{
		Kind:                  FactInvestment,
		SupportedSubjectKinds: []SubjectKind{SubjectTeam, SubjectRepository},
		Obligations: map[SubjectKind][]AnswerObligation{
			SubjectTeam:       {ObligationAllocationBreakdown},
			SubjectRepository: {ObligationAllocationBreakdown},
		},
		Tables: map[SubjectKind][]FactTableShape{
			SubjectTeam:       {FactTableBreakdown},
			SubjectRepository: {FactTableBreakdown},
		},
	}}
}

// fullchaosChildrenFrame is the stored turn's frame, built from its fields: the
// repositories under the anchor term "Fullchaos", the one goal
// allocate_investment, and the obligation set it derived.
func fullchaosChildrenFrame(goals ...InvestigationGoal) QuestionFrame {
	if len(goals) == 0 {
		goals = []InvestigationGoal{GoalAllocateInvestment}
	}
	return QuestionFrame{
		Goals: goals,
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"Fullchaos"}, MemberKind: SubjectRepository},
		},
		Temporal:    TemporalIntentCurrent,
		Obligations: []AnswerObligation{ObligationEvidence, ObligationCoverage, ObligationAllocationBreakdown},
		Version:     QuestionFrameVersion,
	}
}

var fullchaosTeam = SubjectRef{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}

type scopeAllocationRun struct {
	// proposal is the interpreted frame; anchorKind the interpretation's scope
	// anchor kind.
	proposal   QuestionFrame
	anchorKind SubjectKind
}

// runScopeAllocationQuestion drives Engine.Investigate through the production
// interpreter, family route, planner, fact read and finalization for one
// children_of_scope allocation question. Only the model, the graph and the
// fact reader are controlled: retrieval commits the anchor team, discovery
// builds no member set, and the fact reader returns an available investment
// fact for every subject it is asked about.
func runScopeAllocationQuestion(t *testing.T, run scopeAllocationRun) (InvestigationResult, [][]SubjectRef) {
	t.Helper()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	proposal := run.proposal
	receipt.QuestionFrame = &proposal
	receipt.ScopeAnchorKind = run.anchorKind
	receipt.ScopeAnchorTerm = "Fullchaos"
	receipt.RequestedSubjectKind = SubjectRepository
	anchor := SubjectRef{Kind: run.anchorKind, CanonicalID: string(run.anchorKind) + ":fullchaos", Label: "Fullchaos"}
	graph := &organizationMemberGraph{retrievalRecordingGraph: retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{anchor}},
		bases:      provenCommitBases(anchor),
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}, committed: true}
	var read [][]SubjectRef
	readFacts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		read = append(read, append([]SubjectRef(nil), request.Subjects...))
		facts := []CanonicalFact{}
		for _, subject := range request.Subjects {
			facts = append(facts, CanonicalFact{Kind: FactInvestment, Subject: subject, Fields: map[string]FactValue{}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"})
		}
		return CanonicalFactBundle{
			Facts: facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
		}, nil
	})
	capabilities := scopeAllocationCapabilities()
	deriver := registryDeriver{capabilities: capabilities}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "How the team's investment is split across its repositories",
		TimeContext: TimeContext{Axis: TemporalCurrent}, SubjectTerms: []string{"Fullchaos"},
		FactRequirements: []FactRequirement{{Kind: FactInvestment}},
	}
	logs := captureEngineLogger(t)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: interpretation, receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   deriver,
		},
		Graph: graph,
		Facts: readFacts,
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Assessed.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Assessed.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &staticResultStore{results: map[string]InvestigationResult{}},
		Telemetry:    logs.telemetry,
		Requirements: deriver,
	}, EngineOptions{
		ServiceVersion:   "acr-test",
		Now:              func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:      func() string { return "result_scope_allocation_01" },
		FactCapabilities: capabilities,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_scope_allocation_01"
	request.Question = "How is the Fullchaos team's investment split across its repositories over the last 90 days?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_role"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, read
}

func planRequirementIdentities(result InvestigationResult) map[string]contractsv1.ContextFabricPlanRequirement {
	identities := map[string]contractsv1.ContextFabricPlanRequirement{}
	if result.AnswerPlan == nil {
		return identities
	}
	for _, requirement := range result.AnswerPlan.Requirements {
		identities[requirement.Requirement] = requirement
	}
	return identities
}

func scopeAllocationAssembledRow(result InvestigationResult, identity string) (RequirementOutcomeRow, bool) {
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == identity && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			return row, true
		}
	}
	return RequirementOutcomeRow{}, false
}

// THE DEFECT, through the real chain: interpret -> route -> derive -> plan ->
// read -> evaluate -> completeness. On the scope-anchor allocation route the
// plan derives the anchor's requirement, the team's own investment fact read
// satisfies it, and the answer is not lowered to degraded by a member
// requirement nothing reads.
func TestTheScopeAllocationRouteRequiresTheAnchorsAllocation(t *testing.T) {
	result, read := runScopeAllocationQuestion(t, scopeAllocationRun{proposal: fullchaosChildrenFrame(), anchorKind: SubjectTeam})
	t.Logf("served status=%s completeness=%s correction=%+v", result.Status, result.Completeness.State, result.StatusCorrection)

	// Premises: the route decided the family, and the read took the anchor alone.
	if result.AnswerPlan == nil || result.AnswerPlan.Family != QuestionFamilyInvestmentAllocation {
		t.Fatalf("plan %+v: the scope allocation route did not serve investment_allocation", result.AnswerPlan)
	}
	if len(read) == 0 || len(read[0]) != 1 || read[0][0].Kind != SubjectTeam {
		t.Fatalf("fact read subjects = %+v, want the anchor team alone", read)
	}

	planned := planRequirementIdentities(result)
	if _, found := planned[scopeAllocationMemberRequirement]; found {
		t.Fatalf("plan requirements %v carry %s: the route reads no member set", scopeAllocationKeys(planned), scopeAllocationMemberRequirement)
	}
	anchor, found := planned[scopeAllocationAnchorRequirement]
	if !found {
		t.Fatalf("plan requirements %v do not carry %s", scopeAllocationKeys(planned), scopeAllocationAnchorRequirement)
	}
	if anchor.Scope != string(CompletionScopeSingleSubject) || anchor.Quantifier != string(CompletionQuantifierAtLeastOne) ||
		len(anchor.FactKinds) != 1 || anchor.FactKinds[0] != FactInvestment {
		t.Fatalf("anchor requirement %+v, want single_subject at_least_one over investment", anchor)
	}

	row, evaluated := scopeAllocationAssembledRow(result, scopeAllocationAnchorRequirement)
	if !evaluated {
		t.Fatalf("no assembled-result row for %s in %+v", scopeAllocationAnchorRequirement, result.Completeness.Outcomes)
	}
	assertRow(t, row, contractsv1.ContextFabricRequirementSatisfied, contractsv1.ContextFabricAnswerImpactNone, "", false, 1, 1)
	if result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessDegraded || result.Status == InvestigationDegraded {
		t.Fatalf("status/completeness = %q/%q: the team's own fact was read, so nothing degrades the answer", result.Status, result.Completeness.State)
	}
	if result.StatusCorrection != nil && result.StatusCorrection.DecidingRequirement == scopeAllocationMemberRequirement {
		t.Fatalf("status correction %+v is decided by the member requirement", result.StatusCorrection)
	}
}

// The route's exception is the route's alone (check 10). A children_of_scope
// allocation frame the route does not serve -- a second goal, or a project
// anchor -- keeps the member requirement it has always derived.
func TestAChildrenAllocationFrameOffTheRouteKeepsTheMemberRequirement(t *testing.T) {
	for name, run := range map[string]scopeAllocationRun{
		"a second goal":    {proposal: fullchaosChildrenFrame(GoalAllocateInvestment, GoalAssessState), anchorKind: SubjectTeam},
		"a project anchor": {proposal: fullchaosChildrenFrame(), anchorKind: contractsv1.ContextFabricSubjectProject},
	} {
		t.Run(name, func(t *testing.T) {
			result, _ := runScopeAllocationQuestion(t, run)
			if result.AnswerPlan == nil {
				t.Fatalf("status %q: no plan", result.Status)
			}
			planned := planRequirementIdentities(result)
			if _, found := planned[scopeAllocationMemberRequirement]; !found {
				t.Fatalf("plan requirements %v lost %s off the route", scopeAllocationKeys(planned), scopeAllocationMemberRequirement)
			}
			if _, found := planned[scopeAllocationAnchorRequirement]; found {
				t.Fatalf("plan requirements %v carry %s off the route", scopeAllocationKeys(planned), scopeAllocationAnchorRequirement)
			}
		})
	}
}

// The evaluator, both ways, over the read this route makes: the team's own
// investment fact and no cohort. A member requirement has no population to
// count and is unavailable 0/0 read_population_unverified, which is the stored
// turn's row and what made it degraded. The anchor's single_subject
// requirement counts the team's fact and is satisfied.
func TestTheTeamsOwnFactSatisfiesTheAnchorRequirementAndNotAMemberOne(t *testing.T) {
	t.Parallel()
	coverage := factCoverage(FactInvestment, SourceAvailable)
	facts := factsFor(fullchaosTeam, kindList(FactInvestment))
	allocation := func(scope CompletionScope, kind SubjectKind, role SubjectRole) contractsv1.ContextFabricPlanRequirement {
		requirement := scopeRequirement(scope, kind, role, CompletionQuantifierAtLeastOne, FactInvestment)
		requirement.Obligation = string(ObligationAllocationBreakdown)
		requirement.Requirement = string(ObligationAllocationBreakdown) + "/" + string(role) + "/" + string(kind)
		return requirement
	}

	t.Run("member requirement, no cohort", func(t *testing.T) {
		t.Parallel()
		member := allocation(CompletionScopeEachMember, SubjectRepository, SubjectRoleMember)
		rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{member}, nil, coverage, facts)
		assertRow(t, rowFor(t, rows, scopeAllocationMemberRequirement),
			contractsv1.ContextFabricRequirementUnavailable, contractsv1.ContextFabricAnswerImpactDimension,
			contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, true, 0, 0)
		if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessDegraded {
			t.Fatalf("answer state = %q, want degraded", state)
		}
	})

	t.Run("anchor requirement, the team's fact", func(t *testing.T) {
		t.Parallel()
		anchor := allocation(CompletionScopeSingleSubject, SubjectTeam, SubjectRoleSubject)
		rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{anchor}, nil, coverage, facts)
		assertRow(t, rowFor(t, rows, scopeAllocationAnchorRequirement),
			contractsv1.ContextFabricRequirementSatisfied, contractsv1.ContextFabricAnswerImpactNone, "", false, 1, 1)
		if state := contractsv1.DeriveContextFabricAnswerCompletenessState(rows); state != contractsv1.ContextFabricAnswerCompletenessComplete {
			t.Fatalf("answer state = %q, want complete", state)
		}
	})

	t.Run("anchor requirement, no fact read", func(t *testing.T) {
		t.Parallel()
		anchor := allocation(CompletionScopeSingleSubject, SubjectTeam, SubjectRoleSubject)
		rows := evaluateCohort([]contractsv1.ContextFabricPlanRequirement{anchor}, nil, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, CanonicalFactBundle{})
		if row := rowFor(t, rows, scopeAllocationAnchorRequirement); row.Outcome == contractsv1.ContextFabricRequirementSatisfied {
			t.Fatalf("row %+v: nothing was read, so the anchor requirement is not satisfied", row)
		}
	})
}

func scopeAllocationKeys(identities map[string]contractsv1.ContextFabricPlanRequirement) []string {
	keys := make([]string, 0, len(identities))
	for key := range identities {
		keys = append(keys, key)
	}
	return keys
}
