package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// repoScopedSplitMemoryCeiling bounds the repository, team and linked pull
// request reads at every organization size measured here; they run under the
// API default (256 MiB), with this margin.
const repoScopedSplitMemoryCeiling = uint64(64 << 20)

// repoScopedSplitMemorySlope bounds how a read's peak may grow from the small
// to the large organization, as a share of how much the organization grew (6x
// the units): the split over the whole organization grew 6.5x there.
const repoScopedSplitMemorySlope = 0.6

const splitMemoryRepos = 200

// seedSplitMemoryOrg writes units work units (two versions each) whose
// references spread over splitMemoryRepos repositories: prs PR references
// plus one issue-side reference, and commit/edge ids that widen the evidence.
func seedSplitMemoryOrg(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, units, prs, commits, edges int) {
	t.Helper()
	at := ts(2026, 8, 20, 0, 0, 0)
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, 0, splitMemoryRepos)
	for i := 0; i < splitMemoryRepos; i++ {
		label := fmt.Sprintf("mem-%d", i)
		if err := batch.Append(repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatal(err)
		}
		quoted = append(quoted, "'"+repoUUID(label)+"'")
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
	exec := func(statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%.300s", err, statement)
		}
	}
	exec(fmt.Sprintf(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
WITH [%s] AS repos_arr, ['feature_delivery','operational','maintenance','quality','risk'] AS themes
SELECT concat('wu-', toString(u)),
	toDateTime64('2026-08-20 00:00:00', 3, 'UTC') - toIntervalDay(u %% 45),
	toDateTime64('2026-08-20 00:00:00', 3, 'UTC'),
	NULL,
	if(v = 0, 1000.0, toFloat64(1 + u %% 9)),
	map(themes[1 + u %% 5], 0.6, themes[1 + (u + 2) %% 5], 0.4),
	map('%s', 0.25, 'refactor', 0.15, 'docs', 0.1),
	concat('{"issues":', toJSONString([concat('ghpr:acme/mem-', toString((u * 11) %% %d), '#', toString(u)), concat('linear:CH-', toString(u))]),
		',"prs":', toJSONString(arrayMap(j -> concat(repos_arr[1 + (u * 7 + j * 13) %% %d], '#pr', toString(u * 16 + j)), range(%d))),
		',"commits":', toJSONString(arrayMap(j -> lower(hex(SHA1(concat(toString(u), '-', toString(j))))), range(%d))),
		',"edges":', toJSONString(arrayMap(j -> lower(hex(SHA256(concat('e', toString(u), '-', toString(j))))), range(%d))), '}'),
	toDateTime64('2026-08-20 00:00:00', 3, 'UTC') + toIntervalHour(v),
	'%s'
FROM (SELECT intDiv(number, 2) AS u, number %% 2 AS v FROM numbers(%d))`,
		strings.Join(quoted, ","), readers.BugfixSubcategoryKey, splitMemoryRepos, splitMemoryRepos, prs, commits, edges, orgID, 2*units))
	// One team owns a project whose issues link to every pull request the
	// units name in the first five repositories: a high fan-out linked set.
	exec(fmt.Sprintf(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('proj-mem', '%s', 'linear', NULL, 'proj-mem', 1, 'active', '', toDateTime64('2026-08-20 00:00:00', 3, 'UTC'))`, orgID))
	exec(fmt.Sprintf(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES ('team-mem', 'team-mem', NULL, toDateTime64('2026-08-20 00:00:00', 3, 'UTC'), '%s', 'linear', [], 1)`, orgID))
	exec(fmt.Sprintf(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES ('%s', 'linear', 'team-mem', 'proj-mem', NULL, 'native', toDateTime64('2026-01-01 00:00:00', 3, 'UTC'), NULL, toDateTime64('2026-08-20 00:00:00', 3, 'UTC'))`, orgID))
	links := fmt.Sprintf(`WITH [%s] AS repos_arr
SELECT u, j, repos_arr[1 + (u * 7 + j * 13) %% %d] AS repo, toUInt32(u * 16 + j) AS number
FROM (SELECT number AS u FROM numbers(%d)) ARRAY JOIN range(%d) AS j
WHERE (u * 7 + j * 13) %% %d < 5`, strings.Join(quoted, ","), splitMemoryRepos, units, prs, splitMemoryRepos)
	exec(fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at)
SELECT concat('li-', toString(u), '-', toString(j)), repo, '%s', 'title', 'open', '', toDateTime64('2026-08-20 00:00:00', 3, 'UTC'), '', 'linear', 'proj-mem', NULL FROM (%s)`, orgID, links))
	exec(fmt.Sprintf(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT repo, concat('li-', toString(u), '-', toString(j)), number, 1, 'native', '', toDateTime64('2026-08-20 00:00:00', 3, 'UTC'), '%s' FROM (%s)`, orgID, links))
	exec(fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT repo, '%s', number, 'pr', 'open', toDateTime64('2026-08-20 00:00:00', 3, 'UTC'), toDateTime64('2026-08-20 00:00:00', 3, 'UTC'), NULL, NULL, 'feature', '' FROM (%s)`, orgID, links))
	exec(fmt.Sprintf(`INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES ('%s', 'run-1', toDateTime64('2026-08-21 00:00:00', 3, 'UTC'))`, orgID))
	exec(fmt.Sprintf(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)
SELECT '%s', 'issue', concat('n', toString(number)), concat('wu-', toString(number)), 'theme', 'feature_delivery', toDateTime64('2026-08-21 00:00:00', 3, 'UTC'), 'run-1' FROM numbers(%d)`, orgID, units))
}

// The repository mix, the repository unit listing and the team linked pull
// request mix read the whole organization's latest work units once, but
// expand only the units that reach a requested repository or a linked pull
// request, so their peak memory stays under the ceiling at every organization
// size, including units with many references. The organization statements
// are measured and logged beside them.
func TestRepositoryScopedSplitStatementsStayUnderTheMemoryCeilingOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	type size struct {
		org                        string
		units, prs, commits, edges int
	}
	sizes := []size{
		{org: "org-mem-small", units: 2500, prs: 3, commits: 10, edges: 10},
		{org: "org-mem-large", units: 15000, prs: 3, commits: 10, edges: 10},
		{org: "org-mem-many-refs", units: 12000, prs: 14},
	}
	for _, s := range sizes {
		seedSplitMemoryOrg(t, ctx, direct, s.org, s.units, s.prs, s.commits, s.edges)
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	subjects := make([]contextfabric.SubjectRef, 0, 5)
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		label := fmt.Sprintf("mem-%d", i)
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
		ids = append(ids, repoUUID(label))
	}
	peakOf := func(label string, m statementMemory) uint64 {
		t.Helper()
		if len(m) != 1 {
			t.Fatalf("%s: measurement did not happen: %d statements logged, want 1; recent server statements:\n%s", label, len(m), recentServerStatements(ctx, direct))
		}
		for _, peak := range m {
			return peak
		}
		return 0
	}
	idsFor := map[string][]string{
		"repository mix": ids, "repository unit listing": ids,
		"organization unit listing": {}, "organization mix": {}, "team linked pull request mix": {"team-mem"},
	}
	repoScoped := map[string]bool{"repository mix": true, "repository unit listing": true, "team linked pull request mix": true}
	pairs := devhealthfacts.InvestmentSplitStatementPairs(ts(2026, 8, 1, 0, 0, 0), ts(2026, 9, 1, 0, 0, 0), 101)
	peaks := map[string]map[string]uint64{}
	record := func(org, name string, peak uint64) {
		if peaks[name] == nil {
			peaks[name] = map[string]uint64{}
		}
		peaks[name][org] = peak
	}
	for _, s := range sizes {
		var mark int64
		for i := 0; i < 2; i++ {
			mark = serverNowMicros(t, ctx, direct)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: s.org}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
			})
			if err != nil {
				t.Fatalf("%s: ReadFacts: %v: %s", s.org, err, readFailureCause(err))
			}
			if len(result.Facts) != len(subjects) {
				t.Fatalf("%s: %d investment facts served for %d repositories", s.org, len(result.Facts), len(subjects))
			}
		}
		served := peakOf(s.org+" served repository mix", statementMemorySince(t, ctx, direct, statementFilter{Table: "work_unit_investments", QueryContains: "toUInt8(if(win < 0, 255, win)) AS window"}, mark))
		t.Logf("%s (%d units, %d PR refs): served repository mix peak %.2f MiB", s.org, s.units, s.prs, float64(served)/(1<<20))
		record(s.org, "served repository mix", served)
		if served >= repoScopedSplitMemoryCeiling {
			t.Errorf("%s: served repository mix peaked at %d bytes, want under %d", s.org, served, repoScopedSplitMemoryCeiling)
		}
		for _, p := range pairs {
			for i := 0; i < 2; i++ {
				mark = serverNowMicros(t, ctx, direct)
				err := readers.QueryOrgScopedNamed(ctx, query, "SplitMemory", p.New, s.org, idsFor[p.Name], func(readers.RowScanner) error { return nil }, p.Bindings...)
				if err != nil {
					t.Fatalf("%s %s: %v: %s", s.org, p.Name, err, readFailureCause(err))
				}
			}
			peak := peakOf(s.org+" "+p.Name, statementMemorySince(t, ctx, direct, statementFilter{Table: "work_unit_investments"}, mark))
			t.Logf("%s (%d units, %d PR refs): %s peak %.2f MiB", s.org, s.units, s.prs, p.Name, float64(peak)/(1<<20))
			if p.Name == "team linked pull request mix" {
				// The linked set appears in the statement several times as one
				// scalar subquery; it must be built once per statement.
				var misses uint64
				if err := direct.QueryRow(ctx, `SELECT max(ProfileEvents['ScalarSubqueriesCacheMiss']) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, 'uniqExact(m.repo_uuid)') > 0`, mark).Scan(&misses); err != nil {
					t.Fatalf("%s: read scalar subquery events: %v", s.org, err)
				}
				t.Logf("%s: linked mix scalar subquery cache misses %d", s.org, misses)
				if misses == 0 || misses > 2 {
					t.Errorf("%s: linked mix built %d scalar subqueries, want the linked set and its lookup rows once each (2)", s.org, misses)
				}
			}
			if !repoScoped[p.Name] {
				continue
			}
			record(s.org, p.Name, peak)
			if peak >= repoScopedSplitMemoryCeiling {
				t.Errorf("%s: %s peaked at %d bytes, want under %d", s.org, p.Name, peak, repoScopedSplitMemoryCeiling)
			}
		}
	}
	unitGrowth := float64(sizes[1].units) / float64(sizes[0].units)
	for name, byOrg := range peaks {
		small, large := byOrg[sizes[0].org], byOrg[sizes[1].org]
		if small == 0 || large == 0 {
			t.Fatalf("%s: measurement did not happen at both sizes (%d, %d)", name, small, large)
		}
		growth := float64(large) / float64(small)
		t.Logf("%s grew %.2fx for %.0fx the units", name, growth, unitGrowth)
		if growth > repoScopedSplitMemorySlope*unitGrowth {
			t.Errorf("%s grew %.2fx from %d to %d units, want at most %.2fx", name, growth, sizes[0].units, sizes[1].units, repoScopedSplitMemorySlope*unitGrowth)
		}
	}
}
