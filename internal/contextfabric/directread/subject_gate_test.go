package directread

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fakeGraph is one organization's graph. The per-node decision is the REAL
// shared predicate (graphrank.AuthorizeStoredSubjectNodes, the function
// falkorgraph.Adapter.AuthorizeStoredSubjects hands its nodes to), so the
// gate is tested against the rule production applies, not a copy of it.
type fakeGraph struct {
	org        string
	nodes      map[string]map[string]interface{} // SubjectKey -> node attributes
	reach      map[string][]string               // SubjectKey -> ownership-reached repositories
	bindErr    error
	authErr    error
	reachErr   error
	shortAuth  bool
	authCalls  int
	reachCalls int
}

func (g *fakeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, g.bindErr
}

func (g *fakeGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.authCalls++
	if g.authErr != nil {
		return nil, g.authErr
	}
	nodes := map[string][]graphrank.CandidateNode{}
	if principal.OrgID == g.org {
		for _, subject := range subjects {
			if attributes, ok := g.nodes[graphrank.SubjectKey(subject)]; ok {
				nodes[graphrank.SubjectKey(subject)] = []graphrank.CandidateNode{{Attributes: attributes}}
			}
		}
	}
	outcomes := graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes)
	if g.shortAuth {
		return outcomes[:len(outcomes)-1], nil
	}
	return outcomes, nil
}

func (g *fakeGraph) OwnershipReachedRepositories(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	g.reachCalls++
	if g.reachErr != nil {
		return nil, g.reachErr
	}
	out := make([][]string, len(subjects))
	for index, subject := range subjects {
		if principal.OrgID == g.org {
			out[index] = slices.Clone(g.reach[graphrank.SubjectKey(subject)])
		}
	}
	return out, nil
}

type recordingRecorder struct{ decisions []Authorization }

func (r *recordingRecorder) RecordDirectReadAuthorization(_ context.Context, _ storage.Principal, decision Authorization) {
	r.decisions = append(r.decisions, decision)
}

const (
	orgA = "org-a"
	orgB = "org-b"
)

var (
	repoA    = subject(contractsv1.ContextFabricSubjectRepository, "repository:a")
	repoB    = subject(contractsv1.ContextFabricSubjectRepository, "repository:b")
	guessed  = subject(contractsv1.ContextFabricSubjectRepository, "repository:guessed")
	projectP = subject(contractsv1.ContextFabricSubjectProject, "project:p")
	projectQ = subject(contractsv1.ContextFabricSubjectProject, "project:q")
	teamT    = subject(contractsv1.ContextFabricSubjectTeam, "team:t")
	teamU    = subject(contractsv1.ContextFabricSubjectTeam, "team:u")
	teamW    = subject(contractsv1.ContextFabricSubjectTeam, "team:w")
	workA    = subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:a")
	workB    = subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:b")
	ownOrg   = subject(contractsv1.ContextFabricSubjectOrganization, orgA)
	ownOrgGK = subject(contractsv1.ContextFabricSubjectOrganization, "organization:"+orgA)
	otherOrg = subject(contractsv1.ContextFabricSubjectOrganization, orgB)
)

func subject(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: kind, CanonicalID: id}
}

func repos(values ...string) map[string]interface{} {
	return map[string]interface{}{"authorization_repositories": values}
}

// graphOfOrgA is the BUILD ENTRY S0 scenario: repository A (acme/a) and B
// (acme/b); team T owns A and B; team U owns only B; team W is a pre-4390
// wildcard team; project P is owned by U only (so it reaches only B);
// project Q is owned by T (reaches A and B). Project nodes carry the "*"
// repository list projection always writes for them.
func graphOfOrgA() *fakeGraph {
	return &fakeGraph{
		org: orgA,
		nodes: map[string]map[string]interface{}{
			graphrank.SubjectKey(repoA):    repos("acme/a"),
			graphrank.SubjectKey(repoB):    repos("acme/b"),
			graphrank.SubjectKey(teamT):    repos("acme/a", "acme/b"),
			graphrank.SubjectKey(teamU):    repos("acme/b"),
			graphrank.SubjectKey(teamW):    {"authorization_repositories": "*"},
			graphrank.SubjectKey(projectP): {"authorization_repositories": "*", "authorization_projects": []string{"project:p"}},
			graphrank.SubjectKey(projectQ): {"authorization_repositories": "*", "authorization_projects": []string{"project:q"}},
			graphrank.SubjectKey(workA):    repos("acme/a"),
			graphrank.SubjectKey(workB):    repos("acme/b"),
		},
		reach: map[string][]string{
			graphrank.SubjectKey(teamT):    {"acme/a", "acme/b"},
			graphrank.SubjectKey(teamU):    {"acme/b"},
			graphrank.SubjectKey(teamW):    nil,
			graphrank.SubjectKey(projectP): {"acme/b"},
			graphrank.SubjectKey(projectQ): {"acme/a", "acme/b"},
		},
	}
}

func restrictedToA() storage.Principal {
	return storage.Principal{OrgID: orgA, Subject: "user-1", CredentialID: "cred-1", RepositoryScopes: []string{"acme/a"}}
}

func outcomeOf(t *testing.T, decision Authorization, ref contextfabric.SubjectRef) SubjectOutcome {
	t.Helper()
	for _, gated := range decision.Outcomes {
		if gated.Subject.Kind == ref.Kind && gated.Subject.CanonicalID == ref.CanonicalID {
			return gated.Outcome
		}
	}
	t.Fatalf("no outcome for %v in %+v", ref, decision.Outcomes)
	return ""
}

// T1 (BUILD ENTRY S0 first failing test). Rule 1: a restricted caller gets
// data inside its grant and denied_or_not_found outside it. Each expectation
// names the clause that must refuse it (rule 3: one clause, one case).
func TestSubjectGateRestrictedCallerMatrix(t *testing.T) {
	graph := graphOfOrgA()
	recorder := &recordingRecorder{}
	gate := NewSubjectGate(graph, recorder)
	principal := restrictedToA()
	requested := []contextfabric.SubjectRef{repoA, repoB, guessed, projectP, projectQ, teamT, teamU, teamW, workA, workB, ownOrg, ownOrgGK, otherOrg}
	authorized, decision := gate.Authorize(context.Background(), principal, requested)

	want := map[contextfabric.SubjectRef]SubjectOutcome{
		repoA:    SubjectAdmitted,             // inside the grant
		repoB:    SubjectDenied,               // node predicate
		guessed:  SubjectAbsent,               // existence lookup
		projectP: SubjectOwnershipUnproven,    // project wildcard is not enough; reaches only B
		projectQ: SubjectAdmitted,             // reaches A through team T
		teamT:    SubjectAdmitted,             // owns A
		teamU:    SubjectDenied,               // node predicate (owns only B)
		teamW:    SubjectOwnershipUnproven,    // wildcard team: predicate admits, ownership does not
		workA:    SubjectAdmitted,             // inside the grant
		workB:    SubjectDenied,               // node predicate
		ownOrg:   SubjectAdmitted,             // caller's own org, bare id
		ownOrgGK: SubjectAdmitted,             // caller's own org, graph key form
		otherOrg: SubjectOrganizationMismatch, // another org
	}
	for ref, outcome := range want {
		if got := outcomeOf(t, decision, ref); got != outcome {
			t.Errorf("%s %s: outcome %s, want %s", ref.Kind, ref.CanonicalID, got, outcome)
		}
		if got, wantPublic := outcomeOf(t, decision, ref).Public(), outcome.Public(); got != wantPublic {
			t.Errorf("%s %s: public %s, want %s", ref.Kind, ref.CanonicalID, got, wantPublic)
		}
	}
	wantAdmitted := []contextfabric.SubjectRef{repoA, projectQ, teamT, workA, ownOrg, ownOrgGK}
	if got := authorized.Subjects(); !slices.Equal(got, wantAdmitted) {
		t.Fatalf("authorized subjects %v, want %v", got, wantAdmitted)
	}
	if decision.Decision != DecisionPartial || decision.Reason != ReasonOrganizationMismatch {
		t.Fatalf("decision %s/%s, want partial/organization_mismatch", decision.Decision, decision.Reason)
	}
	if decision.AdmittedCount != 6 || decision.DeniedCount != 3 || decision.AbsentCount != 1 || decision.OwnershipUnprovenCount != 2 || decision.OrganizationMismatchCount != 1 || decision.SubjectCount != 13 {
		t.Fatalf("counts %+v", decision)
	}
	if !slices.Equal(decision.RefusedKinds, []string{"organization", "project", "repository", "team", "work_item"}) {
		t.Fatalf("refused kinds %v", decision.RefusedKinds)
	}
	if len(recorder.decisions) != 1 || recorder.decisions[0].Decision != DecisionPartial {
		t.Fatalf("recorded %d decisions", len(recorder.decisions))
	}
	if !authorized.IssuedTo(principal) {
		t.Fatal("authorized subjects not bound to the caller")
	}
}

// The graph lookup runs for EVERY principal (E.2): an unrestricted caller
// with a guessed id, or an id from another organization's graph, is refused.
// This is the named plant of BUILD ENTRY S0: a gate that skips the lookup for
// an unrestricted principal (the stored-result gate's shape) fails here.
func TestSubjectGateLooksUpEveryPrincipalClass(t *testing.T) {
	for _, principal := range []storage.Principal{
		{OrgID: orgA, Subject: "u", CredentialID: "c"},                                       // unrestricted
		{OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"*"}},      // universal
		{OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/a"}}, // restricted
	} {
		class := ClassifyPrincipal(principal)
		graph := graphOfOrgA()
		_, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{guessed, repoA})
		if got := outcomeOf(t, decision, guessed); got != SubjectAbsent {
			t.Errorf("%s: guessed id outcome %s, want absent", class, got)
		}
		if got := outcomeOf(t, decision, repoA); got != SubjectAdmitted {
			t.Errorf("%s: repository A outcome %s, want admitted", class, got)
		}
		if graph.authCalls != 1 {
			t.Errorf("%s: graph lookup ran %d times, want 1", class, graph.authCalls)
		}
	}
}

// Unrestricted and universal callers hold every repository: the ownership
// reach is not consulted, and a wildcard project or team is admitted when
// its node exists.
func TestSubjectGateOwnershipRuleOnlyForRestricted(t *testing.T) {
	for _, principal := range []storage.Principal{
		{OrgID: orgA, Subject: "u", CredentialID: "c"},
		{OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"*"}},
	} {
		graph := graphOfOrgA()
		_, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{projectP, teamW, teamU})
		if decision.Decision != DecisionAdmitted || graph.reachCalls != 0 {
			t.Fatalf("%s: decision %s reach calls %d", ClassifyPrincipal(principal), decision.Decision, graph.reachCalls)
		}
	}
}

// Cross-org: a caller of org B asks for org A's real ids. Every one is absent
// in B's graph, and org A as an organization subject is a mismatch.
func TestSubjectGateCrossOrganization(t *testing.T) {
	graph := graphOfOrgA()
	principal := storage.Principal{OrgID: orgB, Subject: "u", CredentialID: "c"}
	authorized, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{repoA, teamT, projectQ, ownOrg})
	if decision.Decision != DecisionDenied || authorized.Len() != 0 {
		t.Fatalf("decision %s, %d admitted", decision.Decision, authorized.Len())
	}
	for _, ref := range []contextfabric.SubjectRef{repoA, teamT, projectQ} {
		if got := outcomeOf(t, decision, ref); got != SubjectAbsent {
			t.Errorf("%v: %s, want absent", ref, got)
		}
	}
	if got := outcomeOf(t, decision, ownOrg); got != SubjectOrganizationMismatch {
		t.Errorf("org A for an org B caller: %s", got)
	}
}

// Empty grant (a restricted list that grants nothing: blank entries) admits
// no graph subject; an empty request is refused before any graph read.
func TestSubjectGateEmptyGrantAndEmptyRequest(t *testing.T) {
	graph := graphOfOrgA()
	principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{" "}}
	if ClassifyPrincipal(principal) != ClassRestricted {
		t.Fatalf("blank grant classified %s", ClassifyPrincipal(principal))
	}
	authorized, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{repoA, teamT, projectQ, workA})
	if decision.Decision != DecisionDenied || authorized.Len() != 0 || decision.AdmittedCount != 0 {
		t.Fatalf("empty grant: decision %s admitted %d", decision.Decision, decision.AdmittedCount)
	}
	for _, requested := range [][]contextfabric.SubjectRef{nil, {}} {
		graph := graphOfOrgA()
		authorized, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), restrictedToA(), requested)
		if decision.Decision != DecisionDenied || decision.Reason != ReasonNoSubjects || authorized.Len() != 0 || graph.authCalls != 0 {
			t.Fatalf("empty request: %s/%s graph calls %d", decision.Decision, decision.Reason, graph.authCalls)
		}
	}
}

// Unknown subjects give the same public answer as denied ones: absent,
// denied, ownership-unproven, org mismatch and malformed all map to one
// wire value, and nothing else.
func TestSubjectGateUnknownIsIndistinguishableFromDenied(t *testing.T) {
	graph := graphOfOrgA()
	_, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), restrictedToA(), []contextfabric.SubjectRef{
		guessed, repoB, projectP, otherOrg,
		{Kind: "", CanonicalID: "repository:a"},
		{Kind: "person", CanonicalID: "x"},
		{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "  "},
	})
	for _, gated := range decision.Outcomes {
		if gated.Outcome.Public() != PublicDeniedOrNotFound {
			t.Errorf("%v: public %s", gated.Subject, gated.Outcome.Public())
		}
	}
	if decision.InvalidCount != 3 || decision.Decision != DecisionDenied {
		t.Fatalf("invalid %d decision %s", decision.InvalidCount, decision.Decision)
	}
	seen := map[string]bool{}
	for _, outcome := range SubjectOutcomeVocabulary() {
		seen[outcome.Public()] = true
	}
	if len(seen) != 2 || !seen[PublicAdmitted] || !seen[PublicDeniedOrNotFound] {
		t.Fatalf("public vocabulary %v", seen)
	}
}

// Fail closed: every graph failure is unavailable with nothing admitted --
// never a serve, never a silent refusal.
func TestSubjectGateFailsClosed(t *testing.T) {
	boom := fmt.Errorf("%w: falkor down", contextfabric.ErrUnavailable)
	cases := map[string]struct {
		graph      GraphAuthority
		wantReason Reason
		wantClass  string
	}{
		"no graph":        {nil, ReasonAuthorizerMissing, ""},
		"binding error":   {func() *fakeGraph { g := graphOfOrgA(); g.bindErr = boom; return g }(), ReasonGraphReadFailed, "dependency_unavailable"},
		"authorize error": {func() *fakeGraph { g := graphOfOrgA(); g.authErr = context.DeadlineExceeded; return g }(), ReasonGraphReadFailed, "deadline_exceeded"},
		"short outcomes":  {func() *fakeGraph { g := graphOfOrgA(); g.shortAuth = true; return g }(), ReasonGraphReadFailed, "graph_error"},
		"ownership error": {func() *fakeGraph { g := graphOfOrgA(); g.reachErr = errors.New("x"); return g }(), ReasonGraphReadFailed, "graph_error"},
	}
	for name, tc := range cases {
		recorder := &recordingRecorder{}
		authorized, decision := NewSubjectGate(tc.graph, recorder).Authorize(context.Background(), restrictedToA(), []contextfabric.SubjectRef{repoA, teamT})
		if decision.Decision != DecisionUnavailable || decision.Reason != tc.wantReason || authorized.Len() != 0 || authorized.IssuedTo(restrictedToA()) {
			t.Errorf("%s: %s/%s admitted %d", name, decision.Decision, decision.Reason, authorized.Len())
		}
		if decision.ErrorClass() != tc.wantClass {
			t.Errorf("%s: error class %q, want %q", name, decision.ErrorClass(), tc.wantClass)
		}
		if len(recorder.decisions) != 1 {
			t.Errorf("%s: %d decisions recorded", name, len(recorder.decisions))
		}
	}
	// A graph never projected is a refusal (every graph subject absent), not
	// an unavailability, and not a serve.
	g := graphOfOrgA()
	g.bindErr = contextfabric.ErrGraphNotProjected
	authorized, decision := NewSubjectGate(g, nil).Authorize(context.Background(), restrictedToA(), []contextfabric.SubjectRef{repoA, ownOrg})
	if decision.Decision != DecisionPartial || decision.Reason != ReasonGraphNotProjected || outcomeOf(t, decision, repoA) != SubjectAbsent || authorized.Len() != 1 {
		t.Fatalf("not projected: %s/%s", decision.Decision, decision.Reason)
	}
}

func TestSubjectGateBoundsAndDuplicates(t *testing.T) {
	graph := graphOfOrgA()
	over := make([]contextfabric.SubjectRef, MaxSubjectsPerRequest+1)
	for index := range over {
		over[index] = subject(contractsv1.ContextFabricSubjectRepository, fmt.Sprintf("repository:%d", index))
	}
	_, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), restrictedToA(), over)
	if decision.Decision != DecisionDenied || decision.Reason != ReasonSubjectLimit || graph.authCalls != 0 {
		t.Fatalf("over the limit: %s/%s graph calls %d", decision.Decision, decision.Reason, graph.authCalls)
	}
	withLabel := repoA
	withLabel.Label = "caller text"
	authorized, decision := NewSubjectGate(graphOfOrgA(), nil).Authorize(context.Background(), restrictedToA(), []contextfabric.SubjectRef{withLabel, repoA, repoA})
	if decision.SubjectCount != 1 || !slices.Equal(authorized.Subjects(), []contextfabric.SubjectRef{repoA}) {
		t.Fatalf("dedupe/label: %d subjects %v", decision.SubjectCount, authorized.Subjects())
	}
}

func TestSubjectGatePrincipalWithoutOrganization(t *testing.T) {
	graph := graphOfOrgA()
	authorized, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), storage.Principal{}, []contextfabric.SubjectRef{repoA})
	if decision.Decision != DecisionDenied || decision.Reason != ReasonPrincipalInvalid || authorized.Len() != 0 || graph.authCalls != 0 {
		t.Fatalf("%s/%s", decision.Decision, decision.Reason)
	}
}

// Rule 4: a vocabulary that does not cover what the gate emits must fail.
func TestSubjectGateVocabulariesCoverEveryOutcome(t *testing.T) {
	reasons := ReasonVocabulary()
	decisions := DecisionVocabulary()
	graph := graphOfOrgA()
	gate := NewSubjectGate(graph, nil)
	for _, requested := range [][]contextfabric.SubjectRef{{repoA}, {repoA, repoB}, {repoB}, {guessed}, {projectP}, {otherOrg}, {{Kind: "x", CanonicalID: "y"}}} {
		_, decision := gate.Authorize(context.Background(), restrictedToA(), requested)
		if !slices.Contains(reasons[:], decision.Reason) || !slices.Contains(decisions[:], decision.Decision) {
			t.Fatalf("%v: %s/%s outside the vocabulary", requested, decision.Decision, decision.Reason)
		}
	}
}

// T1 matrix: every subject kind of the closed vocabulary x every principal
// class. The kind list is read from the vocabulary, so a kind added later
// gets a cell or this test fails (rule 4).
func TestSubjectGateKindClassMatrix(t *testing.T) {
	kinds := contractsv1.ContextFabricSubjectKindVocabulary()
	classes := PrincipalClassVocabulary()
	principals := map[PrincipalClass]storage.Principal{
		ClassUnrestricted: {OrgID: orgA, Subject: "u", CredentialID: "c"},
		ClassUniversal:    {OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"*"}},
		ClassRestricted:   {OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/a"}},
	}
	cells := 0
	for _, kind := range kinds {
		inside := subject(kind, string(kind)+":inside")
		outside := subject(kind, string(kind)+":outside")
		missing := subject(kind, string(kind)+":missing")
		if kind == contractsv1.ContextFabricSubjectOrganization {
			inside, outside, missing = subject(kind, orgA), subject(kind, orgB), subject(kind, "organization:"+orgB)
		}
		graph := &fakeGraph{
			org: orgA,
			nodes: map[string]map[string]interface{}{
				graphrank.SubjectKey(inside):  repos("acme/a"),
				graphrank.SubjectKey(outside): repos("acme/b"),
			},
			reach: map[string][]string{
				graphrank.SubjectKey(inside):  {"acme/a"},
				graphrank.SubjectKey(outside): {"acme/b"},
			},
		}
		for _, class := range classes {
			principal, ok := principals[class]
			if !ok {
				t.Fatalf("no principal for class %s", class)
			}
			_, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{inside, outside, missing})
			wantOutside := SubjectAdmitted
			switch {
			case kind == contractsv1.ContextFabricSubjectOrganization:
				wantOutside = SubjectOrganizationMismatch
			case class == ClassRestricted:
				wantOutside = SubjectDenied
			}
			wantMissing := SubjectAbsent
			if kind == contractsv1.ContextFabricSubjectOrganization {
				wantMissing = SubjectOrganizationMismatch
			}
			if got := outcomeOf(t, decision, inside); got != SubjectAdmitted {
				t.Errorf("%s/%s inside: %s", kind, class, got)
			}
			if got := outcomeOf(t, decision, outside); got != wantOutside {
				t.Errorf("%s/%s outside: %s, want %s", kind, class, got, wantOutside)
			}
			if got := outcomeOf(t, decision, missing); got != wantMissing {
				t.Errorf("%s/%s missing: %s, want %s", kind, class, got, wantMissing)
			}
			cells++
		}
	}
	if want := len(kinds) * len(classes); cells != want || want < 45 {
		t.Fatalf("matrix ran %d cells, want %d (at least 15 kinds x 3 classes)", cells, len(kinds)*len(classes))
	}
}
