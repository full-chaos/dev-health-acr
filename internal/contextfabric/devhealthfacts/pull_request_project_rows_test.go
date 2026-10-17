package devhealthfacts_test

import (
	"context"
	"fmt"
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

// projectLinkRow is one row of the pull request project links statement:
// pr_key, provider, project id, name, strongest rank and tier.
func projectLinkRow(key, provider, id, name string, rank uint8, tier string) []any {
	return []any{key, provider, id, name, rank, tier}
}

// projectFallbackRow is one row of the fallback decision statement: pr_key,
// provider, project id, the pull request's native cover flag, the link rows
// below native, those of them whose issue a native row covers, and the
// strongest uncovered rank and tier below native.
func projectFallbackRow(key, provider, id string, pullRequestCovered uint8, fallbacks, issueCovered uint64, remainingRank uint8, remainingTier string) []any {
	return []any{key, provider, id, pullRequestCovered, fallbacks, issueCovered, remainingRank, remainingTier}
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
			projectLinkRow("repo-1:7", "jira", "P1", "Payments", 3, "native"),
			projectLinkRow("repo-1:7", "jira", "P2", "Search", 1, "heuristic"),
		}},
		{match: pullRequestFallbackDecisionMarker, rows: [][]any{projectFallbackRow("repo-1:7", "jira", "P2", 0, 1, 0, 1, "heuristic")}},
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

// A project reached only through fallback links a native link covers (by the
// issue, or by the pull request) is not served; a project that keeps an
// uncovered fallback link is served at that link's tier, never a covered one's.
func TestPullRequestProjectRowsDropFallbackLinksANativeLinkCovers(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}}},
		{match: "FROM work_graph_issue_pr", rows: [][]any{
			projectLinkRow("repo-1:7", "jira", "P1", "Payments", 3, "native"),
			// The pull request has a native link: every fallback link of it is
			// covered, whatever its issue.
			projectLinkRow("repo-1:7", "jira", "P2", "Search", 2, "explicit_text"),
			projectLinkRow("repo-1:7", "jira", "P3", "Billing", 2, "explicit_text"),
			projectLinkRow("repo-1:7", "jira", "P4", "Mobile", 1, "heuristic"),
		}},
		{match: pullRequestFallbackDecisionMarker, rows: [][]any{projectFallbackRow("repo-1:7", "jira", "P2", 1, 1, 1, 0, ""), projectFallbackRow("repo-1:7", "jira", "P3", 1, 2, 1, 1, "heuristic"), projectFallbackRow("repo-1:7", "jira", "P4", 1, 1, 0, 1, "heuristic")}},
	}}
	result := readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	linked := result.Facts[0].Fields["project_breakdown"]
	if linked.Table == nil {
		t.Fatalf("project_breakdown absent: %+v", result.Facts[0].Fields)
	}
	// repo-1|7 covers every fallback link of this pull request: only the native row stays.
	if len(linked.Table.Rows) != 1 || *linked.Table.Rows[0].Fields["link_tier"].String != "native" {
		t.Fatalf("project rows = %+v, want only the native project: the pull request has a native link, so every fallback link of it is covered", linked.Table.Rows)
	}
}

// The same rows with the pull request not covered: only the issue side drops.
func TestPullRequestProjectRowsDropOnlyTheCoveredIssuesWhenThePullRequestIsNotCovered(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}}},
		{match: "FROM work_graph_issue_pr", rows: [][]any{
			// Its one link's issue has a native link.
			projectLinkRow("repo-1:7", "jira", "P2", "Search", 2, "explicit_text"),
			// A covered text link beside an uncovered heuristic one: served at heuristic.
			projectLinkRow("repo-1:7", "jira", "P3", "Billing", 2, "explicit_text"),
		}},
		{match: pullRequestFallbackDecisionMarker, rows: [][]any{projectFallbackRow("repo-1:7", "jira", "P2", 0, 1, 1, 0, ""), projectFallbackRow("repo-1:7", "jira", "P3", 0, 2, 1, 1, "heuristic")}},
	}}
	result := readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	linked := result.Facts[0].Fields["project_breakdown"]
	if linked.Table == nil || len(linked.Table.Rows) != 1 {
		t.Fatalf("project rows = %+v, want one (Billing at heuristic): Search is reached only by a covered link", linked)
	}
	row := linked.Table.Rows[0].Fields
	if got, want := *row["scope_id"].String, projectCanonicalID(t, "jira", "P3"); got != want {
		t.Fatalf("scope_id = %q, want %q", got, want)
	}
	if got := *row["link_tier"].String; got != "heuristic" {
		t.Fatalf("link_tier = %q, want heuristic (the text link of the project is covered)", got)
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
		"pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number", // an existing pull request only (the shared link reader)
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
		{match: "FROM work_graph_issue_pr", rows: [][]any{projectLinkRow("repo-1:7", "jira", "P1", "Payments", 1, "guess")}},
		{match: pullRequestFallbackDecisionMarker, rows: [][]any{projectFallbackRow("repo-1:7", "jira", "P1", 0, 1, 0, 1, "guess")}},
	}}
	result := readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	if _, present := result.Facts[0].Fields["project_breakdown"]; present {
		t.Fatalf("a tier outside the vocabulary was served: %+v", result.Facts[0].Fields)
	}
}

func TestPullRequestProjectBreakdownReportsRowsItCapped(t *testing.T) {
	t.Parallel()
	var linked [][]any
	for i := 0; i < 65; i++ {
		linked = append(linked, projectLinkRow("repo-1:7", "jira", fmt.Sprintf("P%03d", i), "Project", 3, "native"))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}}},
		{match: "FROM work_graph_issue_pr", rows: linked},
	}}
	result := readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	table := result.Facts[0].Fields["project_breakdown"].Table
	if table == nil || len(table.Rows) != contextfabric.MaxFactValueRows {
		t.Fatalf("project rows = %v, want the table cap %d", table, contextfabric.MaxFactValueRows)
	}
	if !result.Truncated {
		t.Fatal("65 projects were capped to 64 and the result is not marked truncated")
	}
	projectRefs := 0
	for _, ref := range result.Facts[0].EvidenceRefIDs {
		if strings.Contains(ref, "project") {
			projectRefs++
		}
	}
	if projectRefs != contextfabric.MaxFactValueRows {
		t.Fatalf("project evidence refs = %d, want one per kept row (%d)", projectRefs, contextfabric.MaxFactValueRows)
	}
}

func TestPullRequestProjectReadAsksForOneMoreRowThanItAdmits(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(7), "open"}}}}}
	readPullRequestFacts(t, client, pullRequestSubject("repo-1", "7"))
	for _, query := range client.queries {
		if strings.Contains(query.statement, "FROM work_graph_issue_pr") {
			if !strings.Contains(query.statement, "LIMIT 5001") || !strings.Contains(query.statement, "p.provider = w.provider") {
				t.Fatalf("project link read lacks the limit-plus-one or the provider pairing:\n%s", query.statement)
			}
			return
		}
	}
	t.Fatal("no project link read was issued")
}
