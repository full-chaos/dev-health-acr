package falkorgraph

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// servedEdgeFixture is one in-process graph for CHAOS-12537: edges out of one
// origin, each with its own evidence ref, and the endpoint nodes. It answers
// the walk's reads (the edge read, the endpoint reads) and the direct read of
// served edges from the SAME data, so the two can be compared.
type servedEdgeFixture struct {
	edges map[string]row      // by relationship id
	nodes map[string][]string // canonical id -> authorization_repositories; absent = no node
	// servedRows counts the rows the direct read returned; servedErr fails it;
	// nodeErr fails the endpoint read of that canonical id.
	servedErr error
	nodeErr   string
	reads     []string
}

const servedEdgeRepo = "full-chaos/dev-health-acr"

func newServedEdgeFixture() *servedEdgeFixture {
	rel := func(id, relation, target string, repos []string) row {
		return row{
			"r": &edge{Properties: map[string]interface{}{
				propRelationType: relation, propRelationshipID: id,
				propEvidenceRefs: []string{"evidence_" + id}, "authorization_repositories": repos,
			}},
			"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": target,
		}
	}
	return &servedEdgeFixture{
		edges: map[string]row{
			// Admitted by both.
			"rel_ok": rel("rel_ok", "BLOCKS", "w_ok", []string{servedEdgeRepo}),
			// The edge gate refuses it.
			"rel_edge_denied": rel("rel_edge_denied", "BLOCKS", "w_edge_denied", []string{"other/private"}),
			// An endpoint gate refuses it.
			"rel_end_denied": rel("rel_end_denied", "BLOCKS", "w_end_denied", []string{servedEdgeRepo}),
			// The endpoint is gone (outside the window, or removed).
			"rel_end_gone": rel("rel_end_gone", "BLOCKS", "w_end_gone", []string{servedEdgeRepo}),
			// The source endpoint's gate refuses it. It is not an edge of the
			// origin, so only the direct read is asked about it.
			"rel_src_denied": {
				"r": &edge{Properties: map[string]interface{}{
					propRelationType: "BLOCKS", propRelationshipID: "rel_src_denied",
					propEvidenceRefs: []string{"evidence_rel_src_denied"}, "authorization_repositories": []string{servedEdgeRepo},
				}},
				"srcKind": "project", "srcId": "p_denied", "dstKind": "work_item", "dstId": "w_ok",
			},
			// Admitted by both gates; the walk's budget can cut it.
			"rel_cut": rel("rel_cut", "BLOCKS", "w_cut", []string{servedEdgeRepo}),
		},
		nodes: map[string][]string{
			"p1": {servedEdgeRepo}, "p_denied": {"other/private"}, "w_ok": {servedEdgeRepo}, "w_edge_denied": {servedEdgeRepo},
			"w_end_denied": {"other/private"}, "w_cut": {servedEdgeRepo},
		},
	}
}

func (f *servedEdgeFixture) conn(t *testing.T) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "UNWIND $pairs"):
			f.reads = append(f.reads, "served")
			if f.servedErr != nil {
				return nil, f.servedErr
			}
			var rows []row
			for _, pair := range params["pairs"].([]interface{}) {
				p := pair.(map[string]interface{})
				for _, id := range slices.Sorted(mapKeys(f.edges)) {
					r := f.edges[id]
					if r["srcKind"] == p["fk"] && r["srcId"] == p["fi"] && r["dstKind"] == p["tk"] && r["dstId"] == p["ti"] {
						rows = append(rows, r)
					}
				}
			}
			if limit, ok := params["lim"].(int); ok && len(rows) > limit {
				rows = rows[:limit]
			}
			return rows, nil
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			var rows []row
			for _, id := range slices.Sorted(mapKeys(f.edges)) {
				if f.edges[id]["srcId"] == "p1" {
					rows = append(rows, f.edges[id])
				}
			}
			return rows, nil
		default: // nodeByKindID
			id, _ := params["id"].(string)
			f.reads = append(f.reads, "node:"+id)
			if id == f.nodeErr {
				return nil, errors.New("endpoint read failed")
			}
			repos, ok := f.nodes[id]
			if !ok {
				return nil, nil
			}
			kind := "work_item"
			if strings.HasPrefix(id, "p") {
				kind = "project"
			}
			r := fakeSubjectNodeRow(kind, id, id)
			r["n"].(*node).Properties["authorization_repositories"] = repos
			return []row{r}, nil
		}
	}}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func servedEdgePrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-1", RepositoryScopes: []string{servedEdgeRepo}}
}

// storedEdgesOf is the stored answer's served edges: one per fixture edge,
// from the origin to its target, as AdmitEdges wrote them.
func storedEdgesOf(f *servedEdgeFixture) []contextfabric.RelationshipEdge {
	edges := make([]contextfabric.RelationshipEdge, 0, len(f.edges))
	for _, id := range slices.Sorted(mapKeys(f.edges)) {
		source, target := f.edges[id]["srcId"].(string), f.edges[id]["dstId"].(string)
		edges = append(edges, contextfabric.RelationshipEdge{
			From: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: source}, To: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: target},
			EvidenceRefIDs: []string{"evidence_" + id},
		})
	}
	return edges
}

func servedEdgeRequest() contextfabric.GraphDiscoveryRequest {
	return fakeDiscoveryRequest(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}, 10)
}

// TestTheDirectReadAgreesWithTheWalkOnEveryGate (CHAOS-12537) is the
// differential on one in-process store: the walk and the direct read of the
// served edges read the same edges and nodes. On every gate they agree: the
// edge the gates admit is proven by both, and the edge gate, an endpoint gate
// and a gone endpoint each refuse in both. The ONE allowed difference is named:
// an edge the walk's collect budget cut is proven by the direct read.
func TestTheDirectReadAgreesWithTheWalkOnEveryGate(t *testing.T) {
	t.Parallel()
	gateAdmitted := []string{"evidence_rel_cut", "evidence_rel_ok"}
	for _, tc := range []struct {
		name       string
		maxResults int
	}{
		{name: "the walk reads every edge", maxResults: 25},
		{name: "the walk's budget cuts an edge", maxResults: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newServedEdgeFixture()
			walker, err := newWithAPI(Config{
				Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: tc.maxResults, PoolSize: 1, AllowInsecure: true,
			}, fixture.conn(t))
			if err != nil {
				t.Fatalf("newWithAPI() error = %v", err)
			}
			walked, err := walker.DiscoverContext(context.Background(), servedEdgePrincipal(), servedEdgeRequest())
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			direct := newFakeAdapter(t, fixture.conn(t))
			proof, err := direct.ServedEdgeEvidence(context.Background(), servedEdgePrincipal(), servedEdgeRequest(), storedEdgesOf(fixture))
			if err != nil || proof.Cut {
				t.Fatalf("ServedEdgeEvidence() = cut %t, error %v", proof.Cut, err)
			}
			byRead := slices.Sorted(slices.Values(proof.Context.EvidenceRefIDs))
			byWalk := slices.Sorted(slices.Values(walked.EvidenceRefIDs))
			if !slices.Equal(byRead, gateAdmitted) {
				t.Fatalf("direct read proved %v, want exactly %v: the edge gate, an endpoint gate and a gone endpoint each refuse", byRead, gateAdmitted)
			}
			for _, ref := range byWalk {
				if !slices.Contains(byRead, ref) {
					t.Fatalf("the walk proves %s and the direct read does not: the direct read must never refuse what the walk admits", ref)
				}
			}
			if tc.maxResults == 25 {
				if !slices.Equal(byWalk, gateAdmitted) {
					t.Fatalf("walk proved %v, want %v: with no budget cut the two must agree exactly", byWalk, gateAdmitted)
				}
				return
			}
			// The ONE allowed difference: what the walk's budget cut. It must
			// exist here, or this case tests nothing, and it must be an edge
			// the gates admit.
			if len(byWalk) >= len(byRead) {
				t.Fatalf("the walk's budget cut nothing (walk %v): this case does not test the named difference", byWalk)
			}
			t.Logf("budget cut: the walk proved %v, the direct read %v", byWalk, byRead)
		})
	}
}

// TestTheDirectReadReadsOnlyTheServedPairs (CHAOS-12537): one keyed read over
// the distinct served pairs, in the stored direction; a blank endpoint is not
// read; no pair means no read at all.
func TestTheDirectReadReadsOnlyTheServedPairs(t *testing.T) {
	t.Parallel()
	from := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1"}
	to := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "w_ok"}
	pairs := servedEdgePairs([]contextfabric.RelationshipEdge{
		{From: from, To: to}, {From: from, To: to}, {From: to, To: from}, {From: from, To: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem}},
	})
	want := []interface{}{
		map[string]interface{}{"fk": "project", "fi": "p1", "tk": "work_item", "ti": "w_ok"},
		map[string]interface{}{"fk": "work_item", "fi": "w_ok", "tk": "project", "ti": "p1"},
	}
	if len(pairs) != len(want) {
		t.Fatalf("pairs = %v, want %v", pairs, want)
	}
	for index := range want {
		got, wanted := pairs[index].(map[string]interface{}), want[index].(map[string]interface{})
		for key, value := range wanted {
			if got[key] != value {
				t.Fatalf("pair %d = %v, want %v", index, got, wanted)
			}
		}
	}
	fixture := newServedEdgeFixture()
	proof, err := newFakeAdapter(t, fixture.conn(t)).ServedEdgeEvidence(context.Background(), servedEdgePrincipal(), servedEdgeRequest(), nil)
	if err != nil || proof.Cut || len(proof.Context.EvidenceRefIDs) != 0 || len(fixture.reads) != 0 {
		t.Fatalf("no served edge: proof %+v, error %v, reads %v; want nothing read", proof, err, fixture.reads)
	}
	start, end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	windowed := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end})
	if !windowed.active || !strings.Contains(servedEdgesCypher(windowed), windowed.predicate("r")) || windowed.predicate("r") == "" {
		t.Fatal("a windowed served edge read does not apply the edge's valid-time predicate")
	}
	cypher := servedEdgesCypher(newTemporalFilter(contextfabric.TimeContext{}))
	for _, part := range []string{"UNWIND $pairs AS p", "(a:Subject {org_id:$org, subject_kind:p.fk, canonical_id:p.fi})-[r:Relates]->(b:Subject {org_id:$org, subject_kind:p.tk, canonical_id:p.ti})", "LIMIT $lim"} {
		if !strings.Contains(cypher, part) {
			t.Fatalf("served edge read %q has no %q", cypher, part)
		}
	}
}

// TestTheDirectReadProvesNothingWhenItCannot (CHAOS-12537): a read at its row
// limit is Cut, and a failed read or a failed endpoint read is an error, never
// a quiet proof of less.
func TestTheDirectReadProvesNothingWhenItCannot(t *testing.T) {
	t.Parallel()
	t.Run("cut", func(t *testing.T) {
		t.Parallel()
		fixture := newServedEdgeFixture()
		for index := 0; index <= servedEdgeReadLimit; index++ {
			id := "rel_many_" + strings.Repeat("x", 3) + time.Duration(index).String()
			fixture.edges[id] = fixture.edges["rel_ok"]
		}
		toOK := slices.IndexFunc(storedEdgesOf(fixture), func(e contextfabric.RelationshipEdge) bool { return e.To.CanonicalID == "w_ok" })
		proof, err := newFakeAdapter(t, fixture.conn(t)).ServedEdgeEvidence(context.Background(), servedEdgePrincipal(), servedEdgeRequest(), storedEdgesOf(fixture)[toOK:toOK+1])
		if err != nil || !proof.Cut || len(proof.Context.EvidenceRefIDs) != 0 {
			t.Fatalf("a read at its limit: proof %+v, error %v; want Cut and nothing proven", proof, err)
		}
	})
	t.Run("the read fails", func(t *testing.T) {
		t.Parallel()
		fixture := newServedEdgeFixture()
		fixture.servedErr = errors.New("graph read failed")
		if _, err := newFakeAdapter(t, fixture.conn(t)).ServedEdgeEvidence(context.Background(), servedEdgePrincipal(), servedEdgeRequest(), storedEdgesOf(fixture)); err == nil {
			t.Fatal("a failed read proved something")
		}
	})
	t.Run("an endpoint read fails", func(t *testing.T) {
		t.Parallel()
		fixture := newServedEdgeFixture()
		fixture.nodeErr = "w_ok"
		if _, err := newFakeAdapter(t, fixture.conn(t)).ServedEdgeEvidence(context.Background(), servedEdgePrincipal(), servedEdgeRequest(), storedEdgesOf(fixture)); err == nil {
			t.Fatal("a failed endpoint read proved something")
		}
	})
	t.Run("no organization", func(t *testing.T) {
		t.Parallel()
		if _, err := newFakeAdapter(t, newServedEdgeFixture().conn(t)).ServedEdgeEvidence(context.Background(), storage.Principal{}, servedEdgeRequest(), storedEdgesOf(newServedEdgeFixture())); !errors.Is(err, contextfabric.ErrUnavailable) {
			t.Fatalf("no organization: error %v, want ErrUnavailable", err)
		}
	})
}
