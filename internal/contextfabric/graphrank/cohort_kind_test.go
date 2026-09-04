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

	cohort, _, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
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

// R2 (the ticket's second repro): HALF-FIXED, and the half that remains is
// NOT this slice's to fix. Stating that here rather than asserting a fix that
// does not exist.
//
// The ticket says a repository cohort was unreachable because the prose
// matcher returned only project-or-team and defaulted to team. That barrier
// is gone: the frame declares `repository` and this function reads it. But a
// SECOND, independent barrier was underneath it the whole time and could
// never be observed while the first one stood -- contracts/v1's
// ContextFabricCohort.validate admits exactly {team, project} and refuses
// everything else with "cohort violates v1 bounds". Removing the matcher made
// that bound reachable for the first time, and a repository question that
// used to return a wrong-kind cohort returned an HTTP 500 on the rig instead.
//
// The assertion INVERTED when the arm was proven, and the property it
// protects did not: a repository question resolves to a REPOSITORY cohort. It
// used to be refused by a named contract limitation; before that it was
// silently rewritten to `team`. The constant across all three states is that
// it is never answered as some other kind, and that is what this asserts --
// including the team decoy sitting in the same node list, which differs from
// the repository node in SUBJECT KIND ALONE.
func TestRepositoryCohortIsDiscoveredAndNeverRewrittenToTeam(t *testing.T) {
	t.Parallel()
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectRepository},
	}, "open_incidents", []string{"repositories"})

	cohort, _, _, declared, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
		candidateNode(contextfabric.SubjectRepository, "repo_a", "Repo A", 0.9, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_1", "Team One", 0.9, "*"),
	}, noInternal)

	if basis != CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want %q", basis, CohortKindFromFrameMemberKind)
	}
	if declared != contextfabric.SubjectRepository {
		t.Fatalf("declared kind = %q, want %q", declared, contextfabric.SubjectRepository)
	}
	if cohort == nil {
		t.Fatal("no cohort was built for a repository question that has candidate repository nodes")
	}
	if cohort.Kind != contextfabric.SubjectRepository {
		t.Fatalf("cohort kind = %q, want %q -- a repository question must not resolve to some other kind", cohort.Kind, contextfabric.SubjectRepository)
	}
	if len(cohort.Members) != 1 || cohort.Members[0].Subject.CanonicalID != "repo_a" {
		t.Fatalf("members = %+v, want exactly the repository node; the team node differs in kind alone and must be excluded by the kind filter, not by anything else", cohort.Members)
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

	cohort, _, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
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
			cohort, _, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery, []CandidateNode{
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

// A frame declaring a cohort kind the WIRE CONTRACT cannot carry must refuse
// here, not build a document the validator will reject.
//
// THIS TEST EXISTS BECAUSE THE RIG FOUND IT. contracts/v1 permits exactly two
// cohort kinds, team and project; the deleted prose matcher could only return
// those two, so the bound was unreachable until the frame's declared kind
// started reaching Cohort.Kind. A repository question then produced an HTTP
// 500 ("cohort violates v1 bounds") instead of an answer -- strictly worse
// than the wrong-kind cohort it replaced. Refusing early turns that crash
// into a counted, named limitation.
func TestUnservableCohortKindRefusesInsteadOfBuildingAnInvalidCohort(t *testing.T) {
	t.Parallel()
	// `repository` LEFT this list in the change that proved its arm.
	// `incident` is here on its own merit and not as a placeholder: it is
	// the member kind "open incidents per repository" actually declares
	// (invariant I6 forbids a grouped expression from grouping a kind by
	// itself, so the repository noun in that question is the GROUPING AXIS),
	// and it has no candidate pool at all -- the exact-name census fetches
	// repository, project and team. Serving it is an incident-cohort arm.
	for _, kind := range []contextfabric.SubjectKind{
		contextfabric.SubjectIncident, contextfabric.SubjectWorkItem,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			discovery := frameDiscovery(contextfabric.SubjectExpression{
				Kind:       contextfabric.SubjectExpressionDiscoveredKind,
				Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
			}, "status", []string{"things"})
			cohort, _, _, declared, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery,
				[]CandidateNode{candidateNode(kind, "node_a", "Node A", 0.9, "*")}, noInternal)
			if cohort != nil {
				t.Fatalf("built a %q cohort no discovery arm was proven for", kind)
			}
			if basis != CohortKindMemberKindUnservable {
				t.Fatalf("basis = %q, want %q -- the limitation has to be countable", basis, CohortKindMemberKindUnservable)
			}
			if declared != kind {
				t.Fatalf("declared kind = %q, want %q -- a refusal that cannot name the kind it refused sends the next reader to the question text", declared, kind)
			}
		})
	}
}

// The complement, so the guard cannot quietly refuse everything: every kind
// with a PROVEN ARM must still discover. A deny-list with no positive control
// is indistinguishable from a gate that always denies.
func TestServableCohortKindsStillDiscover(t *testing.T) {
	t.Parallel()
	for _, kind := range []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			discovery := frameDiscovery(contextfabric.SubjectExpression{
				Kind:       contextfabric.SubjectExpressionDiscoveredKind,
				Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
			}, "status", []string{"things"})
			cohort, _, _, _, basis := DiscoveredCohort(storage.Principal{OrgID: "org_1"}, discovery,
				[]CandidateNode{candidateNode(kind, "node_a", "Node A", 0.9, "*")}, noInternal)
			if basis != CohortKindFromFrameMemberKind || cohort == nil {
				t.Fatalf("kind %q no longer discovers: basis=%q cohort=%v", kind, basis, cohort != nil)
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
	// RE-POINTED off `repository` in the change that made repository
	// servable. This fixture is the ONLY input in this test that reaches
	// member_kind_unservable, so leaving it on a now-servable kind would
	// have turned that basis into a dead label while the test stayed green
	// on the other four. `incident` is chosen because it is genuinely
	// unservable and expected to stay that way.
	unservable := contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectIncident},
	}
	reached := map[CohortKindBasis]bool{}
	for _, frame := range []*contextfabric.QuestionFrame{
		nil,
		{SubjectExpression: named},
		{SubjectExpression: explicit},
		{SubjectExpression: discovered},
		{SubjectExpression: unservable},
	} {
		_, _, basis := cohortKindFromFrame(frame)
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
