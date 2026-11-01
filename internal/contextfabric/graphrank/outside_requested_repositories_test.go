package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func outsideRepositoriesResolve(t *testing.T, principal storage.Principal, requested contextfabric.RequestedScope, nodes ...CandidateNode) contextfabric.SubjectResolution {
	t.Helper()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"payments-worker": nodes}}
	request := testRequest()
	request.RequestedScope = requested
	resolution, _, err := ResolveSubjects(context.Background(), principal, request, testInterpreted("payments-worker"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	return resolution
}

func outsideRepositoryNode(id, slug string) CandidateNode {
	return candidateNode(contextfabric.SubjectRepository, id, slug, 0.8, []string{slug})
}

// TestACountOfMatchesOutsideTheRequestedRepositoriesIsForAnUnrestrictedCaller
// (CHAOS-12360, lead ruling): the count is what lets the answer say that the
// narrowing refused a real match. It exists only for a caller whose grant
// covers the subject, so the narrowing is the only thing that refused it.
func TestACountOfMatchesOutsideTheRequestedRepositoriesIsForAnUnrestrictedCaller(t *testing.T) {
	t.Parallel()
	narrowed := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}}
	worker := outsideRepositoryNode("repository:worker", "acme/payments-worker")
	for name, principal := range map[string]storage.Principal{
		"no repository list": {OrgID: "org_1"},
		"a * list":           {OrgID: "org_1", RepositoryScopes: []string{"*"}},
	} {
		got := outsideRepositoriesResolve(t, principal, narrowed, worker)
		if got.OutsideRequestedRepositories != 1 || len(got.Candidates) != 0 {
			t.Fatalf("%s: outside %d, candidates %d; want 1 and 0: the refusal stands, and it is counted", name, got.OutsideRequestedRepositories, len(got.Candidates))
		}
	}
	two := outsideRepositoriesResolve(t, storage.Principal{OrgID: "org_1"}, narrowed, worker, outsideRepositoryNode("repository:gateway", "acme/payments-gateway"), worker)
	if two.OutsideRequestedRepositories != 2 {
		t.Fatalf("outside %d, want 2: distinct subjects, not hits", two.OutsideRequestedRepositories)
	}
}

// TestARestrictedCallerNeverLearnsOfAMatchOutsideItsGrant (CHAOS-12360): for a
// repository-restricted caller the count would be an existence oracle for a
// subject it may not see. That holds with the subject outside the grant, and
// also inside it: a restricted caller is never an unrestricted principal.
func TestARestrictedCallerNeverLearnsOfAMatchOutsideItsGrant(t *testing.T) {
	t.Parallel()
	narrowed := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}}
	worker := outsideRepositoryNode("repository:worker", "acme/payments-worker")
	for name, grant := range map[string][]string{
		"the match is outside the grant": {"acme/payments-api"},
		"the match is inside the grant":  {"acme/payments-api", "acme/payments-worker"},
	} {
		got := outsideRepositoriesResolve(t, storage.Principal{OrgID: "org_1", RepositoryScopes: grant}, narrowed, worker)
		if got.OutsideRequestedRepositories != 0 {
			t.Fatalf("%s: outside %d, want 0: an existence oracle", name, got.OutsideRequestedRepositories)
		}
	}
}

// TestAProjectOnlyCallerNeverLearnsOfAMatchOutsideItsGrant (CHAOS-12360): a
// project-only caller is never an unrestricted principal, whatever its
// repository list holds (CHAOS-12299 r3). That holds for a repository its grant
// refuses, and for its own project: the grant admits that node and only the
// narrowing refuses it, so nothing but the principal check keeps it uncounted.
func TestAProjectOnlyCallerNeverLearnsOfAMatchOutsideItsGrant(t *testing.T) {
	t.Parallel()
	narrowed := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}}
	worker := outsideRepositoryNode("repository:worker", "acme/payments-worker")
	ownProject := candidateNode(contextfabric.SubjectProject, "project.v2:github:P-1", "Payments worker", 0.8, []string{"acme/payments-worker"})
	for _, scopes := range [][]string{nil, {}, {"*"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}
		if got := outsideRepositoriesResolve(t, principal, narrowed, worker, ownProject); got.OutsideRequestedRepositories != 0 {
			t.Fatalf("%q: outside %d, want 0: an existence oracle", scopes, got.OutsideRequestedRepositories)
		}
	}
}

// TestOnlyARequestedRepositoryNarrowingCountsAMatchAsOutside: nothing else is
// that refusal. No narrowing has nothing to refuse, and a weak vector hit is
// not a match the offers would have shown.
func TestOnlyARequestedRepositoryNarrowingCountsAMatchAsOutside(t *testing.T) {
	t.Parallel()
	worker := outsideRepositoryNode("repository:worker", "acme/payments-worker")
	principal := storage.Principal{OrgID: "org_1"}
	if got := outsideRepositoriesResolve(t, principal, contextfabric.RequestedScope{}, worker); got.OutsideRequestedRepositories != 0 {
		t.Fatalf("no narrowing: outside %d, want 0", got.OutsideRequestedRepositories)
	}
	narrowed := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}}
	if got := outsideRepositoriesResolve(t, principal, narrowed, outsideRepositoryNode("repository:api", "acme/payments-api")); got.OutsideRequestedRepositories != 0 {
		t.Fatalf("a match inside the narrowing: outside %d, want 0", got.OutsideRequestedRepositories)
	}
	vector := outsideRepositoryNode("repository:worker", "acme/payments-worker")
	vector.Mechanism = contextfabric.MatchVector
	if got := outsideRepositoriesResolve(t, principal, narrowed, vector); got.OutsideRequestedRepositories != 0 {
		t.Fatalf("a vector-only hit: outside %d, want 0", got.OutsideRequestedRepositories)
	}
	team := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}, TeamIDs: []string{"team-b"}}
	if got := outsideRepositoriesResolve(t, principal, team, worker); got.OutsideRequestedRepositories != 0 {
		t.Fatalf("a repository and a team narrowing: outside %d, want 0: the team could be what refused it", got.OutsideRequestedRepositories)
	}
}

// TestAKindTheQuestionDoesNotAskForIsNotAMatchOutside: the contest admission
// refuses a member kind, so it would not have been offered without the
// narrowing either, and the count must not state it.
func TestAKindTheQuestionDoesNotAskForIsNotAMatchOutside(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.RequestedScope = contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}}
	worker := outsideRepositoryNode("repository:worker", "acme/payments-worker")
	deps := (&fakeGraphBackend{}).deps()
	for name, tc := range map[string]struct {
		admission *contestAdmission
		want      int
	}{
		"no contest scope":     {nil, 1},
		"another kind refused": {&contestAdmission{scope: contestScope{MemberKind: contextfabric.SubjectPullRequest}}, 1},
		"the kind is refused":  {&contestAdmission{scope: contestScope{MemberKind: contextfabric.SubjectRepository}}, 0},
	} {
		got := recordOutsideRequestedRepositories(storage.Principal{OrgID: "org_1"}, request, deps, "payments-worker", []CandidateNode{worker}, tc.admission, nil)
		if len(got) != tc.want {
			t.Fatalf("%s: outside %d, want %d", name, len(got), tc.want)
		}
	}
}

// TestAComparisonCountsEachSubjectOnceAcrossItsOperands: each operand's terms
// can match outside the narrowing, and a held comparison states the union.
func TestAComparisonCountsEachSubjectOnceAcrossItsOperands(t *testing.T) {
	t.Parallel()
	shared := outsideRepositoryNode("repository:shared", "acme/payments-shared")
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {outsideRepositoryNode("repository:worker", "acme/payments-worker"), shared},
		"beta":  {outsideRepositoryNode("repository:gateway", "acme/payments-gateway"), shared},
	}, &slotGateTracer{})
	comparison := contextfabric.ComparisonOperands{
		Admission: contextfabric.ComparisonAdmittedNamedPair,
		Slots: []contextfabric.ComparisonOperandSlot{
			{Position: 0, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectRepository, Terms: []string{"alpha"}},
			{Position: 1, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectRepository, Terms: []string{"beta"}},
		},
	}
	request := slotGateRequest()
	request.RequestedScope = contextfabric.RequestedScope{RepositorySlugs: []string{"acme/payments-api"}}
	resolution, _, _, err := resolveNamedComparison(context.Background(), storage.Principal{OrgID: "org-1"}, request, deps, comparison, nil)
	if err != nil {
		t.Fatalf("resolveNamedComparison() error = %v", err)
	}
	if resolution.OutsideRequestedRepositories != 3 {
		t.Fatalf("outside %d, want 3 distinct subjects", resolution.OutsideRequestedRepositories)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 {
		t.Fatalf("candidates %v, committed %v, want none: the narrowing stands", resolution.Candidates, resolution.Committed)
	}
}
