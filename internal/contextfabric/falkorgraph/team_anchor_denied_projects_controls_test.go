package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type reachEdge struct {
	id, dstKind, dstID string
	edgeAuthz          []string
}

var (
	reachAllowed = []string{"full-chaos/dev-health-acr"}
	reachDenied  = []string{"other/private"}
)

// reachFixture serves a team anchor with the given outgoing edges; node
// authorization is looked up by destination id.
func reachFixture(edges []reachEdge, nodeAuthz map[string][]string) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "team:chaos" {
				return nil, nil
			}
			var rows []row
			for _, e := range edges {
				authz := e.edgeAuthz
				if authz == nil {
					authz = reachAllowed
				}
				rows = append(rows, row{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "OWNS", propRelationshipID: "rel_" + e.id,
						"authorization_repositories": authz,
					}},
					"srcKind": "team", "srcId": "team:chaos", "dstKind": e.dstKind, "dstId": e.dstID,
				})
			}
			return rows, nil
		default:
			id, _ := params["id"].(string)
			kind, _ := params["kind"].(string)
			if id == "team:chaos" {
				r := fakeSubjectNodeRow("team", id, "Fullchaos")
				r["n"].(*node).Properties["authorization_repositories"] = reachAllowed
				return []row{r}, nil
			}
			authz, ok := nodeAuthz[id]
			if !ok {
				return nil, nil
			}
			if kind == "" {
				kind = "project"
				for _, e := range edges {
					if e.dstID == id {
						kind = e.dstKind
					}
				}
			}
			r := fakeSubjectNodeRow(kind, id, "N "+id)
			r["n"].(*node).Properties["authorization_repositories"] = authz
			return []row{r}, nil
		}
	}}
}

func discoverReach(t *testing.T, fake *fakeConn) (contextfabric.GraphContext, *recordingTelemetry) {
	t.Helper()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:chaos", Label: "Fullchaos"}
	request := ownershipRoutingRequest(reachProjectsFrame("Fullchaos"), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Question = "which projects does team Fullchaos own?"
	telemetry := &recordingTelemetry{}
	result, err := newFakeAdapterWithTelemetry(t, fake, telemetry).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: reachAllowed}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return result, telemetry
}

func deniedReasonCount(result contextfabric.GraphContext) string {
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			return reason
		}
	}
	return ""
}

// Some owned projects served, some denied: partial with the denied count only.
func TestDiscoverContextTeamAnchorMixedReachCountsOnlyTheDeniedProjects(t *testing.T) {
	result, _ := discoverReach(t, reachFixture(
		[]reachEdge{{id: "a", dstKind: "project", dstID: "p-ok"}, {id: "b", dstKind: "project", dstID: "p-no1"}, {id: "c", dstKind: "project", dstID: "p-no2"}},
		map[string][]string{"p-ok": reachAllowed, "p-no1": reachDenied, "p-no2": reachDenied}))
	if got := deniedReasonCount(result); got != "cohort_denied_by_authorization:2" {
		t.Fatalf("denied reason = %q, want cohort_denied_by_authorization:2 (reasons %v)", got, result.Coverage.DegradedReasons)
	}
	if !reachMemberIDs(result.Cohort)["p-ok"] || len(reachMemberIDs(result.Cohort)) != 1 {
		t.Fatalf("members = %v, want only p-ok", reachMemberIDs(result.Cohort))
	}
}

// A denied edge (not endpoint) to a project counts, once per project.
func TestDiscoverContextTeamAnchorDeniedEdgeCountsTheProjectOnce(t *testing.T) {
	result, _ := discoverReach(t, reachFixture(
		[]reachEdge{{id: "a", dstKind: "project", dstID: "p-no", edgeAuthz: reachDenied}, {id: "b", dstKind: "project", dstID: "p-no", edgeAuthz: reachDenied}},
		map[string][]string{"p-no": reachDenied}))
	if got := deniedReasonCount(result); got != "cohort_denied_by_authorization:1" {
		t.Fatalf("denied reason = %q, want cohort_denied_by_authorization:1 (reasons %v)", got, result.Coverage.DegradedReasons)
	}
}

// A project the walk reached through an admitted edge is served, not denied,
// even when a second edge to it is denied.
func TestDiscoverContextTeamAnchorReachedProjectIsNotCountedDenied(t *testing.T) {
	result, _ := discoverReach(t, reachFixture(
		[]reachEdge{{id: "a", dstKind: "project", dstID: "p-ok"}, {id: "b", dstKind: "project", dstID: "p-ok", edgeAuthz: reachDenied}},
		map[string][]string{"p-ok": reachAllowed}))
	if got := deniedReasonCount(result); got != "" {
		t.Fatalf("denied reason = %q, want none (reasons %v)", got, result.Coverage.DegradedReasons)
	}
	if !reachMemberIDs(result.Cohort)["p-ok"] {
		t.Fatalf("members = %v, want p-ok", reachMemberIDs(result.Cohort))
	}
}

// A denied endpoint of another kind is not a denied member.
func TestDiscoverContextTeamAnchorDeniedOtherKindFilesNoRow(t *testing.T) {
	result, _ := discoverReach(t, reachFixture(
		[]reachEdge{{id: "a", dstKind: "work_item", dstID: "w-no"}},
		map[string][]string{"w-no": reachDenied}))
	if got := deniedReasonCount(result); got != "" {
		t.Fatalf("denied reason = %q, want none for a work item (reasons %v)", got, result.Coverage.DegradedReasons)
	}
}

func reachProjectsFrame(term string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{term}, MemberKind: contextfabric.SubjectProject,
			},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
}

func reachMemberIDs(c *contextfabric.Cohort) map[string]bool {
	ids := map[string]bool{}
	if c == nil {
		return ids
	}
	for _, m := range c.Members {
		ids[m.Subject.CanonicalID] = true
	}
	return ids
}

// An edge to a project that no longer exists is a filtered edge, not a denial.
func TestDiscoverContextTeamAnchorMissingEndpointFilesNoRow(t *testing.T) {
	result, _ := discoverReach(t, reachFixture(
		[]reachEdge{{id: "a", dstKind: "project", dstID: "p-gone"}},
		map[string][]string{}))
	if got := deniedReasonCount(result); got != "" {
		t.Fatalf("denied reason = %q, want none for a missing endpoint (reasons %v)", got, result.Coverage.DegradedReasons)
	}
}

// The anchor itself is never a denied member, even when the cohort kind is
// the anchor's own kind.
func TestDiscoverContextAnchorIsNotCountedAsADeniedMemberOfItsOwnKind(t *testing.T) {
	fake := reachFixture(
		[]reachEdge{{id: "a", dstKind: "team", dstID: "t-no", edgeAuthz: reachDenied}},
		map[string][]string{"t-no": reachDenied})
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:chaos", Label: "Fullchaos"}
	frame := reachProjectsFrame("Fullchaos")
	frame.SubjectExpression.Scoped.MemberKind = contextfabric.SubjectTeam
	request := ownershipRoutingRequest(frame, anchor)
	request.Request.Question = "which teams relate to Fullchaos?"
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: reachAllowed}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if got := deniedReasonCount(result); got != "cohort_denied_by_authorization:1" {
		t.Fatalf("denied reason = %q, want cohort_denied_by_authorization:1 (reasons %v)", got, result.Coverage.DegradedReasons)
	}
}

func TestAnchorReachDeniedCountJoinsThePoolAndTheWalkOnce(t *testing.T) {
	denied := func(kind, id string) graphrankNode {
		return graphrankNode{kind: kind, id: id, authz: reachDenied}
	}
	pool := graphrankNodes{denied("project", "p-both"), denied("team", "t-pool"), {kind: "project", id: "p-ok", authz: reachAllowed}}
	walk := map[string]struct{}{
		subjectUUID("project", "p-both"): {}, subjectUUID("project", "p-walk"): {}, subjectUUID("team", "t-walk"): {},
		subjectUUID("project", ""): {},
	}
	got, reach := anchorReachDeniedCount(contextfabric.SubjectProject, pool.nodes(), storage.Principal{OrgID: "o", RepositoryScopes: reachAllowed}, contextfabric.GraphDiscoveryRequest{}, walk, 7)
	if got != 2 || !reach {
		t.Fatalf("anchorReachDeniedCount = %d, %v, want 2, true (p-both once, p-walk)", got, reach)
	}
	if got, reach := anchorReachDeniedCount(contextfabric.SubjectProject, pool.nodes(), storage.Principal{OrgID: "o"}, contextfabric.GraphDiscoveryRequest{}, nil, 7); got != 7 || reach {
		t.Fatalf("with no walk denial = %d, %v, want the pool count 7, false", got, reach)
	}
	if got, reach := anchorReachDeniedCount(contextfabric.SubjectProject, nil, storage.Principal{OrgID: "o"}, contextfabric.GraphDiscoveryRequest{}, map[string]struct{}{subjectUUID("team", "t"): {}}, 7); got != 7 || reach {
		t.Fatalf("walk denial of another kind = %d, %v, want the pool count 7, false", got, reach)
	}
}

type graphrankNode struct {
	kind, id string
	authz    []string
}

type graphrankNodes []graphrankNode

func (n graphrankNodes) nodes() []graphrank.CandidateNode {
	out := make([]graphrank.CandidateNode, 0, len(n))
	for _, g := range n {
		out = append(out, toCandidateNode(&node{Properties: map[string]interface{}{
			propKind: g.kind, propCanonicalID: g.id, propLabel: g.id, "authorization_repositories": g.authz,
		}}))
	}
	return out
}
