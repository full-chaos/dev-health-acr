package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The team blocker rollup reaches repositories through the entity tree: a
// blocked work item belongs to the repositories its linked pull request rows
// name (work_graph_issue_pr, tier native > explicit_text > heuristic), never
// to the repository its own column names. Every row below is seeded so the
// two answers differ.
const blockerTreeOrg = "7d3a91c4-2b58-4f06-8e1a-5c9d0b6f2a13"

func TestTeamBlockerRollup_FollowsLinkedPullRequestRepositories(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "team_repo_ownership", "work_items", "work_item_dependencies", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, label := range []string{"repo-a", "repo-b", "repo-c"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), blockerTreeOrg, "acme/"+label, "github", at)
	}
	for _, label := range []string{"repo-a", "repo-b"} {
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			blockerTreeOrg, "github", "team-tree", repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	item := func(id, columnRepo, status string) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, ?, '', ?, '', 'linear', '', NULL)`,
			id, repoUUID(columnRepo), blockerTreeOrg, "title "+id, status, at)
	}
	link := func(id, prRepo string, number uint32, tier string) {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(prRepo), id, number, float32(1), tier, "", at, blockerTreeOrg)
	}
	// A link counts only when its pull request exists.
	pullRequest := func(repo string, number uint32) {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(repo), blockerTreeOrg, number, "pr", "open", at, at, "feature", "")
	}
	blocks := func(source, target string) {
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			blockerTreeOrg, source, target, "blocks", at)
	}
	// Every blocked item's own column names repo-c, which the team does not own.
	item("blocker-open", "repo-c", "open")
	item("blocker-done", "repo-c", "done")
	item("moved", "repo-c", "open")     // linked to repo-a (native)
	item("weak", "repo-c", "open")      // linked to repo-b (heuristic)
	item("both", "repo-c", "open")      // linked to repo-b natively and heuristically
	item("unlinked", "repo-c", "open")  // no link row
	item("elsewhere", "repo-c", "open") // linked to repo-c only: the team owns it by no link
	item("closed-only", "repo-c", "open")
	item("stale", "repo-c", "open") // its link names a pull request that does not exist
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, type) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'github', '', NULL, 'pr')`,
		"pr-typed", repoUUID("repo-a"), blockerTreeOrg, "pr-typed", at)
	link("moved", "repo-a", 1, "native")
	link("weak", "repo-b", 2, "heuristic")
	link("both", "repo-b", 3, "native")
	link("both", "repo-b", 4, "heuristic")
	link("elsewhere", "repo-c", 5, "native")
	link("closed-only", "repo-a", 6, "explicit_text")
	link("stale", "repo-a", 99, "native")
	link("pr-typed", "repo-a", 1, "native")
	for _, n := range []struct {
		repo   string
		number uint32
	}{{"repo-a", 1}, {"repo-a", 6}, {"repo-b", 2}, {"repo-b", 3}, {"repo-b", 4}, {"repo-c", 5}} {
		pullRequest(n.repo, n.number)
	}
	blocks("blocker-open", "stale")
	blocks("blocker-open", "pr-typed")
	blocks("blocker-open", "moved")
	blocks("blocker-open", "weak")
	blocks("blocker-open", "both")
	blocks("blocker-open", "unlinked")
	blocks("blocker-open", "elsewhere")
	blocks("blocker-done", "closed-only")

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	team := teamSubject("team-tree")
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerTreeOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{team},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	fields := factFor(t, result, team).Fields
	rows := wantTableRows(t, fields, "repository_breakdown")

	// repo-a: "moved" (native). repo-b: "weak" and "both". The team's own
	// column-based reading would have found nothing: every column names repo-c.
	wantInt(t, fields, "blocked_work_items_current", 3)
	wantInt(t, fields, "blocker_dependencies_current", 3)
	wantInt(t, fields, "blocker_dependencies_excluded_by_status_current", 1)
	wantInt(t, rows[repoUUID("repo-a")], "blocked_work_items_current", 1)
	wantInt(t, rows[repoUUID("repo-b")], "blocked_work_items_current", 2)
	// "unlinked" has no link row and "stale" a link to a missing pull request.
	wantInt(t, fields, "blocked_work_items_without_link_count", 2)

	tier := func(cells map[string]contextfabric.FactValue) string {
		if v, ok := cells["link_tier"]; ok && v.String != nil {
			return *v.String
		}
		return ""
	}
	if got := tier(rows[repoUUID("repo-a")]); got != "native" {
		t.Fatalf("repo-a link_tier = %q, want native (the counted item's only link)", got)
	}
	// "both" counts at its strongest link (native); "weak" is heuristic: the
	// repository is named at the weakest of its counted items.
	if got := tier(rows[repoUUID("repo-b")]); got != "heuristic" {
		t.Fatalf("repo-b link_tier = %q, want heuristic (weakest counted item)", got)
	}
	if got := fields["repository_link_basis"].String; got == nil || *got != "heuristic" {
		t.Fatalf("repository_link_basis = %#v, want heuristic", fields["repository_link_basis"])
	}
}
