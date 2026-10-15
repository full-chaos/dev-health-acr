package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Two subjects the question text matches share the full-text arm's edge
// budget (MaxResults). The first one's relationship ids all sort before the
// second one's, and it has enough edges to fill the budget alone. The second
// subject's edge must still be read: which subject's evidence fits the budget
// may not depend on how relationship ids are spelled.
func TestTextWalkSharesTheEdgeBudgetAcrossMatchedSubjects(t *testing.T) {
	const budget = 25
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "repository", propCanonicalID: "repository:checkout", propLabel: "acme/checkout", propSearchText: "checkout"}}, "score": 1.0},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "project:checkout", propLabel: "checkout", propSearchText: "checkout"}}, "score": 1.0},
			}, nil
		case strings.Contains(cypher, "UNION"):
			switch params["id"] {
			case "repository:checkout":
				rows := make([]row, 0, budget)
				for i := 0; i < budget; i++ {
					rows = append(rows, row{
						"r":       &edge{Properties: map[string]interface{}{propRelationType: "BELONGS_TO_REPOSITORY", propRelationshipID: fmt.Sprintf("relationship.v2:a:%02d", i), propEvidenceRefs: []string{fmt.Sprintf("evidence_incident_%02d", i)}}},
						"srcKind": "incident", "srcId": fmt.Sprintf("incident:%02d", i), "dstKind": "repository", "dstId": "repository:checkout",
					})
				}
				return rows, nil
			case "project:checkout":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "relationship.v2:z:owner", propEvidenceRefs: []string{"evidence_owner"}}},
					"srcKind": "project", "srcId": "project:checkout", "dstKind": "team", "dstId": "team:core",
				}}, nil
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
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which team owns checkout",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 100,
				MaxDrivers: 10, MaxEvidenceRefs: 500, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject},
	}
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	owner := false
	repositoryEdges := 0
	for _, path := range result.Paths {
		for _, e := range path.Edges {
			switch {
			case e.From.CanonicalID == "project:checkout" && e.To.CanonicalID == "team:core":
				owner = true
			case e.To.CanonicalID == "repository:checkout":
				repositoryEdges++
			}
		}
	}
	if !owner {
		t.Fatalf("the project's owner edge was not read: %d paths, %d of them the repository's; the repository's ids sorted first and took the whole budget", len(result.Paths), repositoryEdges)
	}
	if repositoryEdges != budget-1 {
		t.Fatalf("the repository kept %d edges, want %d: the budget is shared, not grown", repositoryEdges, budget-1)
	}
}

// Two committed subjects share the request's path budget (MaxRelationshipPaths)
// in edge admission. The first subject's ids all sort first and it has enough
// edges to fill the budget alone; the second subject's edge must still be
// admitted.
func TestEdgeAdmissionSharesThePathBudgetAcrossCommittedSubjects(t *testing.T) {
	const paths = 5
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			switch params["id"] {
			case "repository:checkout":
				rows := make([]row, 0, paths)
				for i := 0; i < paths; i++ {
					rows = append(rows, row{
						"r":       &edge{Properties: map[string]interface{}{propRelationType: "BELONGS_TO_REPOSITORY", propRelationshipID: fmt.Sprintf("relationship.v2:a:%02d", i), propEvidenceRefs: []string{fmt.Sprintf("evidence_incident_%02d", i)}}},
						"srcKind": "incident", "srcId": fmt.Sprintf("incident:%02d", i), "dstKind": "repository", "dstId": "repository:checkout",
					})
				}
				return rows, nil
			case "project:checkout":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "relationship.v2:z:owner", propEvidenceRefs: []string{"evidence_owner"}}},
					"srcKind": "project", "srcId": "project:checkout", "dstKind": "team", "dstId": "team:core",
				}}, nil
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
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which team owns checkout",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: paths,
				MaxDrivers: 10, MaxEvidenceRefs: 500, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{
			{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:checkout", Label: "acme/checkout"},
			{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"},
		}},
	}
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	owner := false
	for _, path := range result.Paths {
		for _, e := range path.Edges {
			owner = owner || (e.From.CanonicalID == "project:checkout" && e.To.CanonicalID == "team:core")
		}
	}
	if !owner || len(result.Paths) != paths {
		t.Fatalf("admitted %d paths, owner edge admitted=%v; want %d paths with the project's owner edge among them", len(result.Paths), owner, paths)
	}
}

// committedWalkFake serves two committed subjects' one-hop edges: edges maps a
// subject's canonical id to its (relationship id, other end kind, other end id)
// rows.
func committedWalkFake(edges map[string][][3]string) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			kind := strings.SplitN(id, ":", 2)[0]
			var rows []row
			for _, e := range edges[id] {
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "RELATES_TO", propRelationshipID: e[0], propEvidenceRefs: []string{"evidence_" + e[0]}}},
					"srcKind": kind, "srcId": id, "dstKind": e[1], "dstId": e[2],
				})
			}
			return rows, nil
		default:
			kind, _ := params["kind"].(string)
			id, _ := params["id"].(string)
			if kind == "" || id == "" {
				return nil, nil
			}
			return []row{fakeSubjectNodeRow(kind, id, id)}, nil
		}
	}}
}

func committedRequest(maxPaths int, committed ...contextfabric.SubjectRef) contextfabric.GraphDiscoveryRequest {
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which team owns checkout",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: maxPaths,
				MaxDrivers: 10, MaxEvidenceRefs: 500, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject},
		Resolution:     contextfabric.SubjectResolution{Committed: committed},
	}
}

func admittedRelationships(t *testing.T, result contextfabric.GraphContext) []string {
	t.Helper()
	var ids []string
	for _, path := range result.Paths {
		for _, ref := range path.EvidenceRefIDs {
			ids = append(ids, strings.TrimPrefix(ref, "evidence_"))
		}
	}
	return ids
}

// An edge both committed subjects reach (at their second hop) counts once, in
// the first subject's turns, and the second subject's turns go to its own
// edges.
func TestEdgeReachedFromTwoOriginsCountsOnceForTheFirst(t *testing.T) {
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:checkout", Label: "acme/checkout"}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	fake := committedWalkFake(map[string][][3]string{
		"repository:checkout": {{"relationship.v2:a:00", "work_item", "work_item:w"}, {"relationship.v2:a:01", "work_item", "work_item:1"}, {"relationship.v2:a:02", "work_item", "work_item:2"}, {"relationship.v2:a:03", "work_item", "work_item:3"}},
		"work_item:w":         {{"relationship.v2:x:shared", "work_item", "work_item:v"}},
		"work_item:v":         {{"relationship.v2:x:shared", "work_item", "work_item:w"}},
		"project:checkout":    {{"relationship.v2:z:owner", "team", "team:core"}, {"relationship.v2:z:v", "work_item", "work_item:v"}},
	})
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, committedRequest(7, repository, project))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := admittedRelationships(t, result)
	want := []string{"relationship.v2:a:00", "relationship.v2:z:owner", "relationship.v2:a:01", "relationship.v2:z:v", "relationship.v2:a:02", "relationship.v2:a:03", "relationship.v2:x:shared"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("admitted %v, want %v: the shared edge once, in the repository's turns", got, want)
	}
}

// One origin: admission is the ranked order it always was.
func TestSingleOriginAdmissionIsUnchanged(t *testing.T) {
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:checkout", Label: "acme/checkout"}
	var rows [][3]string
	for i := 5; i >= 0; i-- {
		rows = append(rows, [3]string{fmt.Sprintf("relationship.v2:a:%02d", i), "work_item", fmt.Sprintf("work_item:%d", i)})
	}
	result, err := newFakeAdapter(t, committedWalkFake(map[string][][3]string{"repository:checkout": rows})).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, committedRequest(3, repository))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := admittedRelationships(t, result)
	want := []string{"relationship.v2:a:00", "relationship.v2:a:01", "relationship.v2:a:02"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("admitted %v, want %v (ascending id, as before)", got, want)
	}
}

// A subject's own edge stays in that subject's turns even when another
// subject's walk reached it first at its second hop: the first subject has
// more edges than the cap, and the second subject's own edge is still admitted.
func TestOwnEdgeReachedAtAnotherSubjectsSecondHopStaysWithItsSubject(t *testing.T) {
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:checkout", Label: "acme/checkout"}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	repositoryEdges := [][3]string{{"relationship.v2:a:00", "work_item", "work_item:w"}}
	for i := 1; i <= 5; i++ {
		repositoryEdges = append(repositoryEdges, [3]string{fmt.Sprintf("relationship.v2:a:%02d", i), "work_item", fmt.Sprintf("work_item:%d", i)})
	}
	fake := committedWalkFake(map[string][][3]string{
		"repository:checkout": repositoryEdges,
		"work_item:w":         {{"relationship.v2:a:99:own", "project", "project:checkout"}},
		"project:checkout":    {{"relationship.v2:a:99:own", "work_item", "work_item:w"}},
	})
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, committedRequest(3, repository, project))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := admittedRelationships(t, result)
	want := []string{"relationship.v2:a:00", "relationship.v2:a:99:own", "relationship.v2:a:01"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("admitted %v, want %v: the project's own edge belongs to the project, not to the repository's second hop", got, want)
	}
}

// ownerFixtureFake is the fixture owner question's graph shape: a committed
// project with 17 own membership edges and one owner edge (ownerID), seven
// second-hop link edges, and 20 text-matched review subjects with one edge
// each. gateEdge adds a BLOCKS edge from the project with id gateID.
func ownerFixtureFake(ownerID, gateID string, members int) *fakeConn {
	one := func(rid, kind, srcKind, srcID, dstKind, dstID string) row {
		return row{
			"r":       &edge{Properties: map[string]interface{}{propRelationType: kind, propRelationshipID: rid, propEvidenceRefs: []string{"evidence_" + rid}}},
			"srcKind": srcKind, "srcId": srcID, "dstKind": dstKind, "dstId": dstID,
		}
	}
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			var rows []row
			for i := 0; i < 20; i++ {
				rows = append(rows, row{"node": &node{Properties: map[string]interface{}{propKind: "pull_request_review", propCanonicalID: fmt.Sprintf("pull_request_review:%02d", i), propLabel: "checkout review", propSearchText: "checkout"}}, "score": 1.0})
			}
			return rows, nil
		case strings.Contains(cypher, "UNION"):
			id, _ := params["id"].(string)
			switch {
			case id == "project:checkout":
				var rows []row
				for k := 0; k < members; k++ {
					rows = append(rows, one(fmt.Sprintf("relationship.v2:m:%02d", k), "BELONGS_TO_PROJECT", "work_item", fmt.Sprintf("work_item:%02d", k), "project", "project:checkout"))
				}
				rows = append(rows, one(ownerID, "OWNED_BY_TEAM", "project", "project:checkout", "team", "team:core"))
				if gateID != "" {
					rows = append(rows, one(gateID, "BLOCKS", "project", "project:checkout", "project", "project:ledger"))
				}
				return rows, nil
			case strings.HasPrefix(id, "work_item:"):
				return []row{one("relationship.v2:a:"+id, "LINKS_PULL_REQUEST", "work_item", id, "pull_request", "pull_request:"+id)}, nil
			case strings.HasPrefix(id, "pull_request_review:"):
				return []row{one("relationship.v2:b:"+id, "BELONGS_TO_PULL_REQUEST", "pull_request_review", id, "pull_request", "pull_request:"+id)}, nil
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
}

// The committed subject takes its turns before the subjects the question text
// matched, and its own edges come before its second-hop edges: 20 weakly
// matched subjects with one edge each cannot push the committed project's owner
// edge out of a 25-path budget, however its id is spelled.
func TestCommittedSubjectsOwnEdgesComeBeforeTextMatches(t *testing.T) {
	const paths = 25
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	for _, ownerID := range []string{"relationship.v2:z:owner", "relationship.v2:0:owner"} {
		result, err := newFakeAdapter(t, ownerFixtureFake(ownerID, "", 17)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, committedRequest(paths, project))
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		got := admittedRelationships(t, result)
		owner := false
		for _, id := range got {
			owner = owner || id == ownerID
		}
		if !owner || len(got) != paths {
			t.Fatalf("owner id %s: admitted %d paths, owner edge admitted=%v: %v", ownerID, len(got), owner, got)
		}
	}
}

// An edge the answer gate can use for the committed subject (its driver is
// about that subject) comes before the subject's other edges: with more own
// edges than the budget and an id that sorts last, it is still admitted.
func TestGateEdgeOfTheCommittedSubjectComesFirst(t *testing.T) {
	const paths = 25
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:checkout", Label: "checkout"}
	result, err := newFakeAdapter(t, ownerFixtureFake("relationship.v2:n:owner", "relationship.v2:zz:blocks", 30)).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, committedRequest(paths, project))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	got := admittedRelationships(t, result)
	if len(got) == 0 || got[0] != "relationship.v2:zz:blocks" {
		t.Fatalf("admitted %v, want the project's BLOCKS edge first", got)
	}
}
