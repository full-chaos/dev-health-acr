package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This proof calls the real engine. Its ports record which phase the engine
// selects; they do not produce a preassembled tuple result.
func TestWorkItemFreshDispatchSelectsMembershipBeforeDiscovery(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
	payload := workItemTuplePayloadFixture(t)
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	membershipReads, factReads := 0, 0
	stop := errors.New("membership phase observed")
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}, outcome: outcome},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			membershipReads++
			return nil, WorkItemMembershipResult{}, stop
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			factReads++
			return CanonicalFactBundle{}, stop
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, stop
		}),
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_tuple_fresh_001" }})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	if graph.resolveCalls != 1 || membershipReads != 1 || graph.discoverCalls != 0 || factReads != 0 {
		t.Fatalf("phase counts resolve=%d membership=%d discover=%d facts=%d; want 1,1,0,0", graph.resolveCalls, membershipReads, graph.discoverCalls, factReads)
	}
}

type dispatchGraphProbe struct {
	graphReaderStub
	resolveCalls, discoverCalls int
}

func (g *dispatchGraphProbe) ResolveSubjects(ctx context.Context, p storage.Principal, r InvestigationRequest, q InterpretedQuestion, b ResolvedGraphBinding, k *ConfirmedExpectedKind, a *ConfirmedAnchorSelection, f *QuestionFrame, s SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolveCalls++
	return g.graphReaderStub.ResolveSubjects(ctx, p, r, q, b, k, a, f, s)
}
func (g *dispatchGraphProbe) DiscoverContext(ctx context.Context, p storage.Principal, r GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverCalls++
	return g.graphReaderStub.DiscoverContext(ctx, p, r)
}

func TestWorkItemFreshDispatchMeasuredAndUnmeasured(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, name := range []string{"members", "zero", "unmeasured", "floor"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), nil, "").Frame
			payload := workItemTuplePayloadFixture(t)
			graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
			gate, _ := NewWorkItemMembershipGate(1, 0)
			calls := 0
			store := &staticResultStore{results: map[string]InvestigationResult{}}
			population := 1
			if name == "zero" {
				population = 0
			}
			if name == "floor" {
				population = WorkItemMembershipCensusLimit + 1
			}
			engine, err := NewEngine(EngineDependencies{
				Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}},
				Graph:       graph,
				CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
					return true, ""
				},
				WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
					lease, err := gate.Acquire(ctx)
					m := WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: population}}
					if population > 0 {
						m.Members = []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}}
					}
					if name == "unmeasured" {
						m = WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusUnmeasured}}
					}
					if name == "floor" {
						m.Census.State = WorkItemMembershipCensusFloor
					}
					return lease, m, err
				}),
				Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, r CanonicalFactRequest) (CanonicalFactBundle, error) {
					calls++
					if len(r.Subjects) != 1 || r.Subjects[0].Kind != SubjectWorkItem {
						t.Errorf("fact subjects=%+v", r.Subjects)
					}
					if len(r.Requirements) != 2 || r.Requirements[0].Kind != FactStatus || r.Requirements[1].Kind != FactWork {
						t.Errorf("requirements=%+v", r.Requirements)
					}
					for _, req := range r.Requirements {
						if len(req.Subjects) != 1 || req.Subjects[0].CanonicalID != r.Subjects[0].CanonicalID {
							t.Errorf("requirement subjects=%+v", req.Subjects)
						}
					}
					return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
				}),
				Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
					if input.Graph.Cohort != nil {
						wantComplete := name == "members" || name == "zero"
						if input.Graph.Cohort.Complete != wantComplete || input.Graph.Cohort.Truncated != (name == "floor") {
							t.Errorf("synthesis cohort complete=%v truncated=%v for %s", input.Graph.Cohort.Complete, input.Graph.Cohort.Truncated, name)
						}
						for _, m := range input.Graph.Cohort.Members {
							if m.RankingComputed {
								t.Error("membership was ranked")
							}
						}
					}
					return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
				}), Results: store, Requirements: registryDeriver{},
			}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_tuple_fresh_002" }})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
			if err != nil {
				t.Fatal(err)
			}
			if graph.resolveCalls != 1 || graph.discoverCalls != 0 {
				t.Errorf("resolve=%d discover=%d", graph.resolveCalls, graph.discoverCalls)
			}
			wantCalls := 1
			if name == "zero" || name == "unmeasured" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Errorf("fact calls=%d want=%d", calls, wantCalls)
			}
			if name == "unmeasured" {
				if result.Cohort != nil {
					t.Errorf("unmeasured cohort=%+v", result.Cohort)
				}
			} else if result.Cohort == nil {
				t.Fatal("measured census lost cohort")
			}
			if name == "zero" && len(result.Cohort.Members) != 0 {
				t.Error("measured zero has members")
			}
			if store.savedSemantic == nil || store.savedSemantic.State == nil || store.savedSemantic.State.WorkItemCensus == nil {
				t.Fatalf("saved census unavailable: %+v", store.savedSemantic)
			}
			census := store.savedSemantic.State.WorkItemCensus
			wantState := WorkItemMembershipCensusExact
			if name == "floor" {
				wantState = WorkItemMembershipCensusFloor
			}
			if name == "unmeasured" {
				wantState = WorkItemMembershipCensusUnmeasured
			}
			if census.State != wantState {
				t.Errorf("census=%+v", census)
			}
			counts := 0
			for _, claim := range result.ClaimedFacts {
				if claim.Kind == contractsv1.ContextFabricFactCardinality {
					counts++
					if claim.Value.Integer == nil || *claim.Value.Integer != int64(census.Value) {
						t.Errorf("cardinality=%+v want=%d", claim.Value, census.Value)
					}
				}
			}
			wantCounts := 1
			if name == "unmeasured" {
				wantCounts = 0
			}
			if counts != wantCounts {
				t.Errorf("count claims=%d want=%d", counts, wantCounts)
			}
			if name != "unmeasured" {
				count := population
				if name == "floor" {
					count = WorkItemMembershipCensusLimit
				}
				noun := "work items"
				if count == 1 {
					noun = "work item"
				}
				prefix := "Counted"
				if name == "floor" {
					prefix = "Counted at least"
				}
				if !strings.Contains(result.DeterministicAnswer, fmt.Sprintf("%s %d %s.", prefix, count, noun)) {
					t.Errorf("count display did not preserve measured cardinality: %s", result.DeterministicAnswer)
				}
			}
			if name == "floor" {
				found := false
				for _, detail := range result.Coverage.Details {
					if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && detail.Kind == SubjectWorkItem {
						found = true
						if detail.Declared == nil || *detail.Declared != 2000 || detail.Served == nil || *detail.Served != 1 {
							t.Errorf("floor detail=%+v", detail)
						}
					}
				}
				if !found {
					t.Error("floor census lost D47")
				}
			}
			if err := ValidateWorkItemTuplePayload(result, storage.Principal{OrgID: "org-1"}); err != nil {
				t.Fatal(err)
			}
			next, err := gate.Acquire(context.Background())
			if err != nil {
				t.Fatalf("response lease retained after direct return: %v", err)
			}
			next.Release()
		})
	}
}

func TestWorkItemFreshAdmissionRefusesBeforeResolution(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, name := range []string{"family", "frame_axis", "time_axis", "qualifier", "unknown_qualifier", "goals", "mixed_goals", "invalid", "unrelated_refusal", "plurality"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState), nil, "").Frame
			question := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}
			outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Frame: &frame, FrameObligations: frame.Obligations, Gate: FrameGate{Outcome: FrameGatePassed}, WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
			switch name {
			case "family":
				outcome.Family = QuestionFamilyDiscoveredCohortRanking
			case "frame_axis":
				frame.Temporal = TemporalIntentBoundedWindow
			case "time_axis":
				question.TimeContext.Axis = TemporalValidTime
			case "qualifier":
				frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierStatus
			case "unknown_qualifier":
				frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierUnrecognized
			case "goals":
				frame.Goals = []InvestigationGoal{GoalExplainDrivers}
			case "mixed_goals":
				frame.Goals = append(frame.Goals, GoalExplainDrivers)
			case "invalid":
				outcome.Gate = FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariantI6}
			case "unrelated_refusal":
				outcome.Gate = FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectDocument}
			case "plurality":
				outcome.Family = QuestionFamilyUnclassified
				outcome.Source = QuestionFamilySourcePluralityRejected
			}
			graph := &dispatchGraphProbe{}
			calls := 0
			engine, err := NewEngine(EngineDependencies{Interpreter: familyInterpreter{interpreted: question, outcome: outcome}, Graph: graph,
				WorkItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
					calls++
					return nil, WorkItemMembershipResult{}, nil
				}),
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					calls++
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					calls++
					return InvestigationResult{}, nil
				}),
			}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_tuple_refused_001" }})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
			if graph.resolveCalls != 0 || graph.discoverCalls != 0 || calls != 0 {
				t.Errorf("refused tuple performed I/O: resolve=%d discover=%d member/fact/synth=%d", graph.resolveCalls, graph.discoverCalls, calls)
			}
		})
	}
}

func TestWorkItemTupleFactRequestFailsClosedForEmptyAndForeignSubjects(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	payload := workItemTuplePayloadFixture(t)
	for _, name := range []string{"valid", "empty_roots", "empty_cohort", "empty_requirements", "empty_status_subjects", "empty_work_subjects", "foreign_root", "foreign_requirement", "ranking_kind"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			subjects := workItemTupleSubjects(payload.Cohort)
			request := CanonicalFactRequest{workItemTuple: true, Subjects: subjects, Cohort: payload.Cohort, Requirements: workItemTupleFactRequirements(subjects), Question: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}}
			switch name {
			case "empty_roots":
				request.Subjects = nil
			case "empty_cohort":
				request.Cohort = nil
			case "empty_requirements":
				request.Requirements = nil
			case "empty_status_subjects":
				request.Requirements[0].Subjects = nil
			case "empty_work_subjects":
				request.Requirements[1].Subjects = nil
			case "foreign_root":
				request.Subjects = []SubjectRef{payload.SubjectResolution.Committed[0]}
			case "foreign_requirement":
				request.Requirements[0].Subjects = []SubjectRef{payload.SubjectResolution.Committed[0]}
			case "ranking_kind":
				request.Requirements[0].Kind = FactHealth
			}
			err := validateCanonicalFactRequest(request)
			if (err == nil) != (name == "valid") {
				t.Errorf("validation error=%v", err)
			}
		})
	}
}

func TestWorkItemFreshMembershipCapsAreLexicalAndCensusIndependent(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, caps := range []struct{ plan, request, want int }{{0, 0, 200}, {201, 240, 200}, {7, 240, 7}, {240, 9, 9}, {7, 9, 7}, {9, 7, 7}} {
		t.Run(fmt.Sprintf("%d_%d", caps.plan, caps.request), func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			gate, _ := NewWorkItemMembershipGate(1, 0)
			members := make([]WorkItemMembershipMember, 0, 205)
			for i := 204; i >= 0; i-- {
				id, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", fmt.Sprintf("work-%03d", i)}, nil)
				if err != nil || omitted {
					t.Fatal(err)
				}
				members = append(members, WorkItemMembershipMember{CanonicalID: id, WorkItemID: fmt.Sprintf("work-%03d", i)})
			}
			engine := &Engine{workItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, request WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
				if request.PlanMaxMembers != caps.plan || request.RequestMaxMembers != caps.request {
					t.Errorf("port caps=%+v", request)
				}
				lease, err := gate.Acquire(ctx)
				return lease, WorkItemMembershipResult{Members: members, Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 205}}, err
			})}
			ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
			defer owner.Complete()
			resolution := workItemTuplePayloadFixture(t).SubjectResolution
			graph, census, err := engine.discoverWorkItemTuple(ctx, storage.Principal{OrgID: "org-1"}, InvestigationRequest{Options: InvestigationOptions{MaxCohortMembers: caps.request}}, resolution, &AnswerPlan{Budget: AnswerPlanBudget{MaxMembers: caps.plan}})
			if err != nil {
				t.Fatal(err)
			}
			if len(graph.Cohort.Members) != caps.want || census.Value != 205 || census.Retained != caps.want {
				t.Fatalf("members=%d census=%+v", len(graph.Cohort.Members), census)
			}
			for i, m := range graph.Cohort.Members {
				if m.Subject.CanonicalID != members[len(members)-i-1].CanonicalID {
					t.Errorf("member[%d]=%s is not lexical", i, m.Subject.CanonicalID)
				}
			}
			if graph.Cohort.Complete || !graph.Cohort.Truncated {
				t.Error("bounded cohort claims full population")
			}
			if _, err := gate.Acquire(context.Background()); !errors.Is(err, ErrWorkItemMembershipQueueFull) {
				t.Errorf("borrowed response owner lost lease: %v", err)
			}
			owner.Complete()
			lease, err := gate.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			lease.Release()
		})
	}
}

func TestWorkItemFreshRetryDoesNotRankOrChangeCensus(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	payload := workItemTuplePayloadFixture(t)
	member2 := payload.Cohort.Members[0]
	member2.Subject.CanonicalID, _, _ = identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-2"}, nil)
	member2.Rank = 2
	payload.Cohort.Members = append(payload.Cohort.Members, member2)
	census := &WorkItemTupleCensus{State: WorkItemMembershipCensusFloor, Value: 2000, Retained: 2}
	params := synthesisAssemblyParams{WorkItemCensus: census, Graph: GraphContext{Cohort: payload.Cohort, CohortPopulation: 2000}, Resolution: payload.SubjectResolution}
	narrow := narrowSynthesisInput(params, &AnswerPlan{})
	if !narrow.Narrow || narrow.After != 1 {
		t.Fatalf("retry did not narrow: %+v", narrow)
	}
	if narrow.Graph.Cohort.Members[0].RankingComputed || !reflect.DeepEqual(narrow.Ranked, CohortRankedEvent{}) {
		t.Error("retry ranked tuple members")
	}
	final := finalWorkItemTupleCensus(census, narrow.Graph.Cohort)
	if final.Retained != 1 || final.Value != 2000 || census.Retained != 2 {
		t.Errorf("final census=%+v source=%+v", final, census)
	}
}

func TestWorkItemFreshAnchorRequiresOneLiveAuthorizedProject(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, name := range []string{"authorized", "missing_verifier", "denied", "cancelled", "unresolved", "multiple", "team", "organization"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState), nil, "").Frame
			question := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}
			outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), question.TimeContext), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
			resolution := workItemTuplePayloadFixture(t).SubjectResolution
			anchor := resolution.Committed[0]
			switch name {
			case "unresolved":
				resolution.Committed = []SubjectRef{}
			case "multiple":
				resolution.Committed = append(resolution.Committed, anchor)
			case "team":
				resolution.Committed[0].Kind = SubjectTeam
			case "organization":
				resolution.Committed[0].Kind = SubjectOrganization
			}
			graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases(resolution.Committed...)}}
			request := validInvestigationRequestWithConfirmedWindow()
			request.RequestedScope.RepositorySlugs = []string{"org/repo-b", "org/repo-a"}
			principal := acceptancePrincipal()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			verifications, membership, facts := 0, 0, 0
			verifier := CandidateVerifier(func(_ context.Context, got storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind SubjectKind, id string) (bool, CandidateVerificationReason) {
				verifications++
				if !reflect.DeepEqual(got, principal) || !reflect.DeepEqual(scope, request.RequestedScope) || kind != SubjectProject || id != anchor.CanonicalID || binding == (ResolvedGraphBinding{}) {
					t.Errorf("live verification inputs changed: principal=%+v scope=%+v binding=%+v kind=%s id=%s", got, scope, binding, kind, id)
				}
				if name == "cancelled" {
					cancel()
				}
				return name != "denied", ""
			})
			if name == "missing_verifier" {
				verifier = nil
			}
			engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: familyInterpreter{interpreted: question, outcome: outcome}, Graph: graph, CandidateVerifier: verifier,
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return InvestigationResult{}, errors.New("controlled stop after unmeasured census")
				}),
				WorkItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
					membership++
					return nil, WorkItemMembershipResult{}, nil
				}),
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					facts++
					return CanonicalFactBundle{}, nil
				}),
			})
			result, investigateErr := engine.Investigate(ctx, principal, request)
			wantVerify, wantMembership := 0, 0
			if name == "authorized" || name == "denied" || name == "cancelled" {
				wantVerify = 1
			}
			if name == "authorized" {
				wantMembership = 1
			}
			if graph.resolveCalls != 1 || graph.discoverCalls != 0 || verifications != wantVerify || membership != wantMembership || facts != 0 {
				t.Errorf("phase calls Resolve=%d Discover=%d C=%d S1=%d facts=%d; want 1/0/%d/%d/0; error=%v refusal=%s", graph.resolveCalls, graph.discoverCalls, verifications, membership, facts, wantVerify, wantMembership, investigateErr, result.RefusalBasis)
			}
		})
	}
}
