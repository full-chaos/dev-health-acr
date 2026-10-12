package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readPullRequestFacts(t *testing.T, client *fakeClient, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactPullRequests, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

func projectCanonicalID(t *testing.T, provider, id string) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive: omitted=%v err=%v", omitted, err)
	}
	return canonical
}

func TestPullRequestFactCarriesTheProjectsItsLinkedIssuesReach(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}, {"repo-1", uint32(8), "merged"}}},
		{match: "FROM work_graph_issue_pr", rows: [][]any{
			// pr_key, provider, project id, name, tier rank, strongest tier
			{"repo-1:7", "jira", "P1", "Payments", uint8(3), "native"},
			{"repo-1:7", "jira", "P2", "Search", uint8(1), "heuristic"},
		}},
	}}
	result := readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"), pullRequestSubject("repo-1", "8"))
	byID := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byID[fact.Subject.CanonicalID] = fact
	}
	linked := byID["pull_request:repo-1:7"].Fields["project_breakdown"]
	if linked.Table == nil || len(linked.Table.Rows) != 2 {
		t.Fatalf("linked pull request project_breakdown = %+v, want two project rows", linked)
	}
	first := linked.Table.Rows[0].Fields
	if got := *first["scope"].String; got != "project" {
		t.Fatalf("scope = %q", got)
	}
	if got, want := *first["scope_id"].String, projectCanonicalID(t, "jira", "P1"); got != want {
		t.Fatalf("scope_id = %q, want the derived canonical id %q (never a raw key)", got, want)
	}
	if got := *first["link_tier"].String; got != "native" {
		t.Fatalf("link_tier = %q, want the tier named on the row", got)
	}
	if got := *linked.Table.Rows[1].Fields["link_tier"].String; got != "heuristic" {
		t.Fatalf("second row tier = %q", got)
	}
	// A pull request with no admitted link carries no project_breakdown at
	// all: absent, never an empty table.
	if _, present := byID["pull_request:repo-1:8"].Fields["project_breakdown"]; present {
		t.Fatalf("unlinked pull request carries a project_breakdown: %+v", byID["pull_request:repo-1:8"].Fields)
	}
	if got := *byID["pull_request:repo-1:8"].Fields["state"].String; got != "merged" {
		t.Fatalf("state = %q", got)
	}
}

func TestPullRequestProjectReadUsesTheStrictLinkReading(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}}}}}
	readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	var statement string
	for _, query := range client.queries {
		if strings.Contains(query.statement, "FROM work_graph_issue_pr") {
			statement = query.statement
		}
	}
	if statement == "" {
		t.Fatal("no project link read was issued")
	}
	for _, want := range []string{
		"pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number", // an existing pull request only
		"NOT IN ('pr', 'merge_request')",                                              // an issue, never a pull request item
		"key_resolution_count = 1",                                                    // an ambiguous project key is omitted
		"'explicit_text', 2",                                                          // the closed tier vocabulary
	} {
		if !strings.Contains(statement, want) {
			t.Fatalf("project link read lacks %q:\n%s", want, statement)
		}
	}
	if strings.Contains(statement, "w.repo_id") {
		t.Fatalf("project link read uses the issue's own repository column:\n%s", statement)
	}
}

func TestPullRequestProjectRowsDropAnUnknownTier(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}}},
		{match: "FROM work_graph_issue_pr", rows: [][]any{{"repo-1:7", "jira", "P1", "Payments", uint8(1), "guess"}}},
	}}
	result := readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	if _, present := result.Facts[0].Fields["project_breakdown"]; present {
		t.Fatalf("a tier outside the vocabulary was served: %+v", result.Facts[0].Fields)
	}
}
