package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE INVARIANT: the population is counted over the WHOLE pool, and the
// render cap bounds only how many members the answer carries.
//
// The two used to be one number. MaxCohortMembers is clamped from the
// response ITEM budget before retrieval runs -- it is sized for what the
// answer can render -- and the assembly loop stopped counting at it, so
// "how many exist" was unanswerable by construction for every organization
// larger than its own render allowance.
//
// EVERY FIXTURE BELOW PUTS THE POOL ABOVE THE CAP, because a pool at or
// below the cap cannot separate the two numbers: they agree there, and a
// test that cannot tell the old code from the new one pins nothing.

// populationDiscovery is a team cohort whose cap is set by the caller, so
// each test states the ONE number its claim depends on.
func populationDiscovery(cap int) contextfabric.GraphDiscoveryRequest {
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
	}, "teams_under_pressure", []string{"teams"})
	discovery.Request.Options.MaxCohortMembers = cap
	return discovery
}

func teamNodes(ids ...string) []CandidateNode {
	nodes := make([]CandidateNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, candidateNode(contextfabric.SubjectTeam, id, "Team "+id, 0.9, "*"))
	}
	return nodes
}

func TestCohortPopulationCountsEveryMatchPastTheRenderCap(t *testing.T) {
	t.Parallel()
	cohort, _, _, _, basis, population := DiscoveredCohort(
		storage.Principal{OrgID: "org_1"}, populationDiscovery(2),
		teamNodes("team_a", "team_b", "team_c", "team_d", "team_e"), false, noInternal)

	if basis != CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want %q -- the fixture never reached assembly, so it proves nothing", basis, CohortKindFromFrameMemberKind)
	}
	if cohort == nil {
		t.Fatal("cohort = nil, want a capped team cohort")
	}
	// BOTH numbers are asserted, and the pair is the claim. Asserting the
	// population alone would pass for code that stopped capping members;
	// asserting the members alone is what the old code already did.
	if len(cohort.Members) != 2 {
		t.Errorf("members = %d, want 2 -- the render cap must still bound what the answer carries", len(cohort.Members))
	}
	if population != 5 {
		t.Errorf("population = %d, want 5 -- every authorized team in the pool counts, cap or no cap", population)
	}
}

func TestCohortPopulationCountsADuplicateSubjectExactlyOnce(t *testing.T) {
	t.Parallel()
	// team_a twice: the shape two retrieval arms returning the same subject
	// produces. The member list dedupes it through `seen`, and the population
	// must dedupe through the SAME set -- a counter incremented before the
	// dedup would report a population inflated by retrieval's own overlap.
	_, _, _, _, _, population := DiscoveredCohort(
		storage.Principal{OrgID: "org_1"}, populationDiscovery(1),
		teamNodes("team_a", "team_b", "team_a"), false, noInternal)

	if population != 2 {
		t.Fatalf("population = %d, want 2 -- team_a appears twice in the pool and is one subject", population)
	}
}

func TestCohortPopulationCountsOnlyAuthorizedMembersOfTheCohortKind(t *testing.T) {
	t.Parallel()
	// One authorized team, one project (wrong kind), one team the principal
	// cannot see. Only the first is a member of this cohort, so the
	// population is 1 -- the cap is 10 and cannot be what the assertion
	// reads.
	nodes := []CandidateNode{
		candidateNode(contextfabric.SubjectTeam, "team_a", "Team A", 0.9, "*"),
		candidateNode(contextfabric.SubjectProject, "project_a", "Project A", 0.9, "*"),
		candidateNode(contextfabric.SubjectTeam, "team_denied", "Team Denied", 0.9, "repo_the_principal_cannot_read"),
	}
	_, _, _, _, _, population := DiscoveredCohort(
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"repo_visible"}},
		populationDiscovery(10), nodes, false, noInternal)

	if population != 1 {
		t.Fatalf("population = %d, want 1 -- a wrong-kind node and an unauthorized node are not members to count", population)
	}
}

func TestCohortPopulationIsZeroWhenTheFrameRefusesACohort(t *testing.T) {
	t.Parallel()
	// named_subject refuses before assembly. The population must be zero
	// rather than the pool size: reporting the pool here would declare a
	// population for a question that asked about one subject.
	discovery := frameDiscovery(contextfabric.SubjectExpression{
		Kind:  contextfabric.SubjectExpressionNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{"team_a"}},
	}, "team_state", []string{"team_a"})
	discovery.Request.Options.MaxCohortMembers = 10

	cohort, _, _, _, basis, population := DiscoveredCohort(
		storage.Principal{OrgID: "org_1"}, discovery, teamNodes("team_a", "team_b"), false, noInternal)

	if cohort != nil {
		t.Fatalf("cohort = %+v, want nil for a refusing basis", cohort)
	}
	if basis == CohortKindFromFrameMemberKind {
		t.Fatalf("basis = %q, want a refusing basis -- the fixture did not refuse, so it proves nothing", basis)
	}
	if population != 0 {
		t.Fatalf("population = %d, want 0 -- no cohort was assembled, so nothing was counted", population)
	}
}
