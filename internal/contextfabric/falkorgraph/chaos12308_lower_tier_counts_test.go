package falkorgraph

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12308: the tier is named in two different numbers. The answer's
// coverage details count the pull requests in the answer; the walk line counts
// the pull requests a walk crossed.

var chaos12308Scope = contextfabric.RequestedScope{
	ProjectIDs:       []string{"proj-1"},
	ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true},
}

func chaos12308PullRequest(id, tier string) *node {
	properties := map[string]interface{}{
		propKind: string(contextfabric.SubjectPullRequest), propCanonicalID: id, propLabel: id,
		propAuthzRepos: []string{narrowingRepository}, propAuthzProjects: "*", propAuthzTeams: "*",
	}
	if tier != "" {
		properties[propAuthzProjectIdentities] = []interface{}{"project.v2:linear:proj-1"}
		properties[propProjectMembershipTier] = tier
	}
	return &node{Properties: properties}
}

// discoverPullRequestsUnderProjectNarrowing is a pull request cohort, read
// through DiscoverContext.
func discoverPullRequestsUnderProjectNarrowing(t *testing.T, nodes ...*node) contextfabric.GraphContext {
	t.Helper()
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "$kinds") {
			rows := make([]row, 0, len(nodes))
			for _, n := range nodes {
				rows = append(rows, row{"n": n})
			}
			return rows, nil
		}
		return nil, nil
	}}
	adapter := newFakeAdapterWithTelemetry(t, fake, &recordingTelemetry{})
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{narrowingRepository}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
	request.Frame.SubjectExpression.Discovered.MemberKind = contextfabric.SubjectPullRequest
	request.Request.RequestedScope = chaos12308Scope
	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return result
}

func chaos12308Counts(coverage contextfabric.Coverage) map[contractsv1.ContextFabricCoverageDetailCode]int {
	counts := map[contractsv1.ContextFabricCoverageDetailCode]int{}
	for _, detail := range coverage.Details {
		if detail.Code != contractsv1.ContextFabricCoverageDetailGraphAnswerPullRequestsOnExplicitTextLinks && detail.Code != contractsv1.ContextFabricCoverageDetailGraphAnswerPullRequestsOnHeuristicLinks {
			continue
		}
		if detail.Degrading || detail.Count == nil || detail.Label != contractsv1.ComposeCoverageDetailLabel(detail) || detail.Validate() != nil {
			panic("a lower-tier detail does not hold its contract")
		}
		counts[detail.Code] = *detail.Count
	}
	return counts
}

func TestTheAnswerNamesTheTierItsPullRequestsStandOn(t *testing.T) {
	explicit := contractsv1.ContextFabricCoverageDetailGraphAnswerPullRequestsOnExplicitTextLinks
	heuristic := contractsv1.ContextFabricCoverageDetailGraphAnswerPullRequestsOnHeuristicLinks
	for name, tc := range map[string]struct {
		nodes []*node
		want  map[contractsv1.ContextFabricCoverageDetailCode]int
	}{
		"each tier": {[]*node{chaos12308PullRequest("pull_request:r:1", "explicit_text"), chaos12308PullRequest("pull_request:r:2", "explicit_text"), chaos12308PullRequest("pull_request:r:3", "heuristic")},
			map[contractsv1.ContextFabricCoverageDetailCode]int{explicit: 2, heuristic: 1}},
		"one tier": {[]*node{chaos12308PullRequest("pull_request:r:1", "heuristic")}, map[contractsv1.ContextFabricCoverageDetailCode]int{heuristic: 1}},
		// Unstated stays disclosed, and has no tier to name.
		"unstated": {[]*node{chaos12308PullRequest("pull_request:r:1", "")}, map[contractsv1.ContextFabricCoverageDetailCode]int{}},
	} {
		t.Run(name, func(t *testing.T) {
			result := discoverPullRequestsUnderProjectNarrowing(t, tc.nodes...)
			if got := cohortMemberCount(result); got != len(tc.nodes) {
				t.Fatalf("premise: cohort members = %d, want %d", got, len(tc.nodes))
			}
			assertUnverifiedNarrowingDisclosed(t, result)
			if got := chaos12308Counts(result.Coverage); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("counts = %v, want %v", got, tc.want)
			}
		})
	}
	// A native member proves: nothing is unverified, and no tier is named.
	result := discoverPullRequestsUnderProjectNarrowing(t, chaos12308PullRequest("pull_request:r:1", "native"))
	assertNoUnverifiedNarrowing(t, result)
	if got := chaos12308Counts(result.Coverage); len(got) != 0 {
		t.Fatalf("a proven answer named a tier: %v", got)
	}
}

// The endpoints of admitted edges count too, each pull request once, and a
// cohort member refused by its grants does not.
func TestTheAnswersCountHoldsAdmittedMembersAndEdgeEndpointsOnce(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{narrowingRepository}}
	member := toCandidateNode(chaos12308PullRequest("pull_request:r:1", "heuristic"))
	refused := chaos12308PullRequest("pull_request:r:2", "heuristic")
	refused.Properties[propAuthzRepos] = []string{"full-chaos/other"}
	edges := []graphrank.ResolvedEdge{
		{EndpointLowerTierPullRequests: map[string]string{"pull_request:r:1": "heuristic", "pull_request:r:3": "explicit_text"}},
		{EndpointLowerTierPullRequests: map[string]string{"pull_request:r:3": "explicit_text"}},
	}
	got := answerPullRequestsOnLowerTier(principal, chaos12308Scope, []graphrank.CandidateNode{member, toCandidateNode(refused)}, edges)
	if want := map[string]int{"heuristic": 1, "explicit_text": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
}

// Both hop kinds record a pull request the walk crossed, once, and neither
// records a native one or one the walk refused.
func TestAWalkHopRecordsThePullRequestsItCrossedOnALowerTier(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{narrowingRepository}}
	refused := chaos12308PullRequest("pull_request:r:4", "heuristic")
	refused.Properties[propAuthzRepos] = []string{"full-chaos/other"}
	hits := []walkHit{
		{from: "repository:r", to: chaos12308PullRequest("pull_request:r:1", "explicit_text")},
		{from: "repository:r", to: chaos12308PullRequest("pull_request:r:1", "explicit_text")},
		{from: "repository:r", to: chaos12308PullRequest("pull_request:r:2", "heuristic")},
		{from: "repository:r", to: chaos12308PullRequest("pull_request:r:3", "native")},
		{from: "repository:r", to: refused},
	}
	want := contextfabric.CrossedPullRequests{ExplicitText: 1, Heuristic: 1}
	hop := treeHop{}
	advanced := treeWalkState{out: &treeWalk{}, principal: principal, scope: chaos12308Scope}
	advanced.advance(hop, hits, map[string]contextfabric.SubjectRef{})
	if got := crossedPullRequests(advanced.out.crossedLowerTier); got != want {
		t.Errorf("an intermediate hop crossed %+v, want %+v", got, want)
	}
	last := treeWalkState{out: &treeWalk{}, principal: principal, scope: chaos12308Scope}
	last.members(hop, hits, map[string]contextfabric.SubjectRef{})
	if got := crossedPullRequests(last.out.crossedLowerTier); got != want {
		t.Errorf("a last hop crossed %+v, want %+v", got, want)
	}
}

// A cohort member walk folds its walks: a pull request two walks crossed is one
// pull request, and the decision carries the counts to the walk line.
func TestATeamAnchorDecisionCountsACrossedPullRequestOnce(t *testing.T) {
	var tally teamAnchorWalkTally
	tally.add(treeWalk{crossedLowerTier: map[string]string{"pull_request:r:1": "heuristic", "pull_request:r:2": "explicit_text"}})
	tally.add(treeWalk{crossedLowerTier: map[string]string{"pull_request:r:1": "heuristic"}})
	got := tally.decision(contextfabric.SubjectTeam, contextfabric.SubjectPullRequest, 1, nil).Crossed
	if want := (contextfabric.CrossedPullRequests{ExplicitText: 1, Heuristic: 1}); got != want {
		t.Fatalf("crossed = %+v, want %+v", got, want)
	}
}

// resolveEdge carries the tier of each endpoint to the answer's count. Its own
// fake: each endpoint is read by kind and id.
func TestAnAdmittedEdgeCarriesItsEndpointsLowerTier(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{narrowingRepository}}
	pullRequest := chaos12308PullRequest("pull_request:r:1", "heuristic")
	issue := chaos12308PullRequest("work_item.v2:r:1", "")
	issue.Properties[propKind] = string(contextfabric.SubjectWorkItem)
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if params["kind"] == string(contextfabric.SubjectPullRequest) {
			return []row{{"n": pullRequest}}, nil
		}
		return []row{{"n": issue}}, nil
	}}
	a := newFakeAdapter(t, fake)
	issueEnd, pullRequestEnd := subjectUUID("work_item", "work_item.v2:r:1"), subjectUUID("pull_request", "pull_request:r:1")
	for name, ends := range map[string][2]string{"the target end": {issueEnd, pullRequestEnd}, "the source end": {pullRequestEnd, issueEnd}} {
		ce := graphrank.CandidateEdge{
			UUID: "rel_x", Name: "LINKS_PULL_REQUEST", SourceNodeUUID: ends[0], TargetNodeUUID: ends[1],
			Attributes: map[string]interface{}{propAuthzRepos: []string{narrowingRepository}, propAuthzProjects: "*", propAuthzTeams: "*"},
		}
		resolved, resolution, _, err := a.resolveEdge(context.Background(), "k", "org-1", principal, chaos12308Scope, ce, temporalFilter{})
		if err != nil || resolution != edgeAdmitted {
			t.Fatalf("%s: resolveEdge = %v, %v; want admitted", name, resolution, err)
		}
		if got := resolved.EndpointLowerTierPullRequests; !reflect.DeepEqual(got, map[string]string{"pull_request:r:1": "heuristic"}) {
			t.Fatalf("%s: endpoints = %v", name, got)
		}
	}
}
