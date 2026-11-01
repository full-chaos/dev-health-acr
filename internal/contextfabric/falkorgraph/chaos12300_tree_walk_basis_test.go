package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const treeGrantedProject = "project.v2:linear:P-1"

func treeProjectOnly() storage.Principal {
	return storage.Principal{OrgID: "org_1", ProjectScopes: []string{treeGrantedProject}}
}

func unstatedWorkItem() *node {
	return &node{Properties: map[string]interface{}{propKind: "work_item", propCanonicalID: "work_item:unstated"}}
}

func foreignListEdge() *edge {
	return &edge{Properties: map[string]interface{}{"authorization_projects": []string{"project.v2:linear:OTHER"}}}
}

// assertBasisSums pins that the classified parts and the unclassified rest
// the line writes add up to the walk's authz count.
func assertBasisSums(t *testing.T, walk *treeWalk, wantUnclassified int) {
	t.Helper()
	if got := walk.filters.Authz - walk.filters.Basis.classified(); got != wantUnclassified {
		t.Fatalf("authz %d, classified %d: %d unclassified, want %d", walk.filters.Authz, walk.filters.Basis.classified(), got, wantUnclassified)
	}
}

// The #1850 r1 F1 repro, through the production Info recorder: a project-only
// denial that only a tree walk reported takes the project-only line, not the
// legacy one, and the unclassified rest keeps the parts adding up to authz.
func TestATreeWalkOnlyDenialWritesTheProjectOnlyLine(t *testing.T) {
	walk := &treeWalk{}
	treeWalkState{out: walk, principal: treeProjectOnly()}.deny()
	line := edgeFilterLine(t, walk.filters.Authz, walk.filters.Basis)
	if line["grant_class"] != "project_only" || line["authz"] != float64(1) || line["authz_unclassified"] != float64(1) {
		t.Fatalf("line = %v, want grant_class project_only, authz 1, authz_unclassified 1", line)
	}
	for _, key := range []string{"authz_endpoint", "authz_attribution_relation", "authz_project_list", "authz_project_narrowing"} {
		if line[key] != float64(0) {
			t.Fatalf("%s = %v, want 0: %v", key, line[key], line)
		}
	}
}

// Every other class keeps the three counts alone.
func TestATreeWalkDenialOfAnotherClassKeepsTheLegacyLine(t *testing.T) {
	walk := &treeWalk{}
	treeWalkState{out: walk, principal: storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/a"}}}.deny()
	if walk.filters.Basis != (EdgeAuthzBasis{}) {
		t.Fatalf("basis %+v, want none", walk.filters.Basis)
	}
	if line := edgeFilterLine(t, walk.filters.Authz, walk.filters.Basis); line["grant_class"] != nil {
		t.Fatalf("line = %v, want the legacy shape", line)
	}
}

// Another class's refusals classify nothing, at every site.
func TestATreeWalkOfAnotherClassClassifiesNothing(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/a"}}
	owned := treeHop{edge: treeEdge{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, ownership: true}}
	walk := &treeWalk{}
	state := treeWalkState{out: walk, principal: restricted, ownershipEdges: true}
	state.noteHitBasis(owned, walkHit{from: "a", to: unstatedWorkItem(), rel: foreignListEdge()})
	state.noteLinkBasis(row{"rl": foreignListEdge(), "ra": &edge{}}, owned, owned)
	if walk.filters.Basis != (EdgeAuthzBasis{}) {
		t.Fatalf("basis %+v, want none", walk.filters.Basis)
	}
}

// advance and members classify each refused hit by the rule that decided it.
func TestATreeWalkClassifiesEachRefusedHit(t *testing.T) {
	owned := treeHop{edge: treeEdge{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, ownership: true}}
	attribution := treeHop{edge: treeEdge{relation: contractsv1.ContextFabricRelationshipDocumentedBy}}
	hit := func(rel *edge) walkHit { return walkHit{from: "a", to: unstatedWorkItem(), rel: rel} }
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"P-1"}}
	for _, tc := range []struct {
		name  string
		state treeWalkState
		hop   treeHop
		hit   walkHit
		want  EdgeAuthzBasis
	}{
		{"an unstated node", treeWalkState{ownershipEdges: true}, owned, hit(&edge{}), EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
		{"a foreign project list the walk read", treeWalkState{ownershipEdges: true}, owned, hit(foreignListEdge()), EdgeAuthzBasis{ProjectOnly: true, ProjectList: 1}},
		{"a list the walk did not read", treeWalkState{}, owned, hit(foreignListEdge()), EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
		{"a denied anchor refuses before the edge", treeWalkState{ownershipEdges: true, anchorDenied: true}, owned, hit(foreignListEdge()), EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
		{"an attribution relation", treeWalkState{}, attribution, hit(nil), EdgeAuthzBasis{ProjectOnly: true, AttributionRelation: 1}},
		{"a requested project narrowing", treeWalkState{scope: narrowed}, owned, hit(nil), EdgeAuthzBasis{ProjectOnly: true, ProjectNarrowing: 1}},
		{"an ownership hop with no edge read", treeWalkState{ownershipEdges: true}, owned, hit(nil), EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
	} {
		for site, run := range map[string]func(treeWalkState){
			"advance": func(s treeWalkState) { s.advance(tc.hop, []walkHit{tc.hit}, nil) },
			"members": func(s treeWalkState) { s.members(tc.hop, []walkHit{tc.hit}, nil) },
		} {
			t.Run(tc.name+"/"+site, func(t *testing.T) {
				walk := &treeWalk{}
				state := tc.state
				state.out, state.principal = walk, treeProjectOnly()
				run(state)
				if walk.filters.Basis != tc.want {
					t.Fatalf("basis %+v, want %+v", walk.filters.Basis, tc.want)
				}
				assertBasisSums(t, walk, 0)
			})
		}
	}
}

// A refused link row: the link edge, then the feed edge, then an endpoint. A
// fused read that returned no feed edge names no reason and stays unclassified.
func TestALinkRowRefusalNamesItsBasis(t *testing.T) {
	link := treeHop{edge: treeEdge{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, ownership: true}}
	feed := treeHop{edge: treeEdge{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, ownership: true}}
	for _, tc := range []struct {
		name  string
		state treeWalkState
		row   row
		want  EdgeAuthzBasis
	}{
		{"the link edge", treeWalkState{ownershipEdges: true}, row{"rl": foreignListEdge(), "ra": &edge{}}, EdgeAuthzBasis{ProjectOnly: true, ProjectList: 1}},
		{"the feed edge", treeWalkState{ownershipEdges: true}, row{"rl": &edge{}, "ra": foreignListEdge()}, EdgeAuthzBasis{ProjectOnly: true, ProjectList: 1}},
		{"both edges pass: an endpoint", treeWalkState{ownershipEdges: true}, row{"rl": &edge{}, "ra": &edge{}}, EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
		{"no feed edge returned: no reason", treeWalkState{ownershipEdges: true}, row{"rl": &edge{}}, EdgeAuthzBasis{ProjectOnly: true}},
		{"a walk that reads no edge attributes: an endpoint", treeWalkState{}, row{"rl": foreignListEdge()}, EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
		{"a denied anchor refuses before the edges", treeWalkState{ownershipEdges: true, anchorDenied: true}, row{"rl": foreignListEdge()}, EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walk := &treeWalk{}
			state := tc.state
			state.out, state.principal = walk, treeProjectOnly()
			state.noteLinkBasis(tc.row, feed, link)
			state.deny()
			if walk.filters.Basis != tc.want {
				t.Fatalf("basis %+v, want %+v", walk.filters.Basis, tc.want)
			}
		})
	}
}

// Wiring: a link row the read returned and the node rule refuses a
// project-only caller goes through linkSegment's own deny site, and the walk
// reports its basis. The fake returns the row the read would keep; under
// project_only that read binds no repository, so the row is decided here.
func TestALinkSegmentDenialReportsItsBasis(t *testing.T) {
	linkRow := row{
		"m": fakeSubjectNodeRow("pull_request", "pull_request:ghpr:1", "ghpr:1")["n"],
		"b": unstatedWorkItem(),
		"rl": &edge{Properties: map[string]interface{}{
			propRelationType: "LINKS_PULL_REQUEST", propRelationshipID: "rel_link",
			propPropertyPrefix + linkTierProperty: "native", propPropertyPrefix + linkRankProperty: seededTierRank["native"],
		}},
	}
	served := false
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
		if strings.Contains(cypher, "count(") || served || !strings.Contains(cypher, "rl") {
			return nil, nil
		}
		served = true
		return []row{linkRow}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/svc"}
	start, ok := anchorPosition(anchor.Kind)
	if !ok {
		t.Fatal("no anchor position")
	}
	path, ok := treePath(start, treeIssue)
	if !ok || len(path) != 2 || !path[1].edge.link {
		t.Fatalf("path %+v, want a feed hop and a link hop", path)
	}
	walk := &treeWalk{}
	state := treeWalkState{out: walk, principal: treeProjectOnly(), collectLimit: 25, frontierLimit: 25}
	if _, _, err := adapter.linkSegment(context.Background(), "key", "org-1", state, anchor, path[0], path[1], newTemporalFilter(contextfabric.TimeContext{})); err != nil {
		t.Fatal(err)
	}
	if walk.denied != 1 || walk.filters.Authz != 1 {
		t.Fatalf("denied %d authz %d, want the row refused once", walk.denied, walk.filters.Authz)
	}
	want := EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1}
	if walk.filters.Basis != want {
		t.Fatalf("basis %+v, want %+v", walk.filters.Basis, want)
	}
	assertBasisSums(t, walk, 0)
}

// #1850 r2, end to end: a project-only team-anchored question is answered by a
// tree walk alone (the generic walk's edge read never runs), every owned
// project is refused, and DiscoverContext carries the walk's basis to the
// production Info line: the grant class, the authz_* fields, and parts that
// add up to authz.
func TestATreeWalkOnlyDiscoveryWritesTheProjectOnlyEdgeFilterLine(t *testing.T) {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:chaos", Label: "Fullchaos"}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Fullchaos"}, MemberKind: contextfabric.SubjectProject},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	genericEdgeReads := 0
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if cypher != "MATCH UNION" { // withWalkStepReads' own marker
				genericEdgeReads++
			}
			if params["id"] != anchor.CanonicalID {
				return nil, nil
			}
			var rows []row
			for _, id := range []string{"p1", "p2", "p3"} {
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_" + id}},
					"srcKind": "project", "srcId": id, "dstKind": "team", "dstId": anchor.CanonicalID,
				})
			}
			return rows, nil
		default:
			switch id, _ := params["id"].(string); id {
			case anchor.CanonicalID:
				return []row{fakeSubjectNodeRow("team", id, "Fullchaos")}, nil
			case "p1", "p2", "p3":
				return []row{fakeSubjectNodeRow("project", id, "Project "+id)}, nil
			}
			return nil, nil
		}
	}}
	var buffer bytes.Buffer
	telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buffer, nil))}
	adapter := newFakeAdapterWithTelemetry(t, withWalkStepReads(fake), telemetry)
	request := ownershipRoutingRequest(frame, anchor)
	request.Request.Question = "which projects does team Fullchaos own?"
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	if _, err := adapter.DiscoverContext(context.Background(), treeProjectOnly(), request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if genericEdgeReads != 0 {
		t.Fatalf("the generic walk read edges %d times: the tree walk is not the only source of denials", genericEdgeReads)
	}
	var line map[string]any
	for _, entry := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(entry), &decoded) == nil && decoded["msg"] == "context_fabric: relationship edges filtered" {
			line = decoded
		}
	}
	if line == nil {
		t.Fatalf("no edge-filter line:\n%s", buffer.String())
	}
	if line["grant_class"] != "project_only" {
		t.Fatalf("grant_class = %v, want project_only on a tree-walk-only denial: %v", line["grant_class"], line)
	}
	sum := 0.0
	for _, key := range []string{"authz_endpoint", "authz_attribution_relation", "authz_project_list", "authz_project_narrowing", "authz_unclassified"} {
		value, ok := line[key].(float64)
		if !ok {
			t.Fatalf("%s missing from the line: %v", key, line)
		}
		sum += value
	}
	if authz := line["authz"]; authz != float64(3) || sum != 3 {
		t.Fatalf("authz %v, parts sum %v; want 3 and 3: %v", authz, sum, line)
	}
}
