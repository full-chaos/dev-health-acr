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

// repoScopedSplitMemoryCeiling is 3/4 of the 64 MiB API statement cap: a read
// scoped to some repositories must stay clear of it however large the
// organization around them is.
const repoScopedSplitMemoryCeiling = uint64(48 << 20)

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
	exec(fmt.Sprintf(`INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES ('%s', 'run-1', toDateTime64('2026-08-21 00:00:00', 3, 'UTC'))`, orgID))
	exec(fmt.Sprintf(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)
SELECT '%s', 'issue', concat('n', toString(number)), concat('wu-', toString(number)), 'theme', 'feature_delivery', toDateTime64('2026-08-21 00:00:00', 3, 'UTC'), 'run-1' FROM numbers(%d)`, orgID, units))
}

// The repository mix and the repository unit listing read the whole
// organization's latest work units once, but expand only the units that
// reach a requested repository, so their peak memory stays under the
// repository-scoped ceiling at every organization size. The organization
// statements are measured and logged beside them.
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
		"organization unit listing": {}, "organization mix": {}, "team linked pull request mix": {"team-none"},
	}
	repoScoped := map[string]bool{"repository mix": true, "repository unit listing": true}
	pairs := devhealthfacts.InvestmentSplitStatementPairs(ts(2026, 8, 1, 0, 0, 0), ts(2026, 9, 1, 0, 0, 0), 101)
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
			if repoScoped[p.Name] && peak >= repoScopedSplitMemoryCeiling {
				t.Errorf("%s: %s peaked at %d bytes, want under %d", s.org, p.Name, peak, repoScopedSplitMemoryCeiling)
			}
		}
	}
}
