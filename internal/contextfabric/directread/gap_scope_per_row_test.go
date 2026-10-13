package directread

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type originSplitExpander struct {
	pull contextfabric.SubjectRef
}

func (e originSplitExpander) ExpandFactScope(_ context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	if len(request.Origins) > 0 && request.Origins[0].Kind == contractsv1.ContextFabricSubjectProject {
		return contextfabric.FactScopeExpansionResult{}, errors.New("expansion store down")
	}
	return contextfabric.FactScopeExpansionResult{Targets: []contextfabric.SubjectRef{e.pull}, Counts: contextfabric.FactScopeExpansionCounts{CandidateCount: 1}}, nil
}

// One root's expansion failing marks the kind-level read truncated; the other
// root, whose own expansion completed, is not cut by it.
func TestAnotherRootsFailedExpansionDoesNotCutARootWhoseExpansionCompleted(t *testing.T) {
	pull := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:t1", Label: "#1"}
	provider := perSubjectProvider(contextfabric.FactPullRequests, contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam)
	reader := directExpandingReader(t, graph7073(), originSplitExpander{pull: pull}, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"pull_requests"}, Subjects: []RequestSubject{
		{Kind: "project", CanonicalID: projectQ7073.CanonicalID}, {Kind: "team", CanonicalID: teamT.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	assertRowsHonest(t, response)
	team := coverageOf(t, response, "pull_requests", teamT)
	if team.Outcome != OutcomeFactServed || team.Expansion == nil || team.Expansion.Outcome != string(contextfabric.FactScopeExpanded) {
		t.Errorf("team row %+v, want fact_served with its completed expansion", team)
	}
	project := coverageOf(t, response, "pull_requests", projectQ7073)
	if project.Outcome != OutcomeUnavailable || project.Expansion == nil || project.Expansion.Outcome != string(contextfabric.FactScopeFailed) {
		t.Errorf("project row %+v, want unavailable naming its failed expansion", project)
	}
}

// A provider that itself reports a truncated read is not a scope-only
// truncation, whatever the scope gap says.
func TestProviderTruncatedStateIsNotRelabelledAsServed(t *testing.T) {
	provider := rollupProvider(contextfabric.FactPullRequests, map[string]bool{projectQ7073.CanonicalID: true}, false)
	inner := provider.read
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result, err := inner(query)
		result.State = contextfabric.SourceTruncated
		return result, err
	}
	response, err := newTestFactsReader(t, graph7073(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"pull_requests"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ7073.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	if row := coverageOf(t, response, "pull_requests", projectQ7073); row.Outcome != OutcomeTruncated {
		t.Errorf("row %+v, want truncated: the provider reported the cut", row)
	}
}

func TestAnotherRootsFailedExpansionDoesNotCutARootWhoseOwnFactAndExpansionAreComplete(t *testing.T) {
	pull := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:t1", Label: "#1"}
	provider := rollupProvider(contextfabric.FactPullRequests, map[string]bool{projectQ7073.CanonicalID: true, teamT.CanonicalID: true, pull.CanonicalID: true}, false)
	provider.capability.SupportedSubjectKinds = append(provider.capability.SupportedSubjectKinds, contractsv1.ContextFabricSubjectPullRequest)
	reader := directExpandingReader(t, graph7073(), originSplitExpander{pull: pull}, nil, provider)
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"pull_requests"}, Subjects: []RequestSubject{
		{Kind: "project", CanonicalID: projectQ7073.CanonicalID}, {Kind: "team", CanonicalID: teamT.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	assertRowsHonest(t, response)
	team := coverageOf(t, response, "pull_requests", teamT)
	if team.Outcome != OutcomeFactServed {
		t.Errorf("team row %+v, want fact_served: its own fact was read and its own expansion completed", team)
	}
	project := coverageOf(t, response, "pull_requests", projectQ7073)
	if project.Outcome != OutcomeTruncated {
		t.Errorf("project row %+v, want truncated: its own expansion failed", project)
	}
}
