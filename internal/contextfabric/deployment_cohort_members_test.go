package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func deploymentScopedFrame(goals ...InvestigationGoal) QuestionFrame {
	return frameWith(goals, scopedExpression(SubjectDeployment), TemporalIntentCurrent, nil)
}

func TestDeploymentMembersAreServableOnlyAsTheMembersOfANamedAnchor(t *testing.T) {
	t.Parallel()
	scoped := scopedExpression(SubjectDeployment)
	servable, declared, reason := CohortMemberKindFor(scoped)
	if reason != CohortDiscoverable || servable != SubjectDeployment || declared != SubjectDeployment {
		t.Fatalf("children_of_scope deployment = (%q, %q, %q), want discoverable", servable, declared, reason)
	}
	for name, expression := range map[string]SubjectExpression{
		"discovered_kind": {Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: SubjectDeployment}},
		"grouped_members": groupedExpression(SubjectDeployment, SubjectRepository),
	} {
		if _, _, reason := CohortMemberKindFor(expression); reason != CohortMemberKindUnservable {
			t.Errorf("%s over deployment reason = %q, want %q: only the members of a named anchor are proven", name, reason, CohortMemberKindUnservable)
		}
	}
	deploymentKind := SubjectDeployment
	organization := frameWith([]InvestigationGoal{GoalCountOrAggregate}, SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{MemberKind: &deploymentKind}}, TemporalIntentCurrent, nil)
	if CohortMemberSetResolvableForFrame(organization) {
		t.Error("a counted organization_scope over deployment must stay refused")
	}
	if servableCohortKinds[SubjectDeployment] {
		t.Error("deployment must not join the unconditional allow-list")
	}
}

func TestDeploymentCohortAnchorServableIsRepositoryOnly(t *testing.T) {
	t.Parallel()
	for _, kind := range []SubjectKind{SubjectProject, SubjectTeam, SubjectOrganization, SubjectDeployment, SubjectPullRequest, SubjectWorkItem, SubjectIncident} {
		if DeploymentCohortAnchorServable(kind) {
			t.Errorf("anchor %q must not anchor a deployment cohort", kind)
		}
	}
	if !DeploymentCohortAnchorServable(SubjectRepository) {
		t.Error("a repository anchors a deployment cohort")
	}
	repo := []SubjectRef{{Kind: SubjectRepository, CanonicalID: "a"}}
	if !deploymentCohortAnchorsServable(repo, "") || !deploymentCohortAnchorsServable(repo, SubjectRepository) {
		t.Error("a committed repository with no declared anchor kind, or a declared repository, serves")
	}
	for _, declared := range []SubjectKind{SubjectTeam, SubjectProject} {
		if deploymentCohortAnchorsServable(repo, declared) {
			t.Errorf("a committed repository under a declared %q anchor must not serve (a hint committed it, the question did not name it)", declared)
		}
	}
	if deploymentCohortAnchorsServable(nil, "") || deploymentCohortAnchorsServable([]SubjectRef{{Kind: SubjectRepository, CanonicalID: "a"}, {Kind: SubjectRepository, CanonicalID: "b"}}, "") {
		t.Error("zero or several committed anchors must not serve")
	}
}

func TestDeploymentCohortEngineDiscoversOnlyUnderARepositoryAnchor(t *testing.T) {
	for _, tc := range []struct {
		name         string
		anchorKind   SubjectKind
		wantDiscover int
	}{
		{"repository anchor discovers", SubjectRepository, 1},
		{"project anchor refused before discovery", SubjectProject, 0},
		{"team anchor refused before discovery", SubjectTeam, 0},
		{"repository committed under a declared team anchor refused", SubjectRepository, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaredKind := tc.anchorKind
			if tc.name == "repository committed under a declared team anchor refused" {
				declaredKind = SubjectTeam
			}
			frame := deploymentScopedFrame(GoalAssessState)
			gate := DecideFrameGate(ValidateFrame(frame, nil, ""), true)
			if gate.Refuses() {
				t.Fatalf("fixture gate refuses: %+v", gate)
			}
			outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: gate, WinningSample: FamilySample{ScopeAnchorKind: declaredKind, ScopeAnchorTerm: "Anchor"}}
			payload := workItemTuplePayloadFixture(t)
			resolution := payload.SubjectResolution
			resolution.Committed = []SubjectRef{{Kind: tc.anchorKind, CanonicalID: "anchor-1", Label: "Anchor"}}
			resolution.Candidates = []SubjectCandidate{{Subject: resolution.Committed[0]}}
			graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases(resolution.Committed...)}}
			stop := errors.New("stop after discovery")
			engine, err := NewEngine(EngineDependencies{
				Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "deployments", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactDeployments}}}, outcome: outcome},
				Graph:       graph,
				CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
					return true, ""
				},
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, stop
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return InvestigationResult{}, stop
				}),
			}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_deployment_cohort_001" }})
			if err != nil {
				t.Fatal(err)
			}
			result, investigateErr := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
			if tc.wantDiscover == 0 {
				if investigateErr != nil {
					t.Fatalf("a refused anchor must end on a clean terminal refusal, got error %v", investigateErr)
				}
				if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
					t.Fatalf("refusal basis = %q, want member_kind_unservable", result.RefusalBasis)
				}
			}
			if graph.discoverCalls != tc.wantDiscover {
				t.Fatalf("discover calls = %d, want %d", graph.discoverCalls, tc.wantDiscover)
			}
			if tc.wantDiscover == 0 && (len(result.SubjectResolution.Committed) != 0 || len(result.SubjectResolution.Candidates) != 0) {
				t.Fatalf("a refused anchor must not stay committed or listed as a candidate: %+v", result.SubjectResolution)
			}
		})
	}
}

func TestDeploymentCountSentenceNeverReadsExactWhenThePopulationIsIncomplete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   MembershipCardinality
		want string
	}{
		{"complete exact", MembershipCardinality{Resolved: true, Kind: SubjectDeployment, Served: 3, Declared: 3}, "Counted 3 deployments."},
		{"capped", MembershipCardinality{Resolved: true, Kind: SubjectDeployment, Served: 3, Declared: 7, PopulationIncomplete: true}, "Counted 3 deployments of at least 7 found."},
		{"truncated pool", MembershipCardinality{Resolved: true, Kind: SubjectDeployment, Served: 2, Declared: 2, PopulationIncomplete: true}, "Counted at least 2 deployments."},
		{"other kinds unchanged", MembershipCardinality{Resolved: true, Kind: SubjectIncident, Served: 3, Declared: 7, PopulationIncomplete: true}, "Counted 3 incidents of 7 found."},
	} {
		if got := cardinalityAnswerSentence(tc.in); got != tc.want {
			t.Errorf("%s: sentence = %q, want %q", tc.name, got, tc.want)
		}
	}
}
