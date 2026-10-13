package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const probeRepos = 200

type probeDataset struct {
	org                 string
	units               int
	commits, edges, prs int
}

func probeSeed(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, d probeDataset) {
	t.Helper()
	at := ts(2026, 9, 20, 0, 0, 0)
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, 0, probeRepos)
	for i := 0; i < probeRepos; i++ {
		label := fmt.Sprintf("repo-%d", i)
		if err := batch.Append(repoUUID(label), d.org, "acme/"+label, "github", at); err != nil {
			t.Fatal(err)
		}
		quoted = append(quoted, "'"+repoUUID(label)+"'")
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}
	statement := fmt.Sprintf(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
WITH [%s] AS repos_arr, ['feature_delivery','operational','maintenance','quality','risk'] AS themes
SELECT concat('wu-', toString(u)),
	toDateTime64('2026-09-01 00:00:00', 3, 'UTC') - toIntervalDay(u %% 60),
	toDateTime64('2026-09-20 00:00:00', 3, 'UTC'),
	NULL,
	if(v = 0, 1000.0, toFloat64(1 + u %% 9)),
	map(themes[1 + u %% 5], 0.6, themes[1 + (u + 2) %% 5], 0.4),
	map('%s', 0.25, 'refactor', 0.15, 'docs', 0.1, 'tests', 0.1),
	concat('{"issues":', toJSONString([concat('ghpr:acme/repo-', toString((u * 11) %% %d), '#', toString(u)), concat('linear:CH-', toString(u)), concat('jira:OPS-', toString(u))]),
		',"prs":', toJSONString(arrayMap(j -> concat(repos_arr[1 + (u * 7 + j * 13) %% %d], '#pr', toString(u * 4 + j)), range(%d))),
		',"commits":', toJSONString(arrayMap(j -> lower(hex(SHA1(concat(toString(u), '-', toString(j))))), range(%d))),
		',"edges":', toJSONString(arrayMap(j -> lower(hex(SHA256(concat('e', toString(u), '-', toString(j))))), range(%d))), '}'),
	toDateTime64('2026-09-20 00:00:00', 3, 'UTC') + toIntervalHour(v),
	'%s'
FROM (SELECT intDiv(number, 2) AS u, number %% 2 AS v FROM numbers(%d))`,
		strings.Join(quoted, ","), readers.BugfixSubcategoryKey, probeRepos, probeRepos, d.prs, d.commits, d.edges, d.org, 2*d.units)
	if err := direct.Exec(ctx, statement); err != nil {
		t.Fatalf("seed units: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, d.org, "run-1", at); err != nil {
		t.Fatal(err)
	}
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)
SELECT '%s', 'issue', concat('n', toString(number)), concat('wu-', toString(number)), 'theme', 'feature_delivery', toDateTime64('2026-09-20 00:00:00', 3, 'UTC'), 'run-1' FROM numbers(%d)`, d.org, d.units)); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

type probeRow struct {
	memory, readBytes, readRows uint64
	events                      map[string]uint64
}

func probeLogged(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, where string, args ...any) probeRow {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatal(err)
	}
	var r probeRow
	err := direct.QueryRow(ctx, `SELECT memory_usage, read_bytes, read_rows,
	mapFilter((k, v) -> match(k, 'Arena|Aggregat|Join|Sort|Window|SelectedRows|SelectedBytes|ReadCompressedBytes|Memory|External'), ProfileEvents)
FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND `+where+`
ORDER BY event_time_microseconds DESC LIMIT 1`, args...).Scan(&r.memory, &r.readBytes, &r.readRows, &r.events)
	if err != nil {
		t.Fatalf("query_log (measurement did not happen): %v\n%s", err, recentServerStatements(ctx, direct))
	}
	return r
}

func probeStage(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, org, tag, statement string, ids string) probeRow {
	t.Helper()
	for i := 0; i < 2; i++ {
		qctx := clickhouse.Context(ctx,
			clickhouse.WithParameters(clickhouse.Parameters{"org_id": org, "ids": ids}),
			clickhouse.WithSettings(clickhouse.Settings{"log_comment": tag}))
		rows, err := direct.Query(qctx, "SELECT count() FROM ("+statement+")")
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		rows.Close()
	}
	return probeLogged(t, ctx, direct, "log_comment = ?", tag)
}

const probeLatestPrefix = `SELECT work_unit_id,
	(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
	argMax(from_ts, computed_at) AS from_ts,
	argMax(to_ts, computed_at) AS to_ts,
	argMax(effort_value, computed_at) AS effort_value`

func TestProbeRepositoryMixMemorySlopeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	datasets := []probeDataset{
		{org: "org-w-2500", units: 2500, commits: 10, edges: 10, prs: 3},
		{org: "org-w-10000", units: 10000, commits: 10, edges: 10, prs: 3},
		{org: "org-w-15000", units: 15000, commits: 10, edges: 10, prs: 3},
		{org: "org-n-20000", units: 20000, prs: 3},
		{org: "org-r14-3000", units: 3000, prs: 14},
		{org: "org-r14-12000", units: 12000, prs: 14},
	}
	for _, d := range datasets {
		probeSeed(t, ctx, direct, d)
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	requested := make([]contextfabric.SubjectRef, 0, 5)
	quotedIDs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		label := fmt.Sprintf("repo-%d", i)
		requested = append(requested, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
		quotedIDs = append(quotedIDs, "'"+repoUUID(label)+"'")
	}
	ids := "[" + strings.Join(quotedIDs, ",") + "]"
	stages := []struct{ name, sql string }{
		{"latest_all7", probeLatestPrefix + `, argMax(theme_distribution_json, computed_at) AS t, argMax(subcategory_distribution_json, computed_at) AS s, argMax(structural_evidence_json, computed_at) AS e
FROM work_unit_investments WHERE org_id = {org_id:String} GROUP BY work_unit_id`},
		{"latest_no_evidence", probeLatestPrefix + `, argMax(theme_distribution_json, computed_at) AS t, argMax(subcategory_distribution_json, computed_at) AS s
FROM work_unit_investments WHERE org_id = {org_id:String} GROUP BY work_unit_id`},
		{"latest_refs_only", probeLatestPrefix + `, argMax(theme_distribution_json, computed_at) AS t, argMax(ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0), computed_at) AS b,
	argMax(tuple(JSONExtract(structural_evidence_json, 'prs', 'Array(String)'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')), computed_at) AS e
FROM work_unit_investments WHERE org_id = {org_id:String} GROUP BY work_unit_id`},
		{"parsed_arrayjoin", devhealthfacts.ProbeRepoSplitParsed()},
		{"split_core", devhealthfacts.ProbeRepoSplitCore()},
		{"full_no_scope", devhealthfacts.ProbeRepoMixStatement(100000)},
	}
	var report strings.Builder
	for _, d := range datasets {
		var avgEvidence float64
		if err := direct.QueryRow(ctx, `SELECT avg(length(structural_evidence_json)) FROM work_unit_investments WHERE org_id = ?`, d.org).Scan(&avgEvidence); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "\n== %s units=%d versions=2 avg_evidence_bytes=%.0f\n", d.org, d.units, avgEvidence)
		for _, s := range stages {
			r := probeStage(t, ctx, direct, d.org, d.org+"/"+s.name, s.sql, ids)
			line := fmt.Sprintf("%-20s mem=%7.2f MiB read=%7.2f MiB rows=%d", s.name, float64(r.memory)/(1<<20), float64(r.readBytes)/(1<<20), r.readRows)
			t.Logf("%s %s", d.org, line)
			report.WriteString(line + "\n")
		}
		var mark int64
		failed := false
		for i := 0; i < 2 && !failed; i++ {
			mark = serverNowMicros(t, ctx, direct)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: d.org}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: requested,
			})
			if err != nil {
				t.Logf("%s ReadFacts: %v: %s", d.org, err, readFailureCause(err))
				fmt.Fprintf(&report, "PROVIDER failed: %v\n", err)
				failed = true
				break
			}
			if len(result.Facts) == 0 {
				t.Fatalf("%s: no facts served", d.org)
			}
		}
		if failed {
			continue
		}
		var tableBytes uint64
		if err := direct.QueryRow(ctx, `SELECT sum(length(work_unit_id) + length(structural_evidence_json) + 8*4 + 16*length(mapKeys(theme_distribution_json)) + 16*length(mapKeys(subcategory_distribution_json))) FROM work_unit_investments WHERE org_id = ?`, d.org).Scan(&tableBytes); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "approx org rows bytes=%.2f MiB\n", float64(tableBytes)/(1<<20))
		r := probeLogged(t, ctx, direct, queryLogSinceMark+` AND position(query, 'v * effort), theme_distribution_json)) AS theme_effort') > 0`, mark)
		t.Logf("%s PROVIDER mem=%.2f MiB read=%.2f MiB", d.org, float64(r.memory)/(1<<20), float64(r.readBytes)/(1<<20))
		fmt.Fprintf(&report, "%-20s mem=%7.2f MiB read=%7.2f MiB rows=%d\n", "PROVIDER(ids scope)", float64(r.memory)/(1<<20), float64(r.readBytes)/(1<<20), r.readRows)
		keys := make([]string, 0, len(r.events))
		for k := range r.events {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&report, "    %s=%d\n", k, r.events[k])
		}
	}
	t.Errorf("probe report (intentional failure so the log prints):%s", report.String())
}
