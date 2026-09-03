package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The two CHAOS-4736 repros, as tests.
//
// Both are RED at the parent (6b0cebd9) and GREEN here. At the parent they
// fail because the cohort kind came from a substring match over
// RequestedJudgment + SubjectTerms that returned `project` on
// "project"/"initiative", `team` on "team"/"group", and DEFAULTED to `team`
// -- so a grouped PROJECT question discovered a team cohort, and a
// repository question could not discover repositories at all.
//
// The red-on-parent runs are recorded in this lane's evidence directory
// (the parent's own signature differs by one return value, so the parent
// copy of these tests is necessarily a separate file, not this one).

// frameDiscovery builds a discovery request carrying a validated frame with
// the given subject expression. The INTERPRETATION deliberately carries
// prose that the deleted matcher would have keyed on, so that a regression
// reintroducing prose reading fails these tests rather than passing them:
// judgment and terms below say "team", and the frame says otherwise.
func frameDiscovery(expression contextfabric.SubjectExpression, judgment string, terms []string) contextfabric.GraphDiscoveryRequest {
	frame := contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: expression,
		Temporal:          contextfabric.TemporalIntentCurrent,
		Version:           contextfabric.QuestionFrameVersion,
	}
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: testRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape:             contextfabric.ShapeDiscoveredCohort,
			RequestedJudgment: judgment,
			SubjectTerms:      terms,
			TimeContext:       contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements:  []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		Frame:      &frame,
	}
	discovery.Request.Options.MaxCohortMembers = 10
	return discovery
}

func noInternal(contextfabric.SubjectRef) bool { return false }

// R1: "Show me each team's projects and their status" is a GROUPED question
// whose members are PROJECTS. The model emits judgment "status" (no kind
// keyword at all) and terms ["each team", "projects"]; the deleted matcher
// scanned those, hit "team" first, and returned a TEAM cohort, filtering
// every project node out. The frame says group_kind=team, member_kind=project.
func TestGroupedProjectQuestionDiscoversProjectsNotTeams(t *testing.T) {
	t.Parallel()
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionGroupedMembers,
		Grouped: &contextfabric.GroupedSetExpression{
			GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectProject,
		},
	}, "status", []string{"each team", "projects"})

	cohort, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
		candidateNode(contextfabric.SubjectProject, "project_a", "Project A", 0.9, "*"),
		candidateNode(contextfabric.SubjectProject, "project_b", "Project B", 0.9, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_1", "Team One", 0.9, "*"),
	}, noInternal)

	if basis != CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want %q", basis, CohortKindFromFrameMemberKind)
	}
	if cohort == nil {
		t.Fatal("cohort = nil, want a project cohort")
	}
	if cohort.Kind != contextfabric.SubjectProject {
		t.Fatalf("cohort.Kind = %q, want %q -- the members of a grouped team->project question are PROJECTS; a team cohort here is the deleted matcher's first keyword hit",
			cohort.Kind, contextfabric.SubjectProject)
	}
	// The group axis and the member kind must stay DISTINCT. When they
	// collapse, the engine clears the group axis (engine.go) and the plan
	// contract refuses the plan outright ("groups %q members by their own
	// kind") -- the downstream consequence the ticket traced.
	if len(cohort.Members) != 2 {
		t.Fatalf("cohort has %d members, want both projects", len(cohort.Members))
	}
	for _, member := range cohort.Members {
		if member.Subject.Kind != contextfabric.SubjectProject {
			t.Fatalf("member %q has kind %q, want every member to be a project", member.Subject.CanonicalID, member.Subject.Kind)
		}
	}
}

// R2: a REPOSITORY cohort was unreachable through the deleted matcher by
// construction -- it returned only `project` or `team`, and defaulted to
// `team`. This is the class observed live: "Show me open incidents per
// repository" served a 3-member TEAM cohort.
func TestRepositoryCohortIsReachable(t *testing.T) {
	t.Parallel()
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectRepository},
	}, "open_incidents", []string{"repositories"})

	cohort, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
		candidateNode(contextfabric.SubjectRepository, "repo_a", "Repo A", 0.9, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_1", "Team One", 0.9, "*"),
	}, noInternal)

	if basis != CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want %q", basis, CohortKindFromFrameMemberKind)
	}
	if cohort == nil {
		t.Fatal("cohort = nil, want a repository cohort")
	}
	if cohort.Kind != contextfabric.SubjectRepository {
		t.Fatalf("cohort.Kind = %q, want %q -- repository was unreachable through the deleted matcher and is the live defect this slice fixes",
			cohort.Kind, contextfabric.SubjectRepository)
	}
}

// THE DELETION'S OWN COST, asserted rather than assumed. There is no prose
// fallback: a turn with no validated frame discovers NOTHING and says why.
// If this ever returns a cohort, a guesser has been reintroduced.
func TestNoFrameDiscoversNothingAndNamesWhy(t *testing.T) {
	t.Parallel()
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
	}, "teams_under_pressure", []string{"teams"})
	discovery.Frame = nil

	cohort, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
		candidateNode(contextfabric.SubjectTeam, "team_1", "Team One", 0.9, "*"),
	}, noInternal)

	if cohort != nil {
		t.Fatalf("cohort = %#v, want nil -- prose said \"teams\", and reading it would be the deleted matcher returning", cohort)
	}
	if basis != CohortKindFrameAbsent {
		t.Fatalf("basis = %q, want %q -- an uncounted refusal is indistinguishable from an empty graph", basis, CohortKindFrameAbsent)
	}
}

// A named single subject is not a cohort, and an explicit set of NAMED
// operands has no member kind to discover -- its members come from subject
// resolution. Both refusals are named, not silent.
func TestNonCohortAndMemberlessExpressionsRefuseWithTheirOwnBasis(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		expression contextfabric.SubjectExpression
		want       CohortKindBasis
	}{
		{"named_subject", contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"platform"}},
		}, CohortKindNotACohortVariant},
		{"explicit_set_of_named_operands", contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"acr"}}},
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"ask-dev"}}},
			}},
		}, CohortKindNoMemberKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			discovery := frameDiscovery(tc.expression, "status", []string{"teams"})
			cohort, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
				candidateNode(contextfabric.SubjectTeam, "team_1", "Team One", 0.9, "*"),
			}, noInternal)
			if cohort != nil {
				t.Fatalf("cohort = %#v, want nil", cohort)
			}
			if basis != tc.want {
				t.Fatalf("basis = %q, want %q", basis, tc.want)
			}
		})
	}
}

// The basis vocabulary is closed and cohortKindFromFrame is TOTAL over it:
// every member is produced by some reachable input, so no member is a dead
// label and no input falls through unnamed. A tier with no positive fixture
// can be dead for its whole life and read as green.
func TestEveryCohortKindBasisIsReachable(t *testing.T) {
	t.Parallel()
	named := contextfabric.SubjectExpression{
		Kind:  contextfabric.SubjectExpressionNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{"platform"}},
	}
	explicit := contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionExplicitSet,
		Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
			{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"acr"}}},
		}},
	}
	discovered := contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
	}
	reached := map[CohortKindBasis]bool{}
	for _, frame := range []*contextfabric.QuestionFrame{
		nil,
		{SubjectExpression: named},
		{SubjectExpression: explicit},
		{SubjectExpression: discovered},
	} {
		_, basis := cohortKindFromFrame(frame)
		if !ValidCohortKindBasis(basis) {
			t.Fatalf("basis %q is outside the closed vocabulary", basis)
		}
		reached[basis] = true
	}
	for _, member := range CohortKindBasisVocabulary() {
		if !reached[member] {
			t.Errorf("no input in this test reaches basis %q; a basis nothing produces is a dead label", member)
		}
	}
}
