package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The bounds of the memory row below, measured on the local runner (smallest of
// the warm runs) and given room: the peak follows the links the statement reads
// (the work item join and the issue set of the asked pull request), the
// response does not follow them at all.
// Measured: about 860 bytes per link of peak, and a 3,976-byte response at
// 10,000, 40,000 and 80,000 links.
const (
	pullRequestFallbackSetBaseBytes    = uint64(16 << 20)
	pullRequestFallbackSetBytesPerLink = 1000
	pullRequestFallbackSetResultBytes  = uint64(16 << 10)
	pullRequestFallbackSetWarmRuns     = 3
	// pullRequestFallbackSetMarker is in the logged text of the project links
	// statement, before and after the rule moved into it, and no other.
	pullRequestFallbackSetMarker = "AS pr_key"
)

// One pull request reaches one project through many links below native. The
// fallback-only rule is decided in the statement with a fixed state per (pull
// request, project) group, so the statement's response is one row whatever the
// number of links, and its peak grows by the read, not by a per-group array.
// The row is read at three link counts; a group state or a response that
// carries the links (a groupUniqArray of them) fails the result bound.
func TestPullRequestProjectFallbackLinkSetIsBoundedPerGroupOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-pr-project-fallback-set"
	at := ts(2026, 9, 18, 0, 0, 0)
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	repo := repoUUID("repo-fallback-set")
	exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo, orgID, "acme/fallback-set", "github", at)
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-wide", orgID, "linear", nil, "proj-wide", uint8(1), "active", "", at)
	exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repo, orgID, uint32(1), "pr", "open", at, at, "feature", "")
	// A second issue of the project whose own native link (to another pull
	// request) covers its heuristic link to #1: the row must not take a
	// covered link's tier.
	exec("covered issue", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES ('covered-1', ?, ?, 'covered', 'open', '', ?, '', 'linear', 'proj-wide', NULL)`, repo, orgID, at)
	exec("covered link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, 'covered-1', 1, 1, 'explicit_text', '', ?, ?)`, repo, at, orgID)
	exec("covering link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, 'covered-1', 2, 1, 'native', '', ?, ?)`, repo, at, orgID)
	addLinks := func(prefix string, count int) {
		t.Helper()
		exec("issues", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat(?, toString(number)), ?, ?, 'title', 'open', '', ?, '', 'linear', 'proj-wide', NULL FROM numbers(?)`, prefix, repo, orgID, at, uint64(count))
		exec("links", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat(?, toString(number)), 1, 1, 'heuristic', '', ?, ? FROM numbers(?)`, repo, prefix, at, orgID, uint64(count))
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	subject := pullRequestSubjectByRepoLabel("repo-fallback-set", 1)
	measure := func() (peak, result uint64) {
		t.Helper()
		for run := 0; run < pullRequestFallbackSetWarmRuns; run++ {
			mark := serverNowMicros(t, ctx, direct)
			facts, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{subject},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			table := facts.Facts[0].Fields["project_breakdown"].Table
			if table == nil || len(table.Rows) != 1 || *table.Rows[0].Fields["link_tier"].String != "heuristic" {
				t.Fatalf("project_breakdown = %+v, want proj-wide at heuristic (the explicit_text link is covered by its issue's native link)", facts.Facts[0].Fields["project_breakdown"])
			}
			if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
				t.Fatalf("flush logs (measurement did not happen): %v", err)
			}
			var runPeak, runResult, runs uint64
			if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(result_bytes), count() FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND position(query, ?) > 0`, mark, pullRequestFallbackSetMarker).Scan(&runPeak, &runResult, &runs); err != nil {
				t.Fatalf("read query_log: %v", err)
			}
			if runs != 1 {
				t.Fatalf("measurement did not happen: %d project links statements logged, want 1", runs)
			}
			if run == 0 || runPeak < peak {
				peak = runPeak
			}
			if runResult > result {
				result = runResult
			}
		}
		return peak, result
	}
	links := 0
	for _, step := range []struct {
		prefix string
		add    int
	}{{"a-", 10_000}, {"b-", 30_000}, {"c-", 40_000}} {
		addLinks(step.prefix, step.add)
		links += step.add
		peak, result := measure()
		bound := pullRequestFallbackSetBaseBytes + uint64(links)*pullRequestFallbackSetBytesPerLink
		t.Logf("links=%d peak=%d (bound %d) result_bytes=%d (bound %d)", links, peak, bound, result, pullRequestFallbackSetResultBytes)
		if peak > bound {
			t.Errorf("project links peak %d bytes at %d fallback links of one group, over the %d-byte bound", peak, links, bound)
		}
		if result > pullRequestFallbackSetResultBytes {
			t.Errorf("project links response %d bytes at %d fallback links of one group, over the %d-byte bound: the group carries its links", result, links, pullRequestFallbackSetResultBytes)
		}
	}
}
