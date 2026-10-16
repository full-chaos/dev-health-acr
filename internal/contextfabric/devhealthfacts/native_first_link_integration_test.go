package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A text or heuristic link of an issue that carries a native link does not
// count. The seed (seedProjectLinkedOrg) has i-1 -> repo-a#1 native, i-2 ->
// repo-r#2 explicit_text and i-3 -> repo-r#3 heuristic. Two fallback rows are
// added on natively linked issues: i-1 -> repo-x#7 and i-6 -> repo-x#8 (i-6's
// native link names a pull request that has no row). Neither may make repo-x
// reachable; repo-r has no native link on its issues, so it stays reached and
// is labelled by its fallback tier.
func TestReadFactsProjectReachIgnoresFallbackLinksCoveredByANativeLink(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-native-first-links"
	seedProjectLinkedOrg(t, ctx, direct, orgID)
	at := ts(2026, 9, 18, 0, 0, 0)
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-x"), orgID, "acme/repo-x", "github", at)
	fallback := func(issue, repo string, number uint32, tier string, newPullRequest bool) {
		exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(repo), issue, number, float32(1), tier, "", at, orgID)
		if newPullRequest {
			exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
				repoUUID(repo), orgID, number, "pr", "open", at, at, "feature", "")
		}
	}
	fallback("i-1", "repo-x", 7, "heuristic", true)
	// The covering native link may sit outside everything the read reaches: an
	// issue whose native link names a pull request that has no row (repo-q)
	// still covers its fallback row.
	exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		"i-6", repoUUID("repo-a"), orgID, "title i-6", at, "proj-1")
	fallback("i-6", "repo-x", 8, "heuristic", true)
	fallback("i-6", "repo-q", 1, "native", false)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("fact registry: %v", err)
	}
	gate := directread.NewSubjectGate(admitEverySubject{}, nil)
	reader := directread.NewFactsReader(gate, directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000000002"), storage.Principal{OrgID: orgID, Subject: "project-linked", CredentialID: "project-linked"}, directread.FactsRequest{
		Kinds:    []string{"investment"},
		Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: "team:team-project-only"}},
	})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var investment *directread.ServedFact
	for i := range response.Facts {
		if response.Facts[i].Kind == "investment" && response.Facts[i].Subject.CanonicalID == "team:team-project-only" {
			investment = &response.Facts[i]
		}
	}
	if investment == nil {
		t.Fatalf("no investment fact served: %s", encoded)
	}
	table, ok := investment.Tables["project_reached_repositories"]
	if !ok {
		t.Fatalf("project_reached_repositories table not served: %s", encoded)
	}
	tiers := map[string]string{}
	for _, row := range table.Rows {
		cells := map[string]any{}
		for i, column := range table.Columns {
			if i < len(row) {
				cells[column] = row[i]
			}
		}
		id, _ := cells["repository_id"].(string)
		tier, _ := cells["link_tier"].(string)
		tiers[id] = tier
	}
	if len(tiers) != 2 || tiers[repoUUID("repo-a")] != "native" || tiers[repoUUID("repo-r")] != "heuristic" {
		t.Fatalf("project_reached_repositories = %v, want repo-a native, repo-r heuristic (no native link on either side), and no repo-x (its only links are fallbacks on issues that carry a native link, one natively linked to a pull request inside the read and one to a pull request that has no row)", tiers)
	}
	if got, _ := investment.Fields["repository_link_basis"].(string); got != "heuristic" {
		t.Fatalf("repository_link_basis = %#v, want heuristic from repo-r's fallback-only links", investment.Fields["repository_link_basis"])
	}
}

// KNOWN GAP, pinned on purpose. The fallback-only rule has two sides (ops
// issueprlinks.NativeLinks.Outranks): a native link covers the issue, and a
// native link covers the pull request. These reads apply the issue side only.
// A text link of an owned project's issue to a pull request that another
// project's issue links natively still reaches the repository, at its own
// fallback tier, until the source retraction removes the row. The shared
// readers follow-up (a key-prefix or read-once link set design) closes it; when
// it does, this test turns red on purpose and is rewritten to expect no reach.
func TestReadFactsProjectReachKnownGapFallbackOnAPullRequestAnotherProjectLinksNatively(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-native-first-known-gap"
	seedProjectLinkedOrg(t, ctx, direct, orgID)
	at := ts(2026, 9, 18, 0, 0, 0)
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-x"), orgID, "acme/repo-x", "github", at)
	exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		"i-7", repoUUID("repo-a"), orgID, "title i-7", at, "proj-1")
	exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID("repo-x"), "i-7", uint32(9), float32(1), "explicit_text", "", at, orgID)
	exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID("repo-x"), "i-other-project", uint32(9), float32(1), "native", "", at, orgID)
	exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-x"), orgID, uint32(9), "pr", "open", at, at, "feature", "")

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("fact registry: %v", err)
	}
	gate := directread.NewSubjectGate(admitEverySubject{}, nil)
	reader := directread.NewFactsReader(gate, directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000000003"), storage.Principal{OrgID: orgID, Subject: "project-linked", CredentialID: "project-linked"}, directread.FactsRequest{
		Kinds:    []string{"investment"},
		Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: "team:team-project-only"}},
	})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	encoded, _ := json.Marshal(response)
	var investment *directread.ServedFact
	for i := range response.Facts {
		if response.Facts[i].Kind == "investment" && response.Facts[i].Subject.CanonicalID == "team:team-project-only" {
			investment = &response.Facts[i]
		}
	}
	if investment == nil {
		t.Fatalf("no investment fact served: %s", encoded)
	}
	table, ok := investment.Tables["project_reached_repositories"]
	if !ok {
		t.Fatalf("project_reached_repositories table not served: %s", encoded)
	}
	tiers := map[string]string{}
	for _, row := range table.Rows {
		cells := map[string]any{}
		for i, column := range table.Columns {
			if i < len(row) {
				cells[column] = row[i]
			}
		}
		id, _ := cells["repository_id"].(string)
		tier, _ := cells["link_tier"].(string)
		tiers[id] = tier
	}
	if tiers[repoUUID("repo-x")] != "explicit_text" {
		t.Fatalf("known gap changed: project_reached_repositories = %v, want repo-x still reached at explicit_text (the pull request side of the rule is not applied here); if this now holds no repo-x, the gap is closed: rewrite this test", tiers)
	}
}
