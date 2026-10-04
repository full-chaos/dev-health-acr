package directread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// lookupFakeGraph holds one graph per organization. Its gate side applies the
// REAL shared predicate (graphrank.AuthorizeStoredSubjectNodes), as the S0
// tests do. Its lookup side reads only the caller's own organization, and can
// be told to leak (return nodes the gate side does not hold) to prove the
// gate, not the graph read, is what admits.
type lookupFakeGraph struct {
	orgs map[string]*lookupOrgGraph

	listCalls    int
	nameCalls    int
	authCalls    int
	bindErr      error
	listErr      error
	nameErr      error
	authErr      error
	leakPhantoms []LookupNode // returned by every list/name read, unknown to the gate side
	truncateName bool
}

type lookupOrgGraph struct {
	nodes []LookupNode        // every node of the org, any order
	reach map[string][]string // ownership reach per SubjectKey
}

func (g *lookupFakeGraph) org(id string) *lookupOrgGraph {
	if g.orgs[id] == nil {
		return &lookupOrgGraph{}
	}
	return g.orgs[id]
}

func (g *lookupFakeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, g.bindErr
}

func (g *lookupFakeGraph) ListSubjectsByKind(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, kind, after string, pageSize int) (LookupPage, error) {
	g.listCalls++
	if g.listErr != nil {
		return LookupPage{}, g.listErr
	}
	var all []LookupNode
	for _, node := range g.org(principal.OrgID).nodes {
		if node.Kind == kind && node.CanonicalID > after {
			all = append(all, node)
		}
	}
	for _, node := range g.leakPhantoms {
		if node.Kind == kind && node.CanonicalID > after {
			all = append(all, node)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CanonicalID < all[j].CanonicalID })
	page := LookupPage{More: len(all) > pageSize}
	if page.More {
		all = all[:pageSize]
	}
	page.Nodes = all
	return page, nil
}

func (g *lookupFakeGraph) FindSubjectsByExactName(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, query, kind, after string, pageSize int) (LookupPage, error) {
	g.nameCalls++
	if g.nameErr != nil {
		return LookupPage{}, g.nameErr
	}
	var all []LookupNode
	for _, node := range append(slices.Clone(g.org(principal.OrgID).nodes), g.leakPhantoms...) {
		if node.Kind != kind || !strings.EqualFold(node.Label, query) || node.CanonicalID <= after {
			continue
		}
		if node.Match == "" {
			node.Match = MatchExact
		}
		all = append(all, node)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CanonicalID < all[j].CanonicalID })
	page := LookupPage{More: len(all) > pageSize, Truncated: g.truncateName}
	if page.More {
		all = all[:pageSize]
	}
	page.Nodes = all
	if len(all) > 0 {
		page.After = all[len(all)-1].CanonicalID
	}
	return page, nil
}

func (g *lookupFakeGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.authCalls++
	if g.authErr != nil {
		return nil, g.authErr
	}
	nodes := map[string][]graphrank.CandidateNode{}
	for _, node := range g.org(principal.OrgID).nodes {
		ref := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(node.Kind), CanonicalID: node.CanonicalID}
		attributes := node.Attributes
		// Mirrors falkorgraph's project reach (CHAOS-7080): for a
		// repository-restricted caller a project's "*" is read as its live
		// ownership reach.
		if ClassifyPrincipal(principal) == ClassRestricted && node.Kind == "project" && attributes["authorization_repositories"] == "*" {
			reach := g.org(principal.OrgID).reach[graphrank.SubjectKey(ref)]
			if len(reach) == 0 {
				reach = []string{"acr-context-fabric:no-project-repository-ownership"}
			}
			copied := map[string]interface{}{}
			for key, value := range attributes {
				copied[key] = value
			}
			copied["authorization_repositories"] = slices.Clone(reach)
			attributes = copied
		}
		nodes[graphrank.SubjectKey(ref)] = []graphrank.CandidateNode{{Attributes: attributes}}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

func (g *lookupFakeGraph) OwnershipReachedRepositories(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for index, subject := range subjects {
		out[index] = slices.Clone(g.org(principal.OrgID).reach[graphrank.SubjectKey(subject)])
	}
	return out, nil
}

// Embed and Interpret stand for the model seams. SubjectGraph has no such
// method, so nothing can call them; if code ever reached one it panics.
func (g *lookupFakeGraph) Embed(context.Context, string) ([]float32, error) {
	panic("find_subjects touched an embedding model")
}

func (g *lookupFakeGraph) Interpret(context.Context, string) (any, error) {
	panic("find_subjects touched an interpreter")
}

func repoNode(id, label, slug string) LookupNode {
	return LookupNode{Kind: "repository", CanonicalID: id, Label: label, Attributes: map[string]interface{}{
		"subject_kind": "repository", "canonical_id": id, "label": label,
		"authorization_repositories": []string{slug},
	}}
}

func projectNode(id, label string) LookupNode {
	return LookupNode{Kind: "project", CanonicalID: id, Label: label, Attributes: map[string]interface{}{
		"subject_kind": "project", "canonical_id": id, "label": label, "authorization_repositories": "*",
	}}
}

type lookupRecorder struct{ calls []FindTelemetry }

func (r *lookupRecorder) RecordFindSubjects(_ context.Context, _ storage.Principal, telemetry FindTelemetry) {
	r.calls = append(r.calls, telemetry)
}

func newLookup(graph *lookupFakeGraph, recorder FindRecorder) *SubjectLookup {
	return NewSubjectLookup(graph, NewSubjectGate(graph, nil), recorder)
}

func lookupPrincipal(org string, scopes ...string) storage.Principal {
	return storage.Principal{OrgID: org, Subject: "u", CredentialID: "c", RepositoryScopes: scopes}
}

func threeRepoGraph() *lookupFakeGraph {
	return &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{
		orgA: {nodes: []LookupNode{
			repoNode("repository:a", "acme/a", "acme/a"),
			repoNode("repository:b", "acme/b", "acme/b"),
			repoNode("repository:c", "acme/c", "acme/c"),
		}},
		orgB: {nodes: []LookupNode{repoNode("repository:other", "other/x", "other/x")}},
	}}
}

func ids(subjects []FoundSubject) []string {
	out := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		out = append(out, subject.CanonicalID)
	}
	return out
}

// Restricted principal granted repository A: list returns A only, and
// total_known counts admitted nodes only (not the 3 nodes the graph holds).
func TestFindListRestrictedSeesGrantedRepositoryOnly(t *testing.T) {
	graph := threeRepoGraph()
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA, "acme/a"), FindRequest{Kind: "repository"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(got.Subjects), []string{"repository:a"}) {
		t.Fatalf("subjects = %v, want only repository:a", ids(got.Subjects))
	}
	if got.Population.TotalKnown != 1 || got.Population.Returned != 1 || got.Population.Truncated || got.Population.Kind != "repository" {
		t.Fatalf("population = %+v, want returned=1 total_known=1 (admitted only)", got.Population)
	}
	if got.Status != FindComplete || !got.Page.Complete || got.Consistency != ConsistencyBestEffort {
		t.Fatalf("response = %+v", got)
	}
	if got.Subjects[0].Label != "acme/a" || got.Subjects[0].Kind != "repository" || got.Subjects[0].Match != MatchExact {
		t.Fatalf("subject = %+v", got.Subjects[0])
	}
}

// A guessed name of B gives the same answer as a name that does not exist.
func TestFindNameGuessedRefusedNodeIsIndistinguishableFromAbsent(t *testing.T) {
	graph := threeRepoGraph()
	lookup := newLookup(graph, nil)
	restricted := lookupPrincipal(orgA, "acme/a")
	guessed, err := lookup.Find(context.Background(), restricted, FindRequest{Query: "acme/b", Kinds: []string{"repository"}})
	if err != nil {
		t.Fatal(err)
	}
	absent, err := lookup.Find(context.Background(), restricted, FindRequest{Query: "acme/nope", Kinds: []string{"repository"}})
	if err != nil {
		t.Fatal(err)
	}
	if guessed.Status != FindEmpty || len(guessed.Subjects) != 0 || !slices.Equal(guessed.SearchedKinds, []string{"repository"}) {
		t.Fatalf("guessed = %+v, want empty with searched_kinds", guessed)
	}
	if !reflect.DeepEqual(guessed, absent) {
		t.Fatalf("refused node answers differently from absent:\n%+v\n%+v", guessed, absent)
	}
	// The granted name still resolves, so the empty above is the gate's, not
	// a broken lookup.
	found, err := lookup.Find(context.Background(), restricted, FindRequest{Query: "ACME/A", Kinds: []string{"repository"}})
	if err != nil || len(found.Subjects) != 1 || found.Subjects[0].CanonicalID != "repository:a" || found.Status != FindComplete {
		t.Fatalf("granted name = %+v, %v", found, err)
	}
}

// An unrestricted caller still passes through the graph lookup: a node the
// list read returned that the caller's own graph does not hold (a phantom id)
// is never returned, and the gate did run.
func TestFindUnrestrictedStillPassesThroughGraphLookup(t *testing.T) {
	graph := threeRepoGraph()
	graph.leakPhantoms = []LookupNode{repoNode("repository:phantom", "acme/phantom", "acme/phantom")}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(got.Subjects), []string{"repository:a", "repository:b", "repository:c"}) || got.Population.TotalKnown != 3 {
		t.Fatalf("subjects = %v total_known=%d, want the 3 real nodes", ids(got.Subjects), got.Population.TotalKnown)
	}
	if graph.authCalls == 0 {
		t.Fatal("gate graph lookup did not run for an unrestricted caller")
	}
}

// Another organization's nodes are never returned, by the read or by the
// gate when the read leaks.
func TestFindNeverReturnsAnotherOrganizationsNodes(t *testing.T) {
	graph := threeRepoGraph()
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgB), FindRequest{Kind: "repository"})
	if err != nil || !slices.Equal(ids(got.Subjects), []string{"repository:other"}) {
		t.Fatalf("org B list = %v, %v", ids(got.Subjects), err)
	}
	leaky := threeRepoGraph()
	leaky.leakPhantoms = []LookupNode{repoNode("repository:a", "acme/a", "acme/a")} // org A's node, offered to org B
	got, err = newLookup(leaky, nil).Find(context.Background(), lookupPrincipal(orgB), FindRequest{Kind: "repository"})
	if err != nil || !slices.Equal(ids(got.Subjects), []string{"repository:other"}) {
		t.Fatalf("leaking read: org B saw %v, %v", ids(got.Subjects), err)
	}
	byName, err := newLookup(leaky, nil).Find(context.Background(), lookupPrincipal(orgB), FindRequest{Query: "acme/a", Kinds: []string{"repository"}})
	if err != nil || byName.Status != FindEmpty {
		t.Fatalf("leaking name read: %+v, %v", byName, err)
	}
}

// A project whose repository list is "*" is not enough for a restricted
// caller (the S0 gate rule, reached through find_subjects).
func TestFindProjectNeedsReachedRepositoryForRestrictedCaller(t *testing.T) {
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {
		nodes: []LookupNode{projectNode("project.v2:p1", "P1"), projectNode("project.v2:p2", "P2")},
		reach: map[string][]string{"project\x00project.v2:p1": {"acme/a"}, "project\x00project.v2:p2": {"acme/b"}},
	}}}
	// the fake gate keys by graphrank.SubjectKey; assert its shape once.
	if graphrank.SubjectKey(contextfabric.SubjectRef{Kind: "project", CanonicalID: "project.v2:p1"}) != "project\x00project.v2:p1" {
		t.Skip("SubjectKey shape changed; reach keys need updating")
	}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA, "acme/a"), FindRequest{Kind: "project"})
	if err != nil || !slices.Equal(ids(got.Subjects), []string{"project.v2:p1"}) || got.Population.TotalKnown != 1 {
		t.Fatalf("restricted project list = %v total=%d, %v", ids(got.Subjects), got.Population.TotalKnown, err)
	}
	all, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "project"})
	if err != nil || len(all.Subjects) != 2 {
		t.Fatalf("unrestricted project list = %v, %v", ids(all.Subjects), err)
	}
}

// A cursor is a position, not permission: the same cursor gives each caller
// only what its own fresh gate decision admits, and a tampered cursor cannot
// reach a refused node.
func TestFindCursorIsNotPermission(t *testing.T) {
	graph := threeRepoGraph()
	lookup := newLookup(graph, nil)
	first, err := lookup.Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository", Limit: 1})
	if err != nil || first.Page.NextCursor != EncodeFindCursor("repository:a") || first.Status != FindPartial || first.Page.Complete {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	// The unrestricted caller's cursor, used by a caller granted only C.
	before := graph.authCalls
	second, err := lookup.Find(context.Background(), lookupPrincipal(orgA, "acme/c"), FindRequest{Kind: "repository", Cursor: first.Page.NextCursor})
	if err != nil || !slices.Equal(ids(second.Subjects), []string{"repository:c"}) {
		t.Fatalf("cursor carried permission: %v, %v", ids(second.Subjects), err)
	}
	if graph.authCalls == before {
		t.Fatal("a cursor page did not run the gate")
	}
	// A cursor tampered to sit just before B still cannot return B.
	tampered, err := lookup.Find(context.Background(), lookupPrincipal(orgA, "acme/a"), FindRequest{Kind: "repository", Cursor: EncodeFindCursor("repository:")})
	if err != nil || !slices.Equal(ids(tampered.Subjects), []string{"repository:a"}) {
		t.Fatalf("tampered cursor = %v, %v", ids(tampered.Subjects), err)
	}
}

func TestFindRejectsCursorThatDoesNotDecode(t *testing.T) {
	lookup := newLookup(threeRepoGraph(), nil)
	for _, cursor := range []string{"!!!not base64!!!", "a", EncodeFindCursor("x") + "=", string([]byte{0xff})} {
		if _, err := lookup.Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository", Cursor: cursor}); !errors.Is(err, ErrFindInvalidRequest) {
			t.Errorf("cursor %q error = %v, want invalid_request", cursor, err)
		}
	}
	// base64url of bytes that are not UTF-8.
	if _, err := lookup.Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository", Cursor: "_w"}); !errors.Is(err, ErrFindInvalidRequest) {
		t.Errorf("non-UTF-8 cursor error = %v", err)
	}
}

func TestFindRejectsInvalidRequests(t *testing.T) {
	graph := threeRepoGraph()
	lookup := newLookup(graph, nil)
	for name, request := range map[string]FindRequest{
		"person kind":     {Kind: "person"},
		"user kind":       {Kind: "user"},
		"empty":           {},
		"bad kinds entry": {Query: "x", Kinds: []string{"repository", "member"}},
		"list with kinds": {Kind: "repository", Kinds: []string{"team"}},
		"negative limit":  {Kind: "repository", Limit: -1},
		"too many kinds":  {Query: "x", Kinds: []string{"repository", "project", "team", "work_item", "pull_request", "deployment", "incident", "document", "decision"}},
		"query too long":  {Query: strings.Repeat("x", MaxFindQueryRunes+1)},
	} {
		if _, err := lookup.Find(context.Background(), lookupPrincipal(orgA), request); !errors.Is(err, ErrFindInvalidRequest) {
			t.Errorf("%s: error = %v, want invalid_request", name, err)
		}
	}
	if graph.listCalls+graph.nameCalls+graph.authCalls != 0 {
		t.Fatal("an invalid request reached the graph")
	}
}

// Graph not projected is empty; a graph read error or a gate failure is
// unavailable and serves nothing.
func TestFindGraphStatesFailClosed(t *testing.T) {
	principal := lookupPrincipal(orgA)
	for name, mutate := range map[string]func(*lookupFakeGraph){
		"binding not projected": func(g *lookupFakeGraph) { g.bindErr = contextfabric.ErrGraphNotProjected },
		"list not projected":    func(g *lookupFakeGraph) { g.listErr = fmt.Errorf("x: %w", contextfabric.ErrGraphNotProjected) },
	} {
		graph := threeRepoGraph()
		mutate(graph)
		got, err := newLookup(graph, nil).Find(context.Background(), principal, FindRequest{Kind: "repository"})
		if err != nil || got.Status != FindEmpty || len(got.Subjects) != 0 || !slices.Equal(got.SearchedKinds, []string{"repository"}) {
			t.Errorf("%s: %+v, %v, want empty", name, got, err)
		}
	}
	for name, mutate := range map[string]func(*lookupFakeGraph){
		"binding error": func(g *lookupFakeGraph) { g.bindErr = errors.New("boom") },
		"list error":    func(g *lookupFakeGraph) { g.listErr = errors.New("boom") },
		"gate error":    func(g *lookupFakeGraph) { g.authErr = context.DeadlineExceeded },
	} {
		graph := threeRepoGraph()
		mutate(graph)
		got, err := newLookup(graph, nil).Find(context.Background(), principal, FindRequest{Kind: "repository"})
		if !errors.Is(err, ErrFindUnavailable) || len(got.Subjects) != 0 {
			t.Errorf("%s: %+v, %v, want unavailable and nothing served", name, got, err)
		}
	}
	nameFail := threeRepoGraph()
	nameFail.nameErr = errors.New("boom")
	if _, err := newLookup(nameFail, nil).Find(context.Background(), principal, FindRequest{Query: "acme/a"}); !errors.Is(err, ErrFindUnavailable) {
		t.Errorf("name read error = %v", err)
	}
	if _, err := NewSubjectLookup(nil, NewSubjectGate(threeRepoGraph(), nil), nil).Find(context.Background(), principal, FindRequest{Kind: "repository"}); !errors.Is(err, ErrFindUnavailable) {
		t.Errorf("nil graph error = %v", err)
	}
	if _, err := NewSubjectLookup(threeRepoGraph(), nil, nil).Find(context.Background(), principal, FindRequest{Kind: "repository"}); !errors.Is(err, ErrFindUnavailable) {
		t.Errorf("nil gate error = %v", err)
	}
}

// Keyset paging in canonical id order; total_known is stable across pages.
func TestFindListPagesByKeyset(t *testing.T) {
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {}}}
	for _, id := range []string{"e", "b", "d", "a", "c"} {
		graph.orgs[orgA].nodes = append(graph.orgs[orgA].nodes, repoNode("repository:"+id, id, "acme/"+id))
	}
	lookup := newLookup(graph, nil)
	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		got, err := lookup.Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if got.Population.TotalKnown != 5 {
			t.Fatalf("page %d total_known = %d", page, got.Population.TotalKnown)
		}
		seen = append(seen, ids(got.Subjects)...)
		if got.Page.Complete {
			if got.Page.NextCursor != "" || got.Status != FindComplete {
				t.Fatalf("last page = %+v", got)
			}
			break
		}
		if got.Status != FindPartial || got.Page.NextCursor == "" {
			t.Fatalf("mid page = %+v", got)
		}
		cursor = got.Page.NextCursor
	}
	if !slices.Equal(seen, []string{"repository:a", "repository:b", "repository:c", "repository:d", "repository:e"}) {
		t.Fatalf("paged ids = %v", seen)
	}
	// Past the end is empty, not an error.
	got, err := lookup.Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository", Cursor: EncodeFindCursor("repository:z")})
	if err != nil || got.Status != FindEmpty {
		t.Fatalf("past end = %+v, %v", got, err)
	}
}

// The scan crosses graph pages: total_known counts admitted nodes only, and
// a kind larger than the scan bound reports truncated instead of complete.
func TestFindListScanAcrossPagesCountsAdmittedAndDisclosesTruncation(t *testing.T) {
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {}}}
	for index := 0; index < 450; index++ {
		slug := "acme/other"
		if index%3 == 0 {
			slug = "acme/mine"
		}
		graph.orgs[orgA].nodes = append(graph.orgs[orgA].nodes, repoNode(fmt.Sprintf("repository:%04d", index), "r", slug))
	}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA, "acme/mine"), FindRequest{Kind: "repository", Limit: MaxFindLimit})
	if err != nil || got.Population.TotalKnown != 150 || got.Population.Returned != 150 || got.Population.Truncated || got.Status != FindComplete {
		t.Fatalf("450-node kind: %+v, %v", got.Population, err)
	}
	huge := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {}}}
	for index := 0; index < MaxFindScanNodes+300; index++ {
		huge.orgs[orgA].nodes = append(huge.orgs[orgA].nodes, repoNode(fmt.Sprintf("repository:%05d", index), "r", "acme/mine"))
	}
	got, err = newLookup(huge, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Kind: "repository", Limit: 10})
	if err != nil || !got.Population.Truncated || got.Status != FindPartial || got.Population.TotalKnown != MaxFindScanNodes {
		t.Fatalf("oversized kind: %+v %s, %v", got.Population, got.Status, err)
	}
}

// Name mode: match classes come from the graph read; more than one admitted
// match is ambiguous; a truncated read is disclosed.
func TestFindNameMatchClassesAmbiguityAndTruncation(t *testing.T) {
	alias := repoNode("repository:b", "acme/b", "acme/b")
	alias.Match = MatchAlias
	provider := projectNode("project.v2:p", "acme/b")
	provider.Match = MatchProviderKey
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {nodes: []LookupNode{alias, provider, repoNode("repository:a", "acme/a", "acme/a")}}}}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Query: "acme/b"})
	if err != nil || got.Status != FindAmbiguous || len(got.Subjects) != 2 {
		t.Fatalf("ambiguous = %+v, %v", got, err)
	}
	if got.Subjects[0].Match != MatchProviderKey || got.Subjects[1].Match != MatchAlias {
		t.Fatalf("match classes = %+v", got.Subjects)
	}
	if got.Population.Kind != "" || got.Population.TotalKnown != 2 {
		t.Fatalf("population = %+v", got.Population)
	}
	graph.truncateName = true
	single, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Query: "acme/a", Kinds: []string{"repository"}})
	if err != nil || single.Status != FindPartial || !single.Population.Truncated || len(single.Subjects) != 1 {
		t.Fatalf("truncated name read = %+v, %v", single, err)
	}
}

// find_subjects calls no model: the graph seam has no embedding or
// interpreter method, and the fakes' model methods panic if ever reached.
func TestFindCallsNoModel(t *testing.T) {
	seam := reflect.TypeOf((*SubjectGraph)(nil)).Elem()
	for index := 0; index < seam.NumMethod(); index++ {
		name := strings.ToLower(seam.Method(index).Name)
		for _, banned := range []string{"embed", "vector", "interpret", "model", "llm", "similar"} {
			if strings.Contains(name, banned) {
				t.Fatalf("SubjectGraph method %s looks like a model call", seam.Method(index).Name)
			}
		}
	}
	graph := threeRepoGraph()
	lookup := newLookup(graph, nil)
	principal := lookupPrincipal(orgA, "acme/a")
	for _, request := range []FindRequest{{Kind: "repository"}, {Query: "acme/a"}, {Query: "nothing", Kinds: []string{"team"}}} {
		if _, err := lookup.Find(context.Background(), principal, request); err != nil { // a model call would panic
			t.Fatal(err)
		}
	}
}

// The telemetry line: tool, closed-vocabulary kinds, counts, status and
// latency; no id, label or query.
func TestFindTelemetryClosedVocabularyOnly(t *testing.T) {
	graph := threeRepoGraph()
	recorder := &lookupRecorder{}
	lookup := newLookup(graph, recorder)
	principal := lookupPrincipal(orgA, "acme/a")
	if _, err := lookup.Find(context.Background(), principal, FindRequest{Kind: "repository"}); err != nil {
		t.Fatal(err)
	}
	_, _ = lookup.Find(context.Background(), principal, FindRequest{Kind: "person"})
	graph.listErr = errors.New("secret-store-detail")
	_, _ = lookup.Find(context.Background(), principal, FindRequest{Kind: "repository"})
	if len(recorder.calls) != 3 {
		t.Fatalf("recorded %d calls, want 3", len(recorder.calls))
	}
	ok, invalid, down := recorder.calls[0], recorder.calls[1], recorder.calls[2]
	if ok.Tool != "find_subjects" || ok.Mode != "list" || ok.Count != 1 || ok.Status != "complete" ||
		!slices.Equal(ok.Kinds, []string{"repository"}) || !slices.Equal(ok.SubjectKinds, []string{"repository"}) {
		t.Fatalf("ok telemetry = %+v", ok)
	}
	if invalid.Status != "invalid_request" || invalid.ErrorClass != "invalid_request" || len(invalid.Kinds) != 0 {
		t.Fatalf("invalid telemetry = %+v", invalid)
	}
	if down.Status != "unavailable" || down.ErrorClass != "graph_error" {
		t.Fatalf("unavailable telemetry = %+v", down)
	}

	var buffer bytes.Buffer
	NewSlogFindRecorder(slog.New(slog.NewJSONHandler(&buffer, nil))).RecordFindSubjects(context.Background(), principal, ok)
	line := buffer.String()
	for _, want := range []string{`"msg":"context fabric direct read"`, `"tool":"find_subjects"`, `"status":"complete"`, `"count":1`, `"latency_ms"`} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %s:\n%s", want, line)
		}
	}
	for _, leak := range []string{"repository:a", "acme/a", "acme/"} {
		if strings.Contains(line, leak) {
			t.Errorf("line leaks %q:\n%s", leak, line)
		}
	}
}

// Name mode pages the matches and gates each page: a visible match behind more
// hidden matches than the old single read held is still found, and a name with
// more matches than the examination bound reports the cut instead of an empty
// answer.
func TestFindNameScanPagesMatchesAndGatesEachPage(t *testing.T) {
	build := func(hidden int) *lookupFakeGraph {
		graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {}}}
		for index := 0; index < hidden; index++ {
			graph.orgs[orgA].nodes = append(graph.orgs[orgA].nodes, repoNode(fmt.Sprintf("repository:h%06d", index), "Shared", "acme/hidden"))
		}
		graph.orgs[orgA].nodes = append(graph.orgs[orgA].nodes, repoNode("repository:zz-visible", "shared", "acme/mine"))
		return graph
	}
	principal := lookupPrincipal(orgA, "acme/mine")

	graph := build(MaxFindScanNodes + 500)
	got, err := newLookup(graph, nil).Find(context.Background(), principal, FindRequest{Query: "shared", Kinds: []string{"repository"}})
	if err != nil || !slices.Equal(ids(got.Subjects), []string{"repository:zz-visible"}) || got.Population.Truncated || got.Status != FindComplete {
		t.Fatalf("visible match behind %d hidden: %+v %s, %v", MaxFindScanNodes+500, got, got.Status, err)
	}
	if graph.nameCalls < (MaxFindScanNodes+500)/MaxLookupPageSize {
		t.Fatalf("name calls = %d: the matches were not paged", graph.nameCalls)
	}

	cut, err := newLookup(build(MaxFindNameScanMatches+MaxLookupPageSize), nil).Find(context.Background(), principal, FindRequest{Query: "shared", Kinds: []string{"repository"}})
	if err != nil || len(cut.Subjects) != 0 || !cut.Population.Truncated || cut.Status != FindPartial {
		t.Fatalf("matches past the examination bound: %+v %s, %v", cut.Population, cut.Status, err)
	}

	absent, err := newLookup(build(10), nil).Find(context.Background(), principal, FindRequest{Query: "nothing", Kinds: []string{"repository"}})
	if err != nil || absent.Status != FindEmpty || absent.Population.Truncated || !absent.Page.Complete {
		t.Fatalf("absent name: %+v %s, %v", absent.Population, absent.Status, err)
	}
}

// More admitted matches than MaxFindScanNodes: the read stops at the bound and
// says so.
func TestFindNameScanAdmittedBoundDisclosesTruncation(t *testing.T) {
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {}}}
	for index := 0; index < MaxFindScanNodes+MaxLookupPageSize; index++ {
		graph.orgs[orgA].nodes = append(graph.orgs[orgA].nodes, repoNode(fmt.Sprintf("repository:m%06d", index), "Shared", "acme/mine"))
	}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Query: "shared", Kinds: []string{"repository"}, Limit: 10})
	if err != nil || !got.Population.Truncated || got.Population.TotalKnown > MaxFindScanNodes+MaxLookupPageSize-1 || len(got.Subjects) != 10 {
		t.Fatalf("admitted bound: %+v, %v", got.Population, err)
	}
}
