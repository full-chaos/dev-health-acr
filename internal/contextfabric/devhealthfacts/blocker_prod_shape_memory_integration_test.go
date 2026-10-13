package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const blockerProdShapeOrg = "7d3a9c58-1e46-4f02-b8a7-5c20e9d14b63"

// The production organization the team blocker statements peak on is small in
// work items and edges and large in append-only ownership history. This seeds
// that shape (row counts read from production on 2026-10-13: 2,103 blocks
// edges over 1,602 endpoints, 17k work items, 24k link rows, 39 teams of which
// 6 own repositories, 67k team_project_ownership versions over 88 pairs, 44k
// team_repo_ownership versions over 24 pairs) so the statements' memory is
// measured on the data that makes them peak.
func seedProdShapedBlockerOrganization(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}) {
	t.Helper()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	org := blockerProdShapeOrg
	at := ts(2026, 9, 29, 0, 0, 0)
	repoUUIDExpr := func(expr string) string {
		return fmt.Sprintf("toUUID(concat('00000000-0000-4000-8000-0000000000', lpad(toString(%s), 2, '0')))", expr)
	}
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT `+repoUUIDExpr("number")+`, ?, concat('acme/repo-', toString(number)), 'github', ? FROM numbers(11)`, org, at)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) SELECT concat('team-', toString(number)), concat('team-', toString(number)), NULL, ?, ?, 'linear', [], 1 FROM numbers(39)`, at, org)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('team-', toString((number % 24) % 6)), `+repoUUIDExpr("(number % 24) % 11")+`, concat('acme/repo-', toString((number % 24) % 11)), 'exact', 'native', 1, 100, 0,
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24)),
  if(intDiv(number, 24) = 1847 AND number % 24 < 18, NULL, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24)) + toIntervalMinute(30)),
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24))
FROM numbers(44352)`, org)
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) SELECT concat('proj-', toString(number % 33)), ?, 'linear', NULL, concat('proj-', toString(number % 33)), 1, 'active', '', ? + toIntervalMinute(number) FROM numbers(47)`, org, at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('team-', toString((number % 88) % 36)), concat('proj-', toString((number % 88) % 48)), NULL, 'native',
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88)),
  if(intDiv(number, 88) = 766 AND number % 88 < 60, NULL, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88)) + toIntervalMinute(30)),
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88))
FROM numbers(67496)`, org)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('wi-', toString(number)), `+repoUUIDExpr("number % 11")+`, ?, 'title', 'issue', if(number % 5 = 0, 'done', 'open'), '', ?, ?, '', 'linear', concat('proj-', toString(number % 33)), NULL, ? FROM numbers(17000)`, org, at, at, at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT `+repoUUIDExpr("number % 11")+`, ?, toUInt32(intDiv(number, 11) + 1), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(7755)`, org, at, at)
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT `+repoUUIDExpr("(number * 7) % 11")+`, concat('wi-', toString(number % 14017)), toUInt32((number * 31) % 705 + 1), 1, multiIf(number % 3 = 0, 'native', number % 3 = 1, 'explicit_text', 'heuristic'), '', ?, ? FROM numbers(23905)`, at, org)
	exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at)
SELECT ?, concat('wi-', toString(number % 1602)), concat('wi-', toString((number * 37 + 5 + intDiv(number, 1602)) % 1602)), 'blocks', 'blocks', ?, ? - toIntervalDay(number % 90) FROM numbers(2103)`, org, at, at)
	exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at)
SELECT ?, concat('wi-', toString(number % 17000)), concat('wi-', toString((number * 11 + 3) % 17000)), 'relates_to', 'relates', ?, NULL FROM numbers(21142)`, org, at)
	exec(`INSERT INTO work_item_dependency_first_seen (org_id, source_work_item_id, target_work_item_id, relationship_type, first_seen_at)
SELECT ?, concat('wi-', toString(number % 1602)), concat('wi-', toString((number * 37 + 5 + intDiv(number, 1602)) % 1602)), 'blocks', ? - toIntervalDay(number % 120) FROM numbers(2103)`, org, at)
}

// Logs every statement the team blocker read runs against the production
// shape, with its peak memory and its head, so a reduction is judged on the
// data that makes the production statements peak.
func TestTeamBlockerStatementsOnTheProductionShapeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedProdShapedBlockerOrganization(t, ctx, direct)

	subjects := make([]contextfabric.SubjectRef, 0, 39)
	for team := 0; team < 39; team++ {
		subjects = append(subjects, teamSubject(fmt.Sprintf("team-%d", team)))
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	read := func() contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
		}
		return result
	}
	read()
	mark := serverNowMicros(t, ctx, direct)
	result := read()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT normalized_query_hash, max(memory_usage), max(read_rows), any(substring(replaceRegexpAll(query, '\\s+', ' '), 1, 110))
FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_item_dependencies'))
GROUP BY normalized_query_hash ORDER BY max(memory_usage) DESC`, mark)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	measured := 0
	for rows.Next() {
		var hash, peak, readRows uint64
		var head string
		if err := rows.Scan(&hash, &peak, &readRows, &head); err != nil {
			t.Fatal(err)
		}
		measured++
		t.Logf("PRODSHAPE statement %d peak %d bytes (%.1f MiB) read_rows %d: %s", hash, peak, float64(peak)/(1<<20), readRows, head)
	}
	if measured < 4 {
		t.Fatalf("measured %d statements, want the team blocker statements: the measurement did not happen", measured)
	}
	fragmentMark := serverNowMicros(t, ctx, direct)
	repoKeys := make([]string, 0, 11)
	for repo := 0; repo < 11; repo++ {
		repoKeys = append(repoKeys, fmt.Sprintf("00000000-0000-4000-8000-0000000000%02d", repo))
	}
	teamIDs := make([]string, 0, 39)
	for team := 0; team < 39; team++ {
		teamIDs = append(teamIDs, fmt.Sprintf("team-%d", team))
	}
	if err := devhealthfacts.RunBlockerFragmentsForTest(ctx, query, storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"*"}}, blockerProdShapeOrg, repoKeys, teamIDs); err != nil {
		t.Fatalf("fragments: %v", err)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	for _, name := range devhealthfacts.BlockerFragmentNames {
		var peak, readRows uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows) FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select' AND position(query, ?) > 0`, fragmentMark, "'frag-"+name+"'").Scan(&peak, &readRows); err != nil {
			t.Fatalf("fragment %s: %v", name, err)
		}
		t.Logf("PRODSHAPE fragment %-18s peak %d bytes (%.1f MiB) read_rows %d", name, peak, float64(peak)/(1<<20), readRows)
	}
	teamsServed := 0
	for _, fact := range result.Facts {
		if fact.Subject.Kind == contextfabric.SubjectTeam {
			teamsServed++
		}
	}
	t.Logf("PRODSHAPE teams with a served blockers fact: %d of 39", teamsServed)
}
