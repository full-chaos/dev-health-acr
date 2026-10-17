package directread

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A pull request reached through an issue link serves the link's tier beside
// the unchanged activity_proxy basis; one reached through its repository only
// serves no tier.
func TestDirectReadPullRequestReachedFromCarriesTheLinkTier(t *testing.T) {
	linked := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:linked", Label: "#1"}
	proxied := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:proxied", Label: "#2"}
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets: []contextfabric.SubjectRef{linked, proxied},
			TargetRoot: map[string]contextfabric.SubjectRef{
				contextfabric.FactSubjectKey(linked): projectP, contextfabric.FactSubjectKey(proxied): projectP,
			},
			TargetLinkTier: map[string]string{contextfabric.FactSubjectKey(linked): "native"},
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
		{Kind: "project", CanonicalID: projectP.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for subject, wantTier := range map[contextfabric.SubjectRef]string{linked: "native", proxied: ""} {
		fact := servedOn(response, "pull_requests", subject)
		if fact == nil || len(fact.ReachedFrom) != 1 || fact.ReachedFrom[0].Basis != "activity_proxy" || fact.ReachedFrom[0].LinkTier != wantTier {
			t.Fatalf("pull request %s reached_from = %s, want basis activity_proxy and link tier %q", subject.CanonicalID, mustJSON(t, response.Facts), wantTier)
		}
	}
}
