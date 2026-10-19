package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
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

// The fallback decision statement's bounds at the largest organization's
// shape, half its links explicit_text. Measured locally: 14.84 MiB and 114,050
// rows read for 24,024 link rows (before the decision moved out of it, the one
// statement read 16.67 MiB and 138,074 rows; before the fallback-only rule,
// 13.54 MiB and 66,002 rows plus a 4.54 MiB, 24,024-row cover statement).
const (
	pullRequestProjectLinksPeakBoundMiB    = 24.0
	pullRequestProjectLinksReadRowsPerLink = 7.0
)

// nativeCoverLogMarker is in the logged text of the separate native cover
// statement (readNativeCover) and no other.
const nativeCoverLogMarker = "groupUniqArray(work_item_id)"

// statementRuns reads, from the server's query log since mark, the peak
// memory, the most rows read and the count of the finished statements whose
// logged text holds marker.
func statementRuns(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, mark int64, marker string) (peak, readRows, runs uint64) {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var threadless uint64
	if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), count(), `+statementthreads.ThreadlessCount+` FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_graph_issue_pr')) AND position(query, ?) > 0`, mark, marker).Scan(&peak, &readRows, &runs, &threadless); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, marker, threadless)
	return peak, readRows, runs
}

// At the largest real organization's shape (24,024 link rows) a read of pull
// request facts sends the project links statement once, and the fallback
// decision statement once when half the links are explicit_text next to
// native ones and never when every link is native; no separate native cover
// statement runs, and the decision statement stays small.
func TestPullRequestProjectCoverCostAtTheLargestOrgShape(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name          string
		fallbackEvery int
		decisions     uint64
	}{{"half explicit_text", 2, 1}, {"all native", 0, 0}} {
		t.Run(c.name, func(t *testing.T) {
			orgID := "org-pr-project-cover-cost-" + fmt.Sprint(c.fallbackEvery)
			shape := linkedMemoryProdShape
			shape.fallbackEvery = c.fallbackEvery
			rawQuery, direct, _, _ := newLinkedProdShapeStore(t, ctx, orgID, shape)
			query := statementthreads.API(rawQuery)
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
			_, _, links := statementRuns(t, ctx, direct, mark, "AS pr_key")
			peak, readRows, decisions := statementRuns(t, ctx, direct, mark, pullRequestFallbackDecisionMarker)
			t.Logf("pull request project links at the largest org shape (%s): %d link rows, %d pull requests asked, %d link statements and %d decision statements for one read; decision peak %.2f MiB (bound %.0f), read_rows %d (bound %.0f)",
				c.name, shape.links, len(subjects), links, decisions, float64(peak)/(1<<20), pullRequestProjectLinksPeakBoundMiB, readRows, pullRequestProjectLinksReadRowsPerLink*float64(shape.links))
			if links != 1+c.decisions || decisions != c.decisions {
				t.Errorf("one read sent %d statements with the pr_key alias and %d decision statements, want %d and %d", links, decisions, 1+c.decisions, c.decisions)
			}
			if float64(peak)/(1<<20) > pullRequestProjectLinksPeakBoundMiB {
				t.Errorf("the decision statement peaked at %.2f MiB, bound %.0f MiB", float64(peak)/(1<<20), pullRequestProjectLinksPeakBoundMiB)
			}
			if float64(readRows) > pullRequestProjectLinksReadRowsPerLink*float64(shape.links) {
				t.Errorf("the decision statement read %d rows for a %d row link table, bound %.0f", readRows, shape.links, pullRequestProjectLinksReadRowsPerLink*float64(shape.links))
			}
			var covers uint64
			if err := direct.QueryRow(ctx, `SELECT count() FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND position(query, ?) > 0`, mark, nativeCoverLogMarker).Scan(&covers); err != nil {
				t.Fatalf("read query_log: %v", err)
			}
			if covers != 0 {
				t.Errorf("a separate native cover statement ran %d times for the pull request read, want 0", covers)
			}
		})
	}
}
