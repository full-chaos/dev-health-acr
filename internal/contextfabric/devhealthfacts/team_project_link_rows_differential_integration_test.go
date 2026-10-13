package devhealthfacts_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

const linkRowsDifferentialOrg = "a3c71e58-2f94-4b06-8d1a-9e4b60c7d235"

// The team project link rows return the same rows before and after the
// organization's link rows and pull requests were streamed past the owned
// issues. The seed carries what the rule decides: a team with several
// projects (matched by id and by key), a pull request linked from issues of
// two projects, all three provenance tiers and one outside the vocabulary, a
// stale link to a missing pull request, a pull-request-typed work item, and
// issues of another team's project and of no project.
func TestTeamProjectLinkRowsMatchThePreStreamingStatementOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
	for _, statement := range devhealthschema.DDL("repos", "teams", "work_items", "work_graph_issue_pr", "git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	org, at := linkRowsDifferentialOrg, ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, p := range []struct{ id, key, provider string }{
		{"proj-a", "", "linear"}, {"proj-b", "", "linear"}, {"proj-c", "KEYC", "jira"}, {"proj-other", "", "linear"},
	} {
		var key any
		if p.key != "" {
			key = p.key
		}
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			p.id, org, p.provider, key, p.id, uint8(1), "active", "", at)
		team := "team-owner"
		if p.id == "proj-other" {
			team = "team-other"
		}
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			org, p.provider, team, p.id, key, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	items := []struct{ id, provider, project, typ string }{
		{"i-a1", "linear", "proj-a", "issue"}, {"i-a2", "linear", "proj-a", "issue"}, {"i-b1", "linear", "proj-b", "issue"},
		{"i-c1", "jira", "KEYC", "issue"}, {"i-pr", "linear", "proj-a", "pr"}, {"i-other", "linear", "proj-other", "issue"},
		{"i-none", "linear", "", "issue"}, {"i-stale", "linear", "proj-b", "issue"}, {"i-weird", "linear", "proj-b", "issue"},
	}
	for _, item := range items {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, type) VALUES (?, ?, ?, ?, 'open', '', ?, '', ?, ?, NULL, ?)`,
			item.id, repoUUID("repo-x"), org, "t", at, item.provider, item.project, item.typ)
	}
	versions := []struct {
		id, project string
		lastSynced  time.Time
	}{
		{"i-moved-away", "proj-a", ts(2026, 9, 1, 0, 0, 0)}, {"i-moved-away", "proj-other", ts(2026, 9, 20, 0, 0, 0)},
		{"i-moved-in", "proj-other", ts(2026, 9, 1, 0, 0, 0)}, {"i-moved-in", "proj-a", ts(2026, 9, 20, 0, 0, 0)},
	}
	for _, v := range versions {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, type, last_synced) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL, 'issue', ?)`,
			v.id, repoUUID("repo-x"), org, "t", at, v.project, v.lastSynced)
	}
	for _, id := range []string{"i-moved-away", "i-moved-in"} {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID("repo-y"), id, uint32(1), float32(1), "native", "", at, org)
	}
	for _, number := range []uint32{1, 2, 3, 4, 5, 6, 7} {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID("repo-y"), org, number, "pr", "open", at, at, "feature", "")
	}
	for _, link := range []struct {
		item, tier string
		number     uint32
	}{
		{"i-a1", "native", 1}, {"i-a2", "heuristic", 1}, {"i-a2", "explicit_text", 2}, {"i-b1", "native", 1},
		{"i-c1", "explicit_text", 4}, {"i-pr", "native", 5}, {"i-other", "native", 6}, {"i-none", "native", 7},
		{"i-stale", "native", 99}, {"i-weird", "mystery", 2},
	} {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID("repo-y"), link.item, link.number, float32(1), link.tier, "", at, org)
	}
	rowsOf := func(statement string) [][5]string {
		t.Helper()
		rows, err := direct.Query(ctx, `SELECT toString(team_id), work_item_id, toString(pr_repo_id), toString(pr_number), toString(link_rank) FROM `+statement+` ORDER BY 1, 2, 3, 4, 5`,
			clickhouse.Named("org_id", org), clickhouse.Named("ids", []string{"team-owner", "team-other"}))
		if err != nil {
			t.Fatalf("query: %v\n%s", err, statement)
		}
		defer rows.Close()
		var out [][5]string
		for rows.Next() {
			var row [5]string
			if err := rows.Scan(&row[0], &row[1], &row[2], &row[3], &row[4]); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, row)
		}
		return out
	}
	old, current := rowsOf(devhealthfacts.TeamProjectLinkRowsPreStreamingSQLForTest()), rowsOf(devhealthfacts.TeamProjectLinkRowsSQLForTest())
	if len(old) < 7 {
		t.Fatalf("the seed reached %d link rows before the change, want at least 6: %v", len(old), old)
	}
	if !reflect.DeepEqual(old, current) {
		t.Fatalf("link rows differ.\nbefore: %s\nafter:  %s", fmt.Sprint(old), fmt.Sprint(current))
	}
	var movedIn, movedAway bool
	for _, row := range current {
		movedIn = movedIn || row[1] == "i-moved-in"
		movedAway = movedAway || row[1] == "i-moved-away"
	}
	if !movedIn || movedAway {
		t.Fatalf("stale versions: i-moved-in served = %v (want true), i-moved-away served = %v (want false): %v", movedIn, movedAway, current)
	}
	reachedTwice := 0
	for _, row := range current {
		if row[3] == "1" {
			reachedTwice++
		}
	}
	if reachedTwice < 3 {
		t.Fatalf("no pull request is linked from two issues of the owned projects: %v", current)
	}
}
