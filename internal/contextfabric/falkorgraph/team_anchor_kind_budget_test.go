package falkorgraph

import (
	"context"
	"crypto/sha1"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func budgetTeamFake() *fakeConn {
	return budgetTeamFakeWith(nil, 30)
}

// budgetTeamFakeWith: closedRepositories are repository ids whose ownership
// edge ended in the past.
func budgetTeamFakeWith(closedRepositories map[string]bool, workItems int) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "team:platform" {
				return nil, nil
			}
			var rows []row
			add := func(kind, prefix string, n int) {
				for i := 0; i < n; i++ {
					id := fmt.Sprintf("%s:%s-%02d", kind, prefix, i)
					props := map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: fmt.Sprintf("rel_%x", sha1.Sum([]byte(id)))}
					if closedRepositories[id] {
						props[propValidToNs] = int64(1)
					}
					rows = append(rows, row{
						"r":       &edge{Properties: props},
						"srcKind": kind, "srcId": id, "dstKind": "team", "dstId": "team:platform",
					})
				}
			}
			add("work_item", "a", workItems)
			add("repository", "r", 10)
			add("project", "p", 14)
			return rows, nil
		default:
			id, _ := params["id"].(string)
			kind, _, ok := strings.Cut(id, ":")
			if !ok {
				return nil, nil
			}
			return []row{fakeSubjectNodeRow(kind, id, id)}, nil
		}
	}}
}

func discoverBudgetTeam(t *testing.T, fake *fakeConn, frame *contextfabric.QuestionFrame) contextfabric.GraphContext {
	t.Helper()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	request := ownershipRoutingRequest(frame, anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Options.MaxCohortMembers = 50
	result, err := newTeamAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireWholeCohort(t *testing.T, cohort *contextfabric.Cohort, kind string, want int) {
	t.Helper()
	ids := cohortIDs(cohort)
	if len(ids) != want {
		t.Fatalf("%s cohort = %d members, want %d: %v", kind, len(ids), want, ids)
	}
	for id := range ids {
		if !strings.HasPrefix(id, kind+":") {
			t.Fatalf("member %s is not a %s", id, kind)
		}
	}
	if !cohort.Complete || cohort.Truncated {
		t.Fatalf("%s cohort Complete=%v Truncated=%v, want true/false", kind, cohort.Complete, cohort.Truncated)
	}
}

// The team's other owned subjects spend no part of the budget the kind asked
// for: the whole set is served, complete.
func TestDiscoverContextTeamAnchorKindBudgetRepositories(t *testing.T) {
	result := discoverBudgetTeam(t, budgetTeamFake(), repositoriesOfAnchorFrame("Platform"))
	requireWholeCohort(t, result.Cohort, "repository", 10)
}

func TestDiscoverContextTeamAnchorKindBudgetProjects(t *testing.T) {
	result := discoverBudgetTeam(t, budgetTeamFake(), projectsOfAnchorFrame("Platform"))
	requireWholeCohort(t, result.Cohort, "project", 14)
}

// An ownership edge that ended makes the repository a former one: it is in no
// member list, path or edge.
func TestDiscoverContextTeamAnchorExcludesRepositoryWhoseOwnershipEnded(t *testing.T) {
	closed := "repository:r-03"
	// No other owned subject: the budget does not bind, so only the ended
	// edge can account for a missing member.
	result := discoverBudgetTeam(t, budgetTeamFakeWith(map[string]bool{closed: true}, 0), repositoriesOfAnchorFrame("Platform"))
	requireWholeCohort(t, result.Cohort, "repository", 9)
	if cohortIDs(result.Cohort)[closed] {
		t.Fatalf("members carry %s", closed)
	}
	for _, path := range result.Paths {
		for _, e := range path.Edges {
			if e.From.CanonicalID == closed || e.To.CanonicalID == closed {
				t.Fatalf("edge %+v names the former repository", e)
			}
		}
		for _, n := range path.Nodes {
			if n.CanonicalID == closed {
				t.Fatalf("path %+v carries the former repository", path)
			}
		}
	}
}

// A committed team the question does not anchor on keeps the generic walk: its
// neighbours still reach the context, though none of its projects is a member.
func TestDiscoverContextNonAnchorTeamKeepsTheGenericWalk(t *testing.T) {
	base := budgetTeamFake().queryFunc
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "UNION") && params["id"] == "team:other" {
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_other_wi", propEvidenceRefs: []string{"evidence_rel_other_wi_1234"}}},
				"srcKind": "work_item", "srcId": "work_item:other-1", "dstKind": "team", "dstId": "team:other",
			}}, nil
		}
		if !strings.Contains(cypher, "UNION") && !strings.Contains(cypher, "fulltext") && params["id"] == "team:other" {
			return []row{fakeSubjectNodeRow("team", "team:other", "Other")}, nil
		}
		return base(ctx, graphKey, cypher, params, readOnly)
	}}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:other", Label: "Other"}
	request := ownershipRoutingRequest(projectsOfAnchorFrame("Platform"), team)
	request.Resolution.Committed = []contextfabric.SubjectRef{team, other}
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Options.MaxCohortMembers = 50
	result, err := newTeamAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	requireWholeCohort(t, result.Cohort, "project", 14)
	seen := false
	for _, path := range result.Paths {
		for _, n := range path.Nodes {
			seen = seen || n.CanonicalID == "work_item:other-1"
		}
	}
	if !seen {
		t.Fatalf("paths %+v do not carry the other team's own neighbour", result.Paths)
	}
}

// Only a node reached over an ownership edge is named as a denied member.
func TestDenyOwnedNamesOnlyOwnershipReach(t *testing.T) {
	n := &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "project:x"}}
	for _, c := range []struct {
		ownership bool
		want      int
	}{{true, 1}, {false, 0}} {
		var out treeWalk
		treeWalkState{out: &out}.denyOwned(treeHop{edge: treeEdge{ownership: c.ownership}}, n)
		if got := len(out.filters.ReachDenied); got != c.want || out.denied != 1 {
			t.Fatalf("ownership=%v: reach denied %d (want %d), denied %d (want 1)", c.ownership, got, c.want, out.denied)
		}
	}
}

// An ownership hit is admitted only when both the edge and the node are; an
// edge of any other hop is decided by its node alone.
func TestAuthorizedHitDecidesOwnershipByEdgeAndNode(t *testing.T) {
	allowed := []string{"full-chaos/dev-health-acr"}
	denied := []string{"other/private"}
	hit := func(edgeAuthz, nodeAuthz []string) walkHit {
		return walkHit{
			to:  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "project:x", "authorization_repositories": nodeAuthz}},
			rel: &edge{Properties: map[string]interface{}{"authorization_repositories": edgeAuthz}},
		}
	}
	state := treeWalkState{principal: storage.Principal{OrgID: "org-1", RepositoryScopes: allowed}, ownershipEdges: true}
	owned, other := treeHop{edge: treeEdge{ownership: true}}, treeHop{}
	for _, c := range []struct {
		name       string
		hop        treeHop
		edge, node []string
		want       bool
	}{
		{"ownership both allowed", owned, allowed, allowed, true},
		{"ownership edge denied", owned, denied, allowed, false},
		{"ownership node denied", owned, allowed, denied, false},
		{"other hop edge denied, node allowed", other, denied, allowed, true},
		{"ownership edge denied, edge rule off (deployment walks)", owned, denied, allowed, true},
		{"other hop node denied", other, allowed, denied, false},
	} {
		st := state
		st.ownershipEdges = !strings.Contains(c.name, "edge rule off")
		if got := st.authorizedHit(c.hop, hit(c.edge, c.node)); got != c.want {
			t.Errorf("%s: authorized = %v, want %v", c.name, got, c.want)
		}
	}
}
