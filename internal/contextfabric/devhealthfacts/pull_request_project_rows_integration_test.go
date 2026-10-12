package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The project_breakdown of a pull request fact against a real ClickHouse: the
// statement uses FINAL, argMax and joins a fake client cannot execute. The
// fixture reuses the entity tree (project PROJ-TREE; native / heuristic /
// explicit_text links) and adds the cases the strict reading must exclude.
func TestPullRequestFactProjectRowsFollowTheStrictLinkReading(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedEntityTree(t, ctx, direct, at)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	pullRequest := func(label, repoID string, number uint32) {
		exec(label+" pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			repoID, policyTreeOrgID, number, "PR", "open", at, at, "feat/x", "")
	}
	link := func(label, itemID, repoID string, number uint32, provenance string) {
		exec(label, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, itemID, number, float32(0.9), provenance, "", at, policyTreeOrgID)
	}
	item := func(id, provider, itemType, projectID string) {
		exec("item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, type, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, policyTreeRepoC, policyTreeOrgID, provider, "item "+id, itemType, "open", "", "", projectID, at)
	}
	// The entity-tree seed writes issues with no provider, and the project is
	// paired with the work item's own provider: re-seed the seeded issues with
	// the project's provider.
	exec("clear seeded issues", `TRUNCATE TABLE work_items`)
	for _, id := range []string{"I-native-a", "I-heuristic-b", "I-text-b", "I-unlinked"} {
		item(id, policyTreeProvider, "issue", policyTreeProject)
	}

	// PR 1 (seeded: a native link) also has a weaker explicit_text link from a
	// second issue of the same project: one row, strongest tier native.
	item("I-text-a", policyTreeProvider, "issue", policyTreeProject)
	link("extra explicit_text link to PR 1", "I-text-a", policyTreeRepoA, 1, "explicit_text")
	// A pull-request-typed work item of the project, linked to an existing PR 5.
	pullRequest("PR 5", policyTreeRepoA, 5)
	item("I-pr-typed", policyTreeProvider, "pr", policyTreeProject)
	link("pr-typed item link to PR 5", "I-pr-typed", policyTreeRepoA, 5, "native")
	// A project key that resolves to two projects: omitted, never guessed.
	for _, id := range []string{"AMB-1", "AMB-2"} {
		exec("project "+id, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, policyTreeOrgID, policyTreeProvider, "AMB", id, uint8(1), "active", "", at)
	}
	pullRequest("PR 6", policyTreeRepoA, 6)
	item("I-ambiguous", policyTreeProvider, "issue", "AMB")
	link("ambiguous project link to PR 6", "I-ambiguous", policyTreeRepoA, 6, "native")
	// A stale link: the pull request it names does not exist, so no pull
	// request fact exists to carry a project row.
	item("I-stale", policyTreeProvider, "issue", policyTreeProject)
	link("stale link to PR 99", "I-stale", policyTreeRepoA, 99, "native")
	// A pull request nothing links to.
	pullRequest("PR 7", policyTreeRepoA, 7)
	// One project id string under two providers: a jira work item must not
	// reach the linear project that carries the same string.
	exec("linear project SHARED", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"SHARED", policyTreeOrgID, "linear", nil, "Shared", uint8(1), "active", "", at)
	exec("jira work item on SHARED", `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, type, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"JIRA-SHARED", policyTreeRepoC, policyTreeOrgID, "jira", "jira issue", "issue", "open", "", "", "SHARED", at)
	pullRequest("PR 8", policyTreeRepoA, 8)
	link("cross-provider link to PR 8", "JIRA-SHARED", policyTreeRepoA, 8, "native")

	subject := func(repoID string, number string) contextfabric.SubjectRef {
		return pullRequestSubject(repoID, number)
	}
	subjects := []contextfabric.SubjectRef{
		subject(policyTreeRepoA, "1"), subject(policyTreeRepoB, "2"), subject(policyTreeRepoB, "3"),
		subject(policyTreeRepoA, "5"), subject(policyTreeRepoA, "6"), subject(policyTreeRepoA, "7"), subject(policyTreeRepoA, "8"), subject(policyTreeRepoA, "99"),
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: policyTreeOrgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactPullRequests, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	projectID := policyTreeProjectOrigin(t).CanonicalID
	byID := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byID[fact.Subject.CanonicalID] = fact
	}
	tierOf := func(repoID, number string) (string, bool) {
		fact, ok := byID["pull_request:"+repoID+":"+number]
		if !ok {
			t.Fatalf("no fact for pull request %s:%s", repoID, number)
		}
		table := fact.Fields["project_breakdown"].Table
		if table == nil {
			return "", false
		}
		if len(table.Rows) != 1 {
			t.Fatalf("pull request %s:%s project rows = %d, want exactly one", repoID, number, len(table.Rows))
		}
		row := table.Rows[0].Fields
		if got := *row["scope_id"].String; got != projectID {
			t.Fatalf("pull request %s:%s scope_id = %q, want %q", repoID, number, got, projectID)
		}
		if got := *row["scope"].String; got != "project" {
			t.Fatalf("scope = %q", got)
		}
		return *row["link_tier"].String, true
	}
	for _, tc := range []struct{ repoID, number, tier string }{
		{policyTreeRepoA, "1", "native"},        // strongest among native + explicit_text
		{policyTreeRepoB, "2", "heuristic"},     // the only link
		{policyTreeRepoB, "3", "explicit_text"}, // outranks nothing here, named as is
	} {
		if got, ok := tierOf(tc.repoID, tc.number); !ok || got != tc.tier {
			t.Fatalf("pull request %s:%s tier = %q (present %v), want %q", tc.repoID, tc.number, got, ok, tc.tier)
		}
	}
	// Excluded: a pull-request-typed item, an ambiguous project key, and a
	// pull request nothing links to carry no project rows at all.
	for _, tc := range []struct{ repoID, number, why string }{
		{policyTreeRepoA, "5", "a pull-request-typed work item is never an issue"},
		{policyTreeRepoA, "6", "an ambiguous project key is omitted"},
		{policyTreeRepoA, "7", "no link is no row"},
		{policyTreeRepoA, "8", "a work item reaches only a project of its own provider"},
	} {
		if tier, ok := tierOf(tc.repoID, tc.number); ok {
			t.Fatalf("pull request %s:%s carries a project row (tier %q): %s", tc.repoID, tc.number, tier, tc.why)
		}
	}
	if _, ok := byID["pull_request:"+policyTreeRepoA+":99"]; ok {
		t.Fatal("a stale link produced a pull request fact")
	}
}
