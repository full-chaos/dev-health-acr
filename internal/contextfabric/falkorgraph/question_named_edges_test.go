package falkorgraph

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A question that asks for the project's identity names its OWNED_BY_TEAM edge:
// with more own edges than the path budget, a BLOCKS edge, and an owner id that
// sorts after every other id, the owner edge is still admitted, and first. The
// subject is a project, which the identity fact provider does not serve
// (repositories and work items only): the class reads the question's fact
// requirements, never which subjects a provider serves.
func TestOwnershipQuestionAdmitsTheOwnerEdgeBeyondTheCap(t *testing.T) {
	const paths = 25
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	request := committedRequest(paths, project)
	request.Interpretation.FactRequirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactIdentity}}
	result, err := newFakeAdapter(t, ownerFixtureFake("relationship.v2:zz:owner", "relationship.v2:zy:blocks", 40)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := admittedRelationships(t, result)
	if len(got) < 2 || got[0] != "relationship.v2:zz:owner" || got[1] != "relationship.v2:zy:blocks" {
		t.Fatalf("admitted %v, want the owner edge first, then the BLOCKS edge", got)
	}
	if len(result.DriverCandidates) != 1 {
		t.Fatalf("driver candidates %d, want 1: the BLOCKS edge makes a driver, the owner edge makes none", len(result.DriverCandidates))
	}
}

// A question that does not ask for identity does not lift the owner edge: the
// BLOCKS edge the answer gate can use stays first, and a blockers question puts
// it first as a named edge.
func TestOwnerEdgeIsNotLiftedForAQuestionThatDoesNotNameIt(t *testing.T) {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	for _, requirements := range [][]contextfabric.FactRequirement{nil, {{Kind: contextfabric.FactBlockers}}} {
		request := committedRequest(25, project)
		request.Interpretation.FactRequirements = requirements
		result, err := newFakeAdapter(t, ownerFixtureFake("relationship.v2:0:owner", "relationship.v2:zz:blocks", 40)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		got := admittedRelationships(t, result)
		if len(got) == 0 || got[0] != "relationship.v2:zz:blocks" {
			t.Fatalf("requirements %v: admitted %v, want the BLOCKS edge first", requirements, got)
		}
	}
}

// An edge goes to the origin that reaches it in the fewest hops, also past the
// second hop of a deeper walk: A reaches the edge y-z at its third hop, B at its
// second, so it is B's, though A's walk read it.
func TestEdgeGoesToTheOriginThatReachesItInTheFewestHops(t *testing.T) {
	ref := func(id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id}
	}
	a := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:a"}
	b := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:b"}
	edges := []graphrank.ResolvedEdge{
		{UUID: "a-x", From: a, To: ref("x")},
		{UUID: "x-y", From: ref("x"), To: ref("y")},
		{UUID: "y-z", From: ref("y"), To: ref("z")},
		{UUID: "b-z", From: b, To: ref("z")},
	}
	keyA, keyB := graphrank.SubjectKey(a), graphrank.SubjectKey(b)
	origin := map[string]string{"a-x": keyA, "x-y": keyA, "y-z": keyA, "b-z": keyB}
	own := attributeEdgesByFewestHops(edges, origin, []string{keyA, keyB})
	want := map[string]string{"a-x": keyA, "x-y": keyA, "y-z": keyB, "b-z": keyB}
	for id, key := range want {
		if origin[id] != key {
			t.Fatalf("edge %s: origin %s, want %s", id, origin[id], key)
		}
	}
	if !own["a-x"] || !own["b-z"] || own["x-y"] || own["y-z"] {
		t.Fatalf("own %v, want only a-x and b-z", own)
	}
}

// A work item's OWNED_BY_TEAM edge is a team attribution, not ownership
// (directread OwnedByKinds: projects and repositories): an identity question
// about a work item does not name it, so the BLOCKS edge the answer gate can
// use keeps the one path the budget has.
func TestWorkItemTeamAttributionIsNotNamedByAnIdentityQuestion(t *testing.T) {
	workItem := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:checkout-1", Label: "checkout 1"}
	one := func(rid, kind, dstKind, dstID string) row {
		return row{
			"r":       &edge{Properties: map[string]interface{}{propRelationType: kind, propRelationshipID: rid, propEvidenceRefs: []string{"evidence_" + rid}}},
			"srcKind": "work_item", "srcId": "work_item:checkout-1", "dstKind": dstKind, "dstId": dstID,
		}
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if id, _ := params["id"].(string); id == "work_item:checkout-1" {
				return []row{
					one("relationship.v2:0:attribution", "OWNED_BY_TEAM", "team", "team:core"),
					one("relationship.v2:z:blocks", "BLOCKS", "work_item", "work_item:checkout-2"),
				}, nil
			}
			return nil, nil
		default:
			kind, _ := params["kind"].(string)
			id, _ := params["id"].(string)
			if kind == "" || id == "" {
				return nil, nil
			}
			return []row{fakeSubjectNodeRow(kind, id, id)}, nil
		}
	}}
	request := committedRequest(1, workItem)
	request.Interpretation.FactRequirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactIdentity}}
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if got := admittedRelationships(t, result); len(got) != 1 || got[0] != "relationship.v2:z:blocks" {
		t.Fatalf("admitted %v, want only the BLOCKS edge", got)
	}
}

// One committed subject whose owner edge the question names logs the budget
// line: the class decision has telemetry even without a split.
func TestSingleOriginNamedEdgeDecisionIsLogged(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previous)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	fake := committedWalkFake(map[string][][3]string{"project:checkout": {{"relationship.v2:a:00", "work_item", "work_item:0"}}})
	inner := fake.queryFunc
	fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		rows, err := inner(ctx, graphKey, cypher, params, readOnly)
		if strings.Contains(cypher, "UNION") && params["id"] == "project:checkout" {
			rows = append(rows, row{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "relationship.v2:z:owner", propEvidenceRefs: []string{"evidence_relationship.v2:z:owner"}}},
				"srcKind": "project", "srcId": "project:checkout", "dstKind": "team", "dstId": "team:core",
			})
		}
		return rows, err
	}
	request := committedRequest(5, project)
	request.Interpretation.FactRequirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactIdentity}}
	if _, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if !strings.Contains(buf.String(), `"msg":"context_fabric: shared edge budget"`) || !strings.Contains(buf.String(), `"question_named_edges":1`) {
		t.Fatalf("no budget line with question_named_edges=1:\n%s", buf.String())
	}
}
