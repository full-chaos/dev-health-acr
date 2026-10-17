package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func pullRequestSubjectByRepoLabel(label string, number uint32) contextfabric.SubjectRef {
	return pullRequestSubject(repoUUID(label), fmt.Sprint(number))
}

// A project a pull request reaches only through fallback links (explicit_text,
// heuristic) that a native link covers is not served; EXECUTED against a real
// ClickHouse. repo-a#1 carries a native link (seed: i-1, project proj-1): a
// heuristic link of an issue of proj-2 to it is covered by the pull request, and
// a text link of an issue of proj-3 whose issue has a native link elsewhere is
// covered by the issue; proj-4 keeps its uncovered heuristic link.
func TestPullRequestProjectRowsDropFallbackLinksCoveredByNativeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-pr-project-cover"
	seedProjectLinkedOrg(t, ctx, direct, orgID)
	at := ts(2026, 9, 18, 0, 0, 0)
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	for _, project := range []string{"proj-2", "proj-3", "proj-4"} {
		exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, orgID, "linear", nil, project, uint8(1), "active", "", at)
	}
	issue := func(id, project string) {
		exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			id, repoUUID("repo-a"), orgID, "title "+id, at, project)
	}
	link := func(repo, item string, number uint32, tier string) {
		exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(repo), item, number, float32(1), tier, "", at, orgID)
	}
	issue("j-2", "proj-2")
	issue("j-3", "proj-3")
	issue("j-4", "proj-4")
	link("repo-a", "j-2", 1, "heuristic")     // covered by the pull request: repo-a#1 has native i-1
	link("repo-r", "j-3", 2, "explicit_text") // covered by its issue: j-3 has a native link below
	link("repo-r", "j-3", 3, "native")
	link("repo-r", "j-4", 2, "heuristic") // proj-4's only link: uncovered
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	read := func(subject contextfabric.SubjectRef) map[string]string {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		tiers := map[string]string{}
		for _, fact := range result.Facts {
			table := fact.Fields["project_breakdown"].Table
			if table == nil {
				continue
			}
			for _, row := range table.Rows {
				tiers[*row.Fields["scope_id"].String] = *row.Fields["link_tier"].String
			}
		}
		return tiers
	}
	p := func(project string) string { return projectCanonicalID(t, "linear", project) }
	// repo-a#1: proj-1 native (seed i-1); proj-2's heuristic link is covered by the pull request.
	if got := read(pullRequestSubjectByRepoLabel("repo-a", 1)); len(got) != 1 || got[p("proj-1")] != "native" {
		t.Fatalf("repo-a#1 projects = %v, want only proj-1 native (proj-2 is reached only by a heuristic link the pull request's native link covers)", got)
	}
	// repo-r#2: j-3's text link is covered by j-3's native link; j-4's heuristic link and the seed's explicit_text i-2 stay.
	got := read(pullRequestSubjectByRepoLabel("repo-r", 2))
	if _, reached := got[p("proj-3")]; reached || got[p("proj-4")] != "heuristic" {
		t.Fatalf("repo-r#2 projects = %v, want proj-4 at heuristic and no proj-3 (its text link is covered by j-3's native link)", got)
	}
}

// At the largest real organization's shape (24,024 link rows, half of them
// explicit_text next to native links) a read of pull request facts reads the
// native cover once: the cover statement reads the link table once and stays
// small, whatever the number of pull requests asked for.
func TestPullRequestProjectCoverCostAtTheLargestOrgShape(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-pr-project-cover-cost"
	shape := linkedMemoryProdShape
	shape.fallbackEvery = 2
	query, direct, _, _ := newLinkedProdShapeStore(t, ctx, orgID, shape)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	var subjects []contextfabric.SubjectRef
	for repo := 0; repo < shape.ownedRepos; repo++ {
		for number := uint32(1); number <= 20; number++ {
			subjects = append(subjects, pullRequestSubjectByRepoLabel(fmt.Sprintf("shape-r-%d", repo), number))
		}
	}
	readOnce := func() {
		t.Helper()
		if _, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactPullRequests, Subjects: subjects,
		}); err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
	}
	readOnce()
	mark := serverNowMicros(t, ctx, direct)
	readOnce()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var peak, readRows, runs uint64
	if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), count() FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_graph_issue_pr')) AND position(query, 'groupUniqArray(work_item_id)') > 0`, mark).Scan(&peak, &readRows, &runs); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	t.Logf("pull request project cover at the largest org shape: %d link rows, %d pull requests asked, %d cover statements for one read; peak %.2f MiB (bound %.0f), read_rows %d (bound %.0f)",
		shape.links, len(subjects), runs, float64(peak)/(1<<20), nativeCoverPeakBoundMiB, readRows, nativeCoverReadRowsPerLink*float64(shape.links))
	if runs != 1 {
		t.Errorf("the cover statement ran %d times for one read, want 1 (it is read once per read, only when a fallback link exists)", runs)
	}
	if float64(peak)/(1<<20) > nativeCoverPeakBoundMiB {
		t.Errorf("the native cover statement peaked at %.2f MiB, bound %.0f MiB", float64(peak)/(1<<20), nativeCoverPeakBoundMiB)
	}
	if float64(readRows) > nativeCoverReadRowsPerLink*float64(shape.links) {
		t.Errorf("the native cover statement read %d rows of a %d row table, bound %.0f: it scans the table more than once", readRows, shape.links, nativeCoverReadRowsPerLink*float64(shape.links))
	}
}
