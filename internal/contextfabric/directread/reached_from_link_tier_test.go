package directread

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A pull request serves, per root, the tier of that root's own issue link beside
// the unchanged activity_proxy basis; a root that reached it through the
// repository only serves none.
func TestDirectReadPullRequestReachedFromCarriesTheLinkTier(t *testing.T) {
	linked := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:linked", Label: "#1"}
	proxied := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:proxied", Label: "#2"}
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets: []contextfabric.SubjectRef{linked, proxied},
			TargetRoot: map[string]contextfabric.SubjectRef{
				contextfabric.FactSubjectKey(linked): projectP, contextfabric.FactSubjectKey(proxied): projectP,
			},
			TargetRoots: map[string][]contextfabric.SubjectRef{
				contextfabric.FactSubjectKey(linked): {projectP, projectQ}, contextfabric.FactSubjectKey(proxied): {projectP},
			},
			TargetLinkTier: map[string]map[string]string{contextfabric.FactSubjectKey(linked): {contextfabric.FactSubjectKey(projectP): "native"}},
			Counts:         contextfabric.FactScopeExpansionCounts{CandidateCount: 2},
		}
	}}
	provider := perSubjectProvider(contextfabric.FactPullRequests, contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectProject)
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, subject := range query.Subjects {
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactPullRequests, Subject: subject, Fields: map[string]contextfabric.FactValue{"n": intValue(1)}})
		}
		return result, nil
	}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"pull_requests"}, Subjects: []RequestSubject{
		{Kind: "project", CanonicalID: projectP.CanonicalID}, {Kind: "project", CanonicalID: projectQ.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		linked.CanonicalID:  {projectP.CanonicalID: "native", projectQ.CanonicalID: ""},
		proxied.CanonicalID: {projectP.CanonicalID: ""},
	}
	for _, subject := range []contextfabric.SubjectRef{linked, proxied} {
		fact := servedOn(response, "pull_requests", subject)
		if fact == nil || len(fact.ReachedFrom) != len(want[subject.CanonicalID]) {
			t.Fatalf("pull request %s reached_from = %s, want roots %v", subject.CanonicalID, mustJSON(t, response.Facts), want[subject.CanonicalID])
		}
		for _, reach := range fact.ReachedFrom {
			tier, ok := want[subject.CanonicalID][reach.CanonicalID]
			if !ok || reach.Basis != "activity_proxy" || reach.LinkTier != tier {
				t.Fatalf("pull request %s reached from %s = basis %q tier %q, want activity_proxy and tier %q", subject.CanonicalID, reach.CanonicalID, reach.Basis, reach.LinkTier, tier)
			}
		}
	}
}
