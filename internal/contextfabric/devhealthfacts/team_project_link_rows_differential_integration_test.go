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
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
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
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			org, p.provider, team, p.id, key, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	items := []struct{ id, provider, project, typ string }{
		{"i-a1", "linear", "proj-a", "issue"}, {"i-a2", "linear", "proj-a", "issue"}, {"i-b1", "linear", "proj-b", "issue"},
		{"i-c1", "jira", "KEYC", "issue"}, {"i-pr", "linear", "proj-a", "pr"}, {"i-other", "linear", "proj-other", "issue"},
		{"i-none", "linear", "", "issue"}, {"i-stale", "linear", "proj-b", "issue"}, {"i-weird", "linear", "proj-b", "issue"},
		{"i-tier-up", "linear", "proj-a", "issue"}, {"i-tier-down", "linear", "proj-a", "issue"}, {"i-tier-text", "linear", "proj-a", "issue"},
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
		// Equal versions: which row FINAL keeps is the engine's, so these two items
		// pin only that the statements before and after the change agree on it.
		{"i-tie-project", "proj-a", ts(2026, 9, 10, 0, 0, 0)}, {"i-tie-project", "proj-other", ts(2026, 9, 10, 0, 0, 0)},
		{"i-tie-type", "proj-a", ts(2026, 9, 10, 0, 0, 0)}, {"i-tie-type", "proj-a", ts(2026, 9, 10, 0, 0, 0)},
	}
	for index, v := range versions {
		itemType := "issue"
		if v.id == "i-tie-type" && index%2 == 1 {
			itemType = "pr"
		}
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, type, last_synced) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL, ?, ?)`,
			v.id, repoUUID("repo-x"), org, "t", at, v.project, itemType, v.lastSynced)
	}
	for _, id := range []string{"i-moved-away", "i-moved-in", "i-tie-project", "i-tie-type"} {
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
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
	// Mixed tiers on one (issue, pull request) pair, each version its own part:
	// the stronger tier wins whichever version is newer, so a native-linked pull
	// request is served at the native tier only.
	mixed := []struct {
		item, tier string
		lastSynced time.Time
	}{
		{"i-tier-up", "heuristic", ts(2026, 9, 1, 0, 0, 0)}, {"i-tier-up", "native", ts(2026, 9, 20, 0, 0, 0)},
		{"i-tier-down", "native", ts(2026, 9, 1, 0, 0, 0)}, {"i-tier-down", "heuristic", ts(2026, 9, 20, 0, 0, 0)},
		{"i-tier-text", "explicit_text", ts(2026, 9, 1, 0, 0, 0)}, {"i-tier-text", "heuristic", ts(2026, 9, 20, 0, 0, 0)},
	}
	for _, link := range mixed {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID("repo-y"), link.item, uint32(3), float32(1), link.tier, "", link.lastSynced, org)
	}
	rowsOf := func(ctx context.Context, statement string) [][5]string {
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
	var candidates []string
	if err := direct.QueryRow(ctx, devhealthfacts.TeamLinkCandidatesStatementForTest(),
		clickhouse.Named("org_id", org), clickhouse.Named("ids", []string{"team-owner", "team-other"})).Scan(&candidates); err != nil {
		t.Fatalf("team link candidates: %v", err)
	}
	table, err := devhealthfacts.TeamLinkCandidatesTableForTest(candidates)
	if err != nil {
		t.Fatal(err)
	}
	sent := clickhouse.Context(ctx, clickhouse.WithExternalTable(table))
	old, current := rowsOf(ctx, devhealthfacts.TeamProjectLinkRowsPreStreamingSQLForTest()), rowsOf(sent, devhealthfacts.TeamProjectLinkRowsSQLForTest())
	if len(old) < 7 {
		t.Fatalf("the seed reached %d link rows before the change, want at least 6: %v", len(old), old)
	}
	if !reflect.DeepEqual(old, current) {
		t.Fatalf("link rows differ.\nbefore: %s\nafter:  %s", fmt.Sprint(old), fmt.Sprint(current))
	}
	// The candidate prunes only narrow what is read: with all three removed the
	// rows are the same.
	if unpruned := rowsOf(ctx, devhealthfacts.TeamProjectLinkRowsUnprunedSQLForTest()); !reflect.DeepEqual(unpruned, current) {
		t.Fatalf("link rows with the candidate prunes differ from the rows without them.\nwithout: %s\nwith:    %s", fmt.Sprint(unpruned), fmt.Sprint(current))
	}
	// Planted collisions: every link hash of the organization passes the hash
	// prunes, and the real-id joins still keep exactly the candidates' rows.
	if collided := rowsOf(sent, devhealthfacts.TeamProjectLinkRowsEveryHashSQLForTest()); !reflect.DeepEqual(collided, current) {
		t.Fatalf("link rows with every link hash admitted differ from the pruned rows.\nevery hash: %s\npruned:     %s", fmt.Sprint(collided), fmt.Sprint(current))
	}
	var movedIn, movedAway bool
	for _, row := range current {
		if row[0] != "team-owner" {
			continue
		}
		movedIn = movedIn || row[1] == "i-moved-in"
		movedAway = movedAway || row[1] == "i-moved-away"
	}
	if !movedIn || movedAway {
		t.Fatalf("stale versions: i-moved-in served = %v (want true), i-moved-away served = %v (want false): %v", movedIn, movedAway, current)
	}
	wantMixed := map[string]string{"i-tier-up": "3", "i-tier-down": "3", "i-tier-text": "2"}
	gotMixed := map[string][]string{}
	for _, row := range current {
		if _, ok := wantMixed[row[1]]; ok && row[0] == "team-owner" && row[3] == "3" {
			gotMixed[row[1]] = append(gotMixed[row[1]], row[4])
		}
	}
	for item, rank := range wantMixed {
		if got := gotMixed[item]; len(got) != 1 || got[0] != rank {
			t.Fatalf("mixed tiers: %s served link ranks %v, want exactly [%s]: %v", item, got, rank, current)
		}
	}
	reachedTwice := 0
	for _, row := range current {
		if row[3] == "1" {
			reachedTwice++
		}
	}
	if reachedTwice < 3 {
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		t.Fatalf("no pull request is linked from two issues of the owned projects: %v", current)
	}
}
