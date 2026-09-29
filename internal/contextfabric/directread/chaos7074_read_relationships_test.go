package directread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fakeEdgeGraph is an organization graph of nodes (the subject gate's
// fakeGraph) and edges. DirectEdgePage applies the SAME contract the
// FalkorDB query does -- origins, direction, types, exclusion, strict keyset
// on the relationship id, Limit+1 -- so the reader's paging logic is tested
// here and the Cypher is tested live (chaos7074 live tests in falkorgraph).
type fakeEdgeGraph struct {
	*fakeGraph
	edges     []EdgeCandidate
	pageCalls int
	pageErr   error
	queries   []EdgePageQuery
}

func (g *fakeEdgeGraph) DirectEdgePage(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, query EdgePageQuery) (EdgePage, error) {
	g.pageCalls++
	g.queries = append(g.queries, query)
	if g.pageErr != nil {
		return EdgePage{}, g.pageErr
	}
	if principal.OrgID != g.org {
		return EdgePage{}, nil
	}
	origins := map[string]bool{}
	for _, origin := range query.Origins {
		origins[graphrank.SubjectKey(origin)] = true
	}
	types := map[string]bool{}
	for _, t := range query.Types {
		types[t] = true
	}
	var matched []EdgeCandidate
	for _, e := range g.edges {
		from, to := graphrank.SubjectKey(e.From.Subject), graphrank.SubjectKey(e.To.Subject)
		hit := false
		switch query.Direction {
		case EdgeDirectionOut:
			hit = origins[from]
		case EdgeDirectionIn:
			hit = origins[to]
		default:
			hit = origins[from] || origins[to]
		}
		if !hit || (len(types) > 0 && !types[e.RelationType]) {
			continue
		}
		if query.Exclude != nil {
			x := graphrank.SubjectKey(*query.Exclude)
			if from == x || to == x {
				continue
			}
		}
		if query.After != nil && !query.After.Less(e.Key) {
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Key.Less(matched[j].Key) })
	page := EdgePage{}
	if len(matched) > query.Limit {
		matched, page.More = matched[:query.Limit], true
	}
	page.Edges = matched
	return page, nil
}

type relRecorder struct {
	reads []RelationshipsReadRecord
}

func (r *relRecorder) RecordDirectRelationshipsRead(_ context.Context, _ storage.Principal, record RelationshipsReadRecord) {
	r.reads = append(r.reads, record)
}

func edgeBetween(id, relation string, from, to contextfabric.SubjectRef, attributes map[string]interface{}) EdgeCandidate {
	attrs := map[string]interface{}{"relationship_id": id, "relation_type": relation, "authorization_repositories": []string{"acme/a"}}
	for k, v := range attributes {
		attrs[k] = v
	}
	return EdgeCandidate{
		Key:          EdgeKey{RelationshipID: id},
		RelationType: relation, Attributes: attrs,
		From: EdgeEnd{Subject: from}, To: EdgeEnd{Subject: to},
	}
}

func relCtx(id string) context.Context {
	sum := sha256.Sum256([]byte(id))
	return observability.WithRequestID(context.Background(), "req_"+hex.EncodeToString(sum[:16]))
}

var (
	restrictedA  = storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/a"}}
	unrestricted = storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c"}
)

func newRelReader(graph *fakeEdgeGraph, now *time.Time) (*RelationshipsReader, *relRecorder) {
	recorder := &relRecorder{}
	reader, err := NewRelationshipsReader(NewSubjectGate(graph, nil), graph, recorder, testCursorKeyring())
	if err != nil {
		panic(err)
	}
	if now != nil {
		reader.now = func() time.Time { return *now }
		reader.gate.now = reader.now
	}
	return reader, recorder
}

// readAll follows next_cursor to the end, one fresh request id per page (a
// page is a request).
func readAll(t *testing.T, reader *RelationshipsReader, principal storage.Principal, request RelationshipsRequest) ([]RelationshipsResponse, []string) {
	t.Helper()
	var pages []RelationshipsResponse
	var ids []string
	for page := 0; page < 100; page++ {
		response, err := reader.Read(relCtx(fmt.Sprint(page)), principal, request)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		pages = append(pages, response)
		for _, e := range response.Edges {
			ids = append(ids, e.RelationshipID)
		}
		if response.Page.Complete {
			return pages, ids
		}
		if response.Page.NextCursor == "" {
			t.Fatalf("page %d is not complete and has no cursor", page)
		}
		request.Cursor = response.Page.NextCursor
	}
	t.Fatal("walk did not finish in 100 pages")
	return nil, nil
}

// hubGraph: team T with 250 OWNED_BY_TEAM edges in from repositories; 40 of
// them (every sixth) come from repository nodes of acme/b, which a caller
// restricted to acme/a cannot see. Relationship ids are not in insertion
// order.
func hubGraph() *fakeEdgeGraph {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	for i := 0; i < 250; i++ {
		repo := subject(contractsv1.ContextFabricSubjectRepository, fmt.Sprintf("repository:r%03d", i))
		slug := "acme/a"
		if i%6 == 0 {
			slug = "acme/b"
		}
		graph.nodes[graphrank.SubjectKey(repo)] = repos(slug)
		id := fmt.Sprintf("rel-%03d", (i*37)%250)
		graph.edges = append(graph.edges, edgeBetween(id, "OWNED_BY_TEAM", repo, teamT, map[string]interface{}{"authorization_repositories": []string{slug}}))
	}
	return graph
}

// T5 (design J.5): pages join to the full visible set; no duplicate, no
// gap; the walk ends. Rule 1 = the state: the union of pages IS the set of
// visible edges.
func TestChaos7074_T5_PagesJoinToTheFullVisibleSet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		visible   func(i int) bool
	}{
		{"unrestricted", unrestricted, func(int) bool { return true }},
		{"restricted to acme/a", restrictedA, func(i int) bool { return i%6 != 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := hubGraph()
			reader, _ := newRelReader(graph, nil)
			pages, ids := readAll(t, reader, tc.principal, RelationshipsRequest{
				Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Types: []string{"OWNED_BY_TEAM"}, Direction: "in", Limit: 100,
			})
			want := map[string]int{}
			for i, e := range graph.edges {
				if tc.visible(i) {
					want[e.Key.RelationshipID+"|"+e.From.Subject.CanonicalID]++
				}
			}
			got := map[string]int{}
			withheld := 0
			for _, page := range pages {
				withheld += page.Withheld.EdgesNotVisible
				for _, e := range page.Edges {
					got[e.RelationshipID+"|"+e.From.CanonicalID]++
				}
			}
			for key, n := range got {
				if n != 1 {
					t.Fatalf("edge %s served %d times", key, n)
				}
				if want[key] != 1 {
					t.Fatalf("edge %s served but not visible", key)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("served %d edges, want %d (gap)", len(got), len(want))
			}
			if withheld != len(graph.edges)-len(want) {
				t.Fatalf("withheld %d, want %d", withheld, len(graph.edges)-len(want))
			}
			if len(pages) != 3 || pages[len(pages)-1].Status != RelationshipsComplete {
				t.Fatalf("pages=%d last status=%s", len(pages), pages[len(pages)-1].Status)
			}
			_ = ids
		})
	}
}

// T5 rule 2 plant, in the reader: a next position taken from the last
// SERVED edge instead of the last EXAMINED edge re-reads a withheld tail. A
// page whose whole tail is withheld must still advance.
func TestChaos7074_T5_PositionAdvancesPastAWithheldTail(t *testing.T) {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	for i := 0; i < 10; i++ {
		repo := subject(contractsv1.ContextFabricSubjectRepository, fmt.Sprintf("repository:w%02d", i))
		slug := "acme/a"
		if i >= 3 {
			slug = "acme/b" // everything after the third edge is withheld
		}
		graph.nodes[graphrank.SubjectKey(repo)] = repos(slug)
		graph.edges = append(graph.edges, edgeBetween(fmt.Sprintf("rel-%02d", i), "OWNED_BY_TEAM", repo, teamT, nil))
	}
	reader, _ := newRelReader(graph, nil)
	pages, ids := readAll(t, reader, restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Limit: 4})
	if len(ids) != 3 || len(pages) != 3 {
		t.Fatalf("ids=%v pages=%d", ids, len(pages))
	}
	if pages[1].Page.Returned != 0 || pages[1].Page.Examined != 4 || pages[1].Withheld.EdgesNotVisible != 4 {
		t.Fatalf("second page = %+v withheld=%+v", pages[1].Page, pages[1].Withheld)
	}
}

func testCursorKeyring() CursorKeyring {
	return CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}}
}

func decodeCursorForTest(t *testing.T, sealer *cursorSealer, token string) relationshipsCursor {
	t.Helper()
	raw, err := sealer.open(token)
	if err != nil {
		t.Fatal(err)
	}
	var c relationshipsCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// T5 rule 3: one case per cursor binding clause. Each refusal is a
// different outcome, so a mutation that drops one clause is killed by its
// own case.
func TestChaos7074_T5_CursorBindings(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	graph := hubGraph()
	reader, recorder := newRelReader(graph, &now)
	request := RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Direction: "in", Limit: 100}
	first, err := reader.Read(relCtx("p0"), unrestricted, request)
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("first page: %v %+v", err, first.Page)
	}
	good := first.Page.NextCursor
	reencode := func(mutate func(*relationshipsCursor)) string {
		c := decodeCursorForTest(t, reader.sealer, good)
		mutate(&c)
		token, err := encodeRelationshipsCursor(reader.sealer, c)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	cases := []struct {
		name      string
		principal storage.Principal
		request   func() RelationshipsRequest
		advance   time.Duration
		reason    string
		outcome   CursorOutcome
	}{
		{"org binding", storage.Principal{OrgID: orgB, Subject: "u", CredentialID: "c"}, func() RelationshipsRequest { r := request; r.Cursor = good; return r }, 0, RelationshipsRefusalInvalidCursor, CursorForeignOrg},
		{"request digest", unrestricted, func() RelationshipsRequest { r := request; r.Direction = "both"; r.Cursor = good; return r }, 0, RelationshipsRefusalInvalidCursor, CursorStale},
		{"expiry", unrestricted, func() RelationshipsRequest { r := request; r.Cursor = good; return r }, RelationshipsCursorTTL, RelationshipsRefusalExpiredCursor, CursorExpired},
		{"version", unrestricted, func() RelationshipsRequest {
			r := request
			r.Cursor = reencode(func(c *relationshipsCursor) { c.Version = 2 })
			return r
		}, 0, RelationshipsRefusalInvalidCursor, CursorInvalid},
		{"hop beyond depth", unrestricted, func() RelationshipsRequest {
			r := request
			r.Cursor = reencode(func(c *relationshipsCursor) { c.Hop = 2 })
			return r
		}, 0, RelationshipsRefusalInvalidCursor, CursorInvalid},
		{"garbage", unrestricted, func() RelationshipsRequest { r := request; r.Cursor = "!!"; return r }, 0, RelationshipsRefusalInvalidCursor, CursorInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).Add(tc.advance)
			recorder.reads = nil
			calls := graph.pageCalls
			_, err := reader.Read(relCtx("c-"+tc.name), tc.principal, tc.request())
			var requestError *RelationshipsRequestError
			if !errors.As(err, &requestError) || requestError.Reason != tc.reason || requestError.Cursor != tc.outcome {
				t.Fatalf("err = %v (%+v), want reason %s outcome %s", err, requestError, tc.reason, tc.outcome)
			}
			if graph.pageCalls != calls {
				t.Fatal("a refused cursor read the graph")
			}
			if len(recorder.reads) != 1 || recorder.reads[0].CursorIn != tc.outcome || recorder.reads[0].Status != RelationshipsInvalid {
				t.Fatalf("recorded %+v", recorder.reads)
			}
		})
	}
	// The same cursor, in time, for the same caller and request: accepted.
	now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).Add(RelationshipsCursorTTL - time.Second)
	r := request
	r.Cursor = good
	if _, err := reader.Read(relCtx("ok"), unrestricted, r); err != nil {
		t.Fatalf("valid cursor refused: %v", err)
	}
}

// T15 (design J.5): two visible nodes, one edge whose own attributes exclude
// the caller: the edge is absent and counted. Plant: an end-node check only.
// Rule 3: one case per clause (edge, source, target).
func TestChaos7074_T15_EdgeGate(t *testing.T) {
	cases := []struct {
		name     string
		edge     map[string]interface{}
		from, to contextfabric.SubjectRef
		want     EdgeWithheldReason
	}{
		{"edge attributes exclude the caller", map[string]interface{}{"authorization_repositories": []string{"acme/b"}}, repoA, teamT, EdgeWithheldAttributes},
		{"source not visible", nil, repoB, teamT, EdgeWithheldSource},
		{"target not visible", nil, repoA, projectP, EdgeWithheldTarget},
		{"all three pass", map[string]interface{}{"authorization_repositories": []string{"acme/a"}}, repoA, teamT, EdgeVisible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
			graph.edges = []EdgeCandidate{edgeBetween("rel-1", "OWNED_BY_TEAM", tc.from, tc.to, tc.edge)}
			root := tc.from
			if tc.want == EdgeWithheldSource {
				root = tc.to
			}
			reader, recorder := newRelReader(graph, nil)
			response, err := reader.Read(relCtx("t15"), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: string(root.Kind), CanonicalID: root.CanonicalID}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == EdgeVisible {
				if len(response.Edges) != 1 || response.Withheld.EdgesNotVisible != 0 {
					t.Fatalf("visible edge not served: %+v", response)
				}
				return
			}
			if len(response.Edges) != 0 || response.Withheld.EdgesNotVisible != 1 {
				t.Fatalf("withheld edge served: %+v", response)
			}
			if got := recorder.reads[0].EdgesWithheld[tc.want]; got != 1 {
				t.Fatalf("withheld reasons = %v, want %s", recorder.reads[0].EdgesWithheld, tc.want)
			}
			encoded, _ := json.Marshal(response)
			if strings.Contains(string(encoded), "rel-1") {
				t.Fatalf("withheld edge id leaked: %s", encoded)
			}
		})
	}
}

// The pure gate, clause by clause (unrestricted callers pass the attribute
// clause; the node clauses still apply to them).
func TestChaos7074_EdgeGateClauses(t *testing.T) {
	deny := map[string]interface{}{"authorization_repositories": []string{"acme/b"}}
	if EdgeGate(restrictedA, deny, true, true) != EdgeWithheldAttributes {
		t.Fatal("edge clause")
	}
	open := map[string]interface{}{"authorization_repositories": []string{"acme/a"}}
	if EdgeGate(restrictedA, open, false, true) != EdgeWithheldSource {
		t.Fatal("source clause")
	}
	if EdgeGate(restrictedA, open, true, false) != EdgeWithheldTarget {
		t.Fatal("target clause")
	}
	if EdgeGate(unrestricted, deny, false, true) != EdgeWithheldSource {
		t.Fatal("unrestricted caller still needs visible ends")
	}
	if EdgeGate(restrictedA, open, true, true) != EdgeVisible {
		t.Fatal("granted edge with visible ends")
	}
	// CHAOS-7080 (#691): a "*" edge proves no repository, so a restricted
	// caller does not see it; an unrestricted caller does.
	wildcard := map[string]interface{}{"authorization_repositories": "*"}
	if EdgeGate(restrictedA, wildcard, true, true) != EdgeWithheldAttributes || EdgeGate(unrestricted, wildcard, true, true) != EdgeVisible {
		t.Fatal("wildcard edge")
	}
}

// Depth 2: hop 2 continues ONLY from neighbours reached through a visible
// edge, never repeats hop-1 edges, and the gates run on hop 2 too.
func TestChaos7074_DepthTwoWalksOnlyThroughVisibleEdges(t *testing.T) {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	hidden := subject(contractsv1.ContextFabricSubjectRepository, "repository:h")
	far := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:far")
	graph.nodes[graphrank.SubjectKey(hidden)] = repos("acme/b")
	graph.nodes[graphrank.SubjectKey(far)] = repos("acme/a")
	graph.edges = []EdgeCandidate{
		edgeBetween("e1", "OWNED_BY_TEAM", repoA, teamT, nil),         // hop 1, visible
		edgeBetween("e2", "OWNED_BY_TEAM", hidden, teamT, nil),        // hop 1, hidden end
		edgeBetween("e3", "BELONGS_TO_REPOSITORY", workA, repoA, nil), // hop 2 via repoA, visible
		edgeBetween("e4", "BELONGS_TO_REPOSITORY", far, hidden, nil),  // hop 2 via hidden: must never be read
		edgeBetween("e5", "BELONGS_TO_REPOSITORY", workB, repoA, nil), // hop 2, workB hidden
	}
	reader, _ := newRelReader(graph, nil)
	pages, ids := readAll(t, reader, restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Depth: 2})
	if strings.Join(ids, ",") != "e1,e3" {
		t.Fatalf("ids = %v", ids)
	}
	if len(pages) != 2 || pages[0].Effective.Hop != 1 || pages[1].Effective.Hop != 2 || pages[1].Edges[0].Hop != 2 {
		t.Fatalf("hops: %+v", pages)
	}
	if pages[0].Withheld.EdgesNotVisible != 1 || pages[1].Withheld.EdgesNotVisible != 1 {
		t.Fatalf("withheld: %+v %+v", pages[0].Withheld, pages[1].Withheld)
	}
	for _, q := range graph.queries {
		for _, origin := range q.Origins {
			if origin == hidden {
				t.Fatal("hop 2 read from a node reached only through a withheld edge")
			}
		}
	}
}

func TestChaos7074_RootGate(t *testing.T) {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	graph.edges = []EdgeCandidate{edgeBetween("e1", "OWNED_BY_TEAM", repoB, teamU, nil)}
	reader, _ := newRelReader(graph, nil)
	for _, root := range []contextfabric.SubjectRef{repoB, guessed, teamU} {
		response, err := reader.Read(relCtx("root"), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: string(root.Kind), CanonicalID: root.CanonicalID}})
		if err != nil || response.Status != RelationshipsDenied || response.Reason != RelationshipsRefusalDeniedOrNotFound || len(response.Edges) != 0 {
			t.Fatalf("%s: %v %+v", root.CanonicalID, err, response)
		}
	}
	if graph.pageCalls != 0 {
		t.Fatal("a refused root read edges")
	}
	// Other organization: the same id is absent.
	other := storage.Principal{OrgID: orgB, Subject: "u", CredentialID: "c"}
	if response, _ := reader.Read(relCtx("org"), other, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}}); response.Status != RelationshipsDenied {
		t.Fatalf("foreign org: %+v", response)
	}
}

func TestChaos7074_FailsClosed(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		mut  func(*fakeEdgeGraph)
	}{
		{"root gate", func(g *fakeEdgeGraph) { g.authErr = boom }},
		{"edge page", func(g *fakeEdgeGraph) { g.pageErr = boom }},
		{"end node reach", func(g *fakeEdgeGraph) {
			g.reachErr = boom
			g.edges[0] = edgeBetween("e1", "OWNED_BY_TEAM", repoA, teamT, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA(), edges: []EdgeCandidate{edgeBetween("e1", "BELONGS_TO_REPOSITORY", workA, repoA, nil)}}
			tc.mut(graph)
			reader, _ := newRelReader(graph, nil)
			response, err := reader.Read(relCtx("fc"), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}})
			if !errors.Is(err, ErrRelationshipsUnavailable) || len(response.Edges) != 0 {
				t.Fatalf("err=%v response=%+v", err, response)
			}
		})
	}
	var nilReader *RelationshipsReader
	if _, err := nilReader.Read(relCtx("nil"), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}}); !errors.Is(err, ErrRelationshipsUnavailable) {
		t.Fatalf("nil reader: %v", err)
	}
}

// Evidence references that name a subject the caller cannot see are removed
// and counted (K2 extended); row-level references reach an unrestricted
// caller only.
func TestChaos7074_EvidenceRefsNamingUnseenSubjectsAreRemoved(t *testing.T) {
	refs := []string{
		contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "a"),
		contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "b"),
		contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "x"),
	}
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		want      int
		withheld  int
	}{{"restricted", restrictedA, 1, 2}, {"unrestricted", unrestricted, 3, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA(), edges: []EdgeCandidate{edgeBetween("e1", "BELONGS_TO_REPOSITORY", workA, repoA, map[string]interface{}{"evidence_refs": refs})}}
			reader, _ := newRelReader(graph, nil)
			response, err := reader.Read(relCtx("ev"), tc.principal, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}})
			if err != nil || len(response.Edges) != 1 {
				t.Fatalf("%v %+v", err, response)
			}
			if got := response.Edges[0].Provenance.EvidenceRefIDs; len(got) != tc.want || response.Withheld.EvidenceRefs != tc.withheld {
				t.Fatalf("refs=%v withheld=%d", got, response.Withheld.EvidenceRefs)
			}
		})
	}
}

func TestChaos7074_RequestValidation(t *testing.T) {
	reader, _ := newRelReader(&fakeEdgeGraph{fakeGraph: graphOfOrgA()}, nil)
	base := RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}
	for name, request := range map[string]RelationshipsRequest{
		"no subject": {},
		"bad kind":   {Subject: RelationshipsSubject{Kind: "person", CanonicalID: "x"}},
		"bad type":   {Subject: base, Types: []string{"MANAGES"}},
		"bad dir":    {Subject: base, Direction: "sideways"},
		"depth 3":    {Subject: base, Depth: 3},
		"limit 101":  {Subject: base, Limit: 101},
		"limit -1":   {Subject: base, Limit: -1},
		"bad as_of":  {Subject: base, AsOf: "yesterday"},
	} {
		if _, err := reader.Read(relCtx(name), unrestricted, request); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The proof the root gate issues is spent by this read: a second read in the
// same request id takes a new decision (and the page query ran once per
// read).
func TestChaos7074_EveryPageTakesAFreshRootDecision(t *testing.T) {
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA(), edges: []EdgeCandidate{edgeBetween("e1", "BELONGS_TO_REPOSITORY", workA, repoA, nil)}}
	reader, _ := newRelReader(graph, nil)
	before := graph.authCalls
	for i := 0; i < 2; i++ {
		if _, err := reader.Read(relCtx("same"), restrictedA, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}}); err != nil {
			t.Fatal(err)
		}
	}
	// Two reads x (root decision + end-node decision).
	if graph.authCalls-before != 4 {
		t.Fatalf("auth calls = %d, want 4", graph.authCalls-before)
	}
}
