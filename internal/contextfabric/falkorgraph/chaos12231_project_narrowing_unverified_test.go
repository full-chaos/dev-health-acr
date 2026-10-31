package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12231 (the interim half of CHAOS-12229): a request narrowed by
// scope.project_ids admits a node whose authorization_projects is the "*"
// list, because scopeContainsAttr admits "*" for every requested project
// (graphrank/authorize.go). The projection writes an empty project list as
// "*", which today is every work item and pull request. So the answer
// carried the requested project scope back as if it had been applied, while
// nothing showed that the admitted material belongs to a requested project.
//
// The reader now discloses that with the existing documented code
// read_population_unverified ("the set this covers could not be
// identified"). Admission does not change: the same members are served.

const narrowingRepository = "full-chaos/dev-health-acr"

// wantNarrowingReason is the composed degraded reason, written out so this
// test pins the wire string rather than a constant it shares with the code.
const wantNarrowingReason = "project_narrowing_unverified"

// narrowingTeam is one candidate team of a discovered cohort: its project
// list (the "*" string or a []string) and whether the caller's repository
// grant admits it.
type narrowingTeam struct {
	projects interface{}
	granted  bool
}

func discoverUnderProjectNarrowing(t *testing.T, projectIDs []string, teams ...narrowingTeam) contextfabric.GraphContext {
	t.Helper()
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			rows := make([]row, 0, len(teams))
			for i, team := range teams {
				key := fmt.Sprintf("team_%d", i)
				r := fakeSubjectNodeRow("team", key, fmt.Sprintf("Team %d", i))
				repository := narrowingRepository
				if !team.granted {
					repository = "full-chaos/other"
				}
				r["n"].(*node).Properties["authorization_repositories"] = []string{repository}
				r["n"].(*node).Properties["authorization_teams"] = []string{key}
				r["n"].(*node).Properties["authorization_projects"] = team.projects
				rows = append(rows, r)
			}
			return rows, nil
		default:
			t.Fatalf("unexpected query: %s", cypher)
			return nil, nil
		}
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{narrowingRepository}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Request.RequestedScope.ProjectIDs = projectIDs
	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return result
}

func unverifiedNarrowingDetails(coverage contextfabric.Coverage) []contextfabric.CoverageDetail {
	var found []contextfabric.CoverageDetail
	for _, detail := range coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailReadPopulationUnverified {
			found = append(found, detail)
		}
	}
	return found
}

func cohortMemberCount(result contextfabric.GraphContext) int {
	if result.Cohort == nil {
		return 0
	}
	return len(result.Cohort.Members)
}

func assertUnverifiedNarrowingDisclosed(t *testing.T, result contextfabric.GraphContext) {
	t.Helper()
	details := unverifiedNarrowingDetails(result.Coverage)
	if len(details) != 1 {
		t.Fatalf("read_population_unverified details = %d, want 1 in %#v", len(details), result.Coverage.Details)
	}
	detail := details[0]
	want := wantNarrowingReason
	if !detail.Degrading || detail.Source != "context-fabric:graph" || detail.Raw != want ||
		detail.Count != nil || detail.Kind != "" || detail.Declared != nil || detail.Served != nil {
		t.Fatalf("detail = %#v, want a degrading context-fabric:graph detail, raw %q, no count, kind or declared/served", detail, want)
	}
	if detail.Label != contractsv1.ComposeCoverageDetailLabel(detail) {
		t.Fatalf("label = %q, want the composed label %q", detail.Label, contractsv1.ComposeCoverageDetailLabel(detail))
	}
	// The existing contract accepts the detail unchanged.
	if err := detail.Validate(); err != nil {
		t.Fatalf("detail does not validate against the existing contract: %v", err)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: the requested project narrowing was not verified")
	}
	reasons := 0
	for _, reason := range result.Coverage.DegradedReasons {
		if reason == want {
			reasons++
		}
	}
	if reasons != 1 {
		t.Fatalf("DegradedReasons = %v, want %q once", result.Coverage.DegradedReasons, want)
	}
}

func assertNoUnverifiedNarrowing(t *testing.T, result contextfabric.GraphContext) {
	t.Helper()
	if details := unverifiedNarrowingDetails(result.Coverage); len(details) != 0 {
		t.Fatalf("read_population_unverified details = %#v, want none", details)
	}
	for _, reason := range result.Coverage.DegradedReasons {
		if reason == wantNarrowingReason {
			t.Fatalf("DegradedReasons = %v, want no %q", result.Coverage.DegradedReasons, reason)
		}
	}
	if result.Coverage.Partial {
		t.Fatalf("Coverage.Partial = true, want false: %#v", result.Coverage)
	}
}

// A narrowed request whose admitted member passed only on "*": disclosed,
// and the member is still served (admission is unchanged).
func TestProjectNarrowingAdmittedOnlyByTheWildcardIsDisclosed(t *testing.T) {
	result := discoverUnderProjectNarrowing(t, []string{"proj-1"}, narrowingTeam{projects: "*", granted: true})
	assertUnverifiedNarrowingDisclosed(t, result)
	if got := cohortMemberCount(result); got != 1 {
		t.Fatalf("cohort members = %d, want 1: the disclosure must not change admission", got)
	}
}

// One member with a real project list and one on "*": still disclosed,
// once, and both are served.
func TestProjectNarrowingWithOneWildcardMemberIsDisclosedOnce(t *testing.T) {
	result := discoverUnderProjectNarrowing(t, []string{"proj-1"},
		narrowingTeam{projects: []string{"proj-1"}, granted: true},
		narrowingTeam{projects: "*", granted: true})
	assertUnverifiedNarrowingDisclosed(t, result)
	if got := cohortMemberCount(result); got != 2 {
		t.Fatalf("cohort members = %d, want 2", got)
	}
}

// Clause 1: no project narrowing was requested, so nothing claims one.
func TestNoProjectNarrowingRequestedDisclosesNothing(t *testing.T) {
	result := discoverUnderProjectNarrowing(t, nil, narrowingTeam{projects: "*", granted: true})
	assertNoUnverifiedNarrowing(t, result)
	if got := cohortMemberCount(result); got != 1 {
		t.Fatalf("cohort members = %d, want 1", got)
	}
}

// Clause 2: every admitted member names the requested project, so the
// narrowing was verified.
func TestProjectNarrowingVerifiedByARealProjectListDisclosesNothing(t *testing.T) {
	result := discoverUnderProjectNarrowing(t, []string{"proj-1"}, narrowingTeam{projects: []string{"proj-1"}, granted: true})
	assertNoUnverifiedNarrowing(t, result)
	if got := cohortMemberCount(result); got != 1 {
		t.Fatalf("cohort members = %d, want 1", got)
	}
}

// Clause 3: a "*" node the caller's repository grant denies was never
// admitted, so it says nothing about what the answer carries.
func TestAWildcardNodeTheGrantDeniedDisclosesNothing(t *testing.T) {
	result := discoverUnderProjectNarrowing(t, []string{"proj-1"},
		narrowingTeam{projects: []string{"proj-1"}, granted: true},
		narrowingTeam{projects: "*", granted: false})
	for _, detail := range unverifiedNarrowingDetails(result.Coverage) {
		t.Fatalf("read_population_unverified detail = %#v, want none: the only \"*\" node was denied", detail)
	}
	if got := cohortMemberCount(result); got != 1 {
		t.Fatalf("cohort members = %d, want 1", got)
	}
}

// discoverEdgeUnderProjectNarrowing walks one BLOCKS edge from project p1 to
// a work item, both endpoints naming p1, under a request narrowed to p1. Only
// the edge's own project list varies.
func discoverEdgeUnderProjectNarrowing(t *testing.T, edgeProjects interface{}) contextfabric.GraphContext {
	t.Helper()
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	endpoint := func(kind, id, label string) row {
		r := fakeSubjectNodeRow(kind, id, label)
		r["n"].(*node).Properties["authorization_projects"] = []string{"p1"}
		return r
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{{
				"r": &edge{Properties: map[string]interface{}{
					propRelationType: "BLOCKS", propRelationshipID: "relationship_1", propEvidenceRefs: []string{"evidence_1"},
					"authorization_projects": edgeProjects,
				}},
				"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_target",
			}}, nil
		default: // nodeByKindID
			switch params["id"] {
			case "work_target":
				return []row{endpoint("work_item", "work_target", "Target")}, nil
			case "p1":
				return []row{endpoint("project", "p1", "Origin")}, nil
			default:
				return nil, nil
			}
		}
	}}
	request := fakeDiscoveryRequest(origin, 10)
	request.Request.RequestedScope.ProjectIDs = []string{"p1"}
	result, err := newFakeAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if findFakeEdge(result.Paths, "BLOCKS") == nil {
		t.Fatalf("Paths = %#v, want the BLOCKS edge admitted", result.Paths)
	}
	return result
}

// Clause 4: an admitted edge whose own project list is "*" passed the
// narrowing unverified, even when both endpoints name the project.
func TestAnAdmittedWildcardEdgeIsDisclosed(t *testing.T) {
	assertUnverifiedNarrowingDisclosed(t, discoverEdgeUnderProjectNarrowing(t, "*"))
}

func TestAnAdmittedEdgeWithARealProjectListDisclosesNothing(t *testing.T) {
	assertNoUnverifiedNarrowing(t, discoverEdgeUnderProjectNarrowing(t, []string{"p1"}))
}
