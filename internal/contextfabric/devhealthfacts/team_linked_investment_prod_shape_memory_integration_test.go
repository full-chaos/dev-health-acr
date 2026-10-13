package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// linkedProdShape is the largest prod organization's volume for the tables
// the team linked pull request mix reads (read-only counts, 2026-10-13).
type linkedProdShape struct {
	repos, ownedRepos, teams      int
	projects, projectRows         int
	teamProjects                  int
	ownershipRows, ownershipPairs int
	ownershipOpen                 int
	workItems, linkedItems        int
	teamLinkedItems               int
	links, pullRequests           int
	// units carry a second version when u % 100 < secondVersionPct; the
	// membership run holds the units with u % 100 < scopePct. A unit names
	// prTailN pull requests by repository id when u % prTailEvery = 0,
	// prMidN when u % prMidEvery = 0, else one when u % 100 < prBasePct. It
	// names issueTailN issue references when u % issueTailEvery = 1,
	// issueMidN when u % issueMidEvery = 1, else one plus one more when
	// u % 100 < issueBasePct. Of a narrow unit's issue references all are
	// GitHub lookups when u % 100 < lookupPct; a wide unit's first quarter
	// are. padHashes commit hashes widen the evidence to prod's average size.
	units, secondVersionPct, scopePct int
	prTailEvery, prTailN              int
	prMidEvery, prMidN, prBasePct     int
	issueTailEvery, issueTailN        int
	issueMidEvery, issueMidN          int
	issueBasePct, lookupPct           int
	padHashes                         int
}

// Largest real prod organization (read-only counts, 2026-10-13):
// work_unit_investments 32,049 rows / 26,304 units, 12,643 units in the
// latest membership run; PR refs 44,465 (avg 1.39, max 205; 858 units with 10
// or more, 94 with 50 or more); issue refs 111,075 (avg 3.47, max 776; 1,413
// units with 10 or more, 90 with 100 or more), 29,289 of them GitHub lookups
// over 13,281 rows; evidence avg 641 B, max 172 KB. work_graph_issue_pr 24,024
// rows / 14,111 items / 7,134 PRs; git_pull_requests 7,816 rows / 7,782 PRs;
// work_items 17,238 rows / 17,035 items / 42 projects; repos 11;
// team_project_ownership 67,486 rows / 88 pairs / 36 teams. The read team
// owns 20 projects and 10 repositories and reaches about 6,000 linked items
// and 5,900 linked pull requests.
var linkedMemoryProdShape = linkedProdShape{
	repos: 11, ownedRepos: 10, teams: 39,
	projects: 42, projectRows: 47,
	teamProjects:  20,
	ownershipRows: 67486, ownershipPairs: 88, ownershipOpen: 60,
	workItems: 17035, linkedItems: 14111, teamLinkedItems: 6010,
	links: 24024, pullRequests: 7782,
	units: 26304, secondVersionPct: 22, scopePct: 48,
	prTailEvery: 280, prTailN: 100, prMidEvery: 34, prMidN: 20, prBasePct: 46,
	issueTailEvery: 290, issueTailN: 300, issueMidEvery: 19, issueMidN: 20,
	issueBasePct: 50, lookupPct: 41,
	padHashes: 9,
}

const (
	linkedShapeTeam = "T-0"
	linkedShapeRuns = 3
	// The API's statement settings in prod: max_memory_usage from
	// contextpacket.APIStatementMaxMemoryUsage and max_bytes_to_read from
	// config.DefaultClickHouseMaxBytesToRead.
	linkedShapeMaxBytesToRead = 256 << 20
	// The served statement must stay at or under this at the prod shape in
	// both merge states. The repository-scoped split reads it sits beside are
	// held under 64 MiB (repoScopedSplitMemoryCeiling).
	linkedShapeBoundMiB = 64.0
)

func seedLinkedProdShape(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, s linkedProdShape) []string {
	t.Helper()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%.400s", err, statement)
		}
	}
	at := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	quoted := make([]string, 0, s.repos)
	for i := 0; i < s.repos; i++ {
		label := fmt.Sprintf("shape-r-%d", i)
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", at)
		quoted = append(quoted, "'"+repoUUID(label)+"'")
		if i < s.ownedRepos {
			exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				orgID, "github", linkedShapeTeam, repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), at.AddDate(0, -9, 0), nil, at)
		}
	}
	repoArr := "[" + strings.Join(quoted, ",") + "]"
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active)
SELECT concat('T-', toString(number)), concat('T-', toString(number)), NULL, ?, ?, 'linear', [], 1 FROM numbers(?)`, at, orgID, uint64(s.teams))
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at)
SELECT concat('P-', toString(number % ?)), ?, 'linear', concat('K', toString(number % ?)), 'project', 1, 'started', '', ? + toIntervalSecond(number) FROM numbers(?)`,
		uint64(s.projects), orgID, uint64(s.projects), at, uint64(s.projectRows))
	// Pair p < teamProjects is (T-0, P-p); the rest belong to the other teams
	// over the remaining projects. Each pair carries its share of the history;
	// the last version of the first ownershipOpen pairs stays open.
	versions := s.ownershipRows / s.ownershipPairs
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear',
	if(p < ?, 'T-0', concat('T-', toString(1 + (p - ?) % (? - 1)))),
	if(p < ?, concat('P-', toString(p)), concat('P-', toString(? + intDiv(p - ?, ? - 1) % (? - ?)))),
	NULL, 'native',
	? + toIntervalHour(v),
	if(p < ? AND v = ? - 1, NULL, ? + toIntervalHour(v) + toIntervalMinute(30)),
	?
FROM (SELECT number % ? AS p, intDiv(number, ?) AS v FROM numbers(?))`,
		orgID,
		uint64(s.teamProjects), uint64(s.teamProjects), uint64(s.teams),
		uint64(s.teamProjects), uint64(s.teamProjects), uint64(s.teamProjects), uint64(s.teams), uint64(s.projects), uint64(s.teamProjects),
		at.AddDate(-2, 0, 0),
		uint64(s.ownershipOpen), uint64(versions), at.AddDate(-2, 0, 0),
		at,
		uint64(s.ownershipPairs), uint64(s.ownershipPairs), uint64(versions*s.ownershipPairs))
	// Items below teamLinkedItems sit in T-0's projects; the other linked items
	// and the unlinked rest sit in the other teams' projects.
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, type)
SELECT concat('wi-', toString(number)), toUUID('00000000-0000-0000-0000-000000000000'), ?, 'title', 'open', '', ?, '', 'linear',
	if(number < ?, concat('P-', toString(number % ?)), concat('P-', toString(? + number % (? - ?)))), NULL, 'issue'
FROM numbers(?)`, orgID, at, uint64(s.teamLinkedItems), uint64(s.teamProjects), uint64(s.teamProjects), uint64(s.projects), uint64(s.teamProjects), uint64(s.workItems))
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
WITH `+repoArr+` AS repos_arr
SELECT repos_arr[1 + number % ?], ?, toUInt32(1 + intDiv(number, ?)), 'pr', 'merged', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?)`,
		uint64(s.ownedRepos), orgID, uint64(s.ownedRepos), at, at, uint64(s.pullRequests))
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
WITH `+repoArr+` AS repos_arr
SELECT repos_arr[1 + pr % ?], concat('wi-', toString(k % ?)), toUInt32(1 + intDiv(pr, ?)), 1, if(k % 3 = 0, 'explicit_text', 'native'), '', ?, ?
FROM (SELECT number AS k, (number * 7) % ? AS pr FROM numbers(?))`,
		uint64(s.ownedRepos), uint64(s.linkedItems), uint64(s.ownedRepos), at, orgID, uint64(s.pullRequests), uint64(s.links))
	// One insert per version: a single insert block would collapse the two
	// versions of a unit (optimize_on_insert).
	for version := uint64(0); version < 2; version++ {
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
	WITH `+repoArr+` AS repos_arr, ['feature_delivery','operational','maintenance','quality','risk'] AS themes
	SELECT concat('wu-', toString(u)), ? - toIntervalDay(u % 300), ?, NULL, toFloat64(1 + u % 9),
		map(themes[1 + u % 5], 0.6, themes[1 + (u + 2) % 5], 0.4),
		map('`+readers.BugfixSubcategoryKey+`', 0.25, 'refactor', 0.15),
		concat('{"issues":', toJSONString(arrayMap(j -> if(j < nl,
				concat('ghpr:acme/shape-r-', toString((u + j) % ?), '#', toString(1 + intDiv((u * 3 + j) % ?, ?))),
				concat('linear:CH-', toString(u * 1000 + j))), range(ni))),
			',"prs":', toJSONString(arrayMap(j -> concat(repos_arr[1 + ((u * 5 + j) % ?) % ?], '#pr', toString(1 + intDiv((u * 5 + j) % ?, ?))), range(np))),
			',"commits":', toJSONString(arrayMap(j -> lower(hex(SHA1(concat(toString(u), '-', toString(j))))), range(?))), '}'),
		? + toIntervalHour(v), ?
	FROM (
		SELECT u, v, np, ni, if(ni > 2, intDiv(ni, 4), if(u % 100 < ?, ni, 0)) AS nl
		FROM (
			SELECT u, v,
				multiIf(u % ? = 0, ?, u % ? = 0, ?, toUInt64(u % 100 < ?)) AS np,
				multiIf(u % ? = 1, ?, u % ? = 1, ?, 1 + toUInt64(u % 100 < ?)) AS ni
			FROM (SELECT intDiv(number, 2) AS u, number % 2 AS v FROM numbers(?))
			WHERE v = ? AND (v = 0 OR u % 100 < ?)
		)
	)`,
			at, at,
			uint64(s.ownedRepos), uint64(s.pullRequests), uint64(s.ownedRepos),
			uint64(s.pullRequests), uint64(s.ownedRepos), uint64(s.pullRequests), uint64(s.ownedRepos),
			uint64(s.padHashes),
			at, orgID,
			uint64(s.lookupPct),
			uint64(s.prTailEvery), uint64(s.prTailN), uint64(s.prMidEvery), uint64(s.prMidN), uint64(s.prBasePct),
			uint64(s.issueTailEvery), uint64(s.issueTailN), uint64(s.issueMidEvery), uint64(s.issueMidN), uint64(s.issueBasePct),
			uint64(2*s.units), version, uint64(s.secondVersionPct))
	}
	exec(`INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?, 'run-1', ?)`, orgID, at.Add(time.Hour))
	exec(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)
SELECT ?, 'issue', concat('n', toString(number)), concat('wu-', toString(number)), 'theme', 'feature_delivery', ?, 'run-1' FROM numbers(?) WHERE number % 100 < ?`,
		orgID, at.Add(time.Hour), uint64(s.units), uint64(s.scopePct))
	var ids []string
	for i := 0; i < s.units; i++ {
		if i%100 < s.scopePct {
			ids = append(ids, fmt.Sprintf("wu-%d", i))
		}
	}
	return ids
}

type linkedFragmentPeak struct {
	name     string
	mib      float64
	readRows uint64
	misses   uint64
}

func measureLinkedMixFragments(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID, stage string, fragments []devhealthfacts.LinkedMixFragment) []linkedFragmentPeak {
	t.Helper()
	var out []linkedFragmentPeak
	for _, frag := range fragments {
		best := linkedFragmentPeak{name: frag.Name, mib: -1}
		for run := 0; run < linkedShapeRuns; run++ {
			tag := fmt.Sprintf("linked-%s-%s-%d-%d", stage, frag.Name, run, time.Now().UnixNano())
			settings := "max_result_rows = 0, max_memory_usage = 0, log_comment = '" + tag + "'"
			if frag.Settings != "" {
				settings += ", " + frag.Settings
			}
			statement := frag.SQL + "\nSETTINGS " + settings
			if err := readers.QueryOrgScopedNamed(ctx, query, "LinkedMixFragment", statement, orgID, []string{linkedShapeTeam},
				func(readers.RowScanner) error { return nil }, frag.Bindings...); err != nil {
				t.Fatalf("%s %s: %v: %s", stage, frag.Name, err, readFailureCause(err))
			}
			if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
				t.Fatalf("flush logs (measurement did not happen): %v", err)
			}
			var mem, readRows, misses uint64
			if err := direct.QueryRow(ctx, `SELECT memory_usage, read_rows, ProfileEvents['ScalarSubqueriesCacheMiss'] FROM system.query_log
WHERE type = 'QueryFinish' AND log_comment = ? ORDER BY event_time_microseconds DESC LIMIT 1`, tag).Scan(&mem, &readRows, &misses); err != nil {
				t.Fatalf("%s %s: no query_log row for %s (measurement did not happen): %v", stage, frag.Name, tag, err)
			}
			mib := float64(mem) / (1 << 20)
			if best.mib < 0 || mib < best.mib {
				best = linkedFragmentPeak{name: frag.Name, mib: mib, readRows: readRows, misses: misses}
			}
		}
		out = append(out, best)
	}
	return out
}

func linkedFragmentTable(stage string, peaks []linkedFragmentPeak) string {
	var b strings.Builder
	fmt.Fprintf(&b, "team linked pull request mix, stage=%s (min of %d warm runs; diagnostic settings max_memory_usage=0, max_result_rows=0)\n", stage, linkedShapeRuns)
	for _, p := range peaks {
		fmt.Fprintf(&b, "  %-20s peak=%8.2f MiB read_rows=%10d scalar_misses=%d\n", p.name, p.mib, p.readRows, p.misses)
	}
	return b.String()
}

type linkedMixRow struct {
	repos  uint64
	theme  map[string]float64
	bugfix float64
}

func readLinkedMixRows(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, teams []string, frag devhealthfacts.LinkedMixFragment) map[string]linkedMixRow {
	t.Helper()
	out := map[string]linkedMixRow{}
	statement := frag.SQL + "\nSETTINGS max_result_rows = 0, max_memory_usage = 0"
	if err := readers.QueryOrgScopedNamed(ctx, query, "LinkedMixDifferential", statement, orgID, teams, func(row readers.RowScanner) error {
		var team string
		var r linkedMixRow
		if err := row.Scan(&team, &r.repos, &r.theme, &r.bugfix); err != nil {
			return err
		}
		out[team] = r
		return nil
	}, frag.Bindings...); err != nil {
		t.Fatalf("%s: %v: %s", frag.Name, err, readFailureCause(err))
	}
	return out
}

func closeEnough(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	scale := a
	if scale < 0 {
		scale = -scale
	}
	return d <= 1e-9*scale+1e-12
}

func fragmentNamed(t *testing.T, fragments []devhealthfacts.LinkedMixFragment, name string) devhealthfacts.LinkedMixFragment {
	t.Helper()
	for _, f := range fragments {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no fragment named %s", name)
	return devhealthfacts.LinkedMixFragment{}
}

// servedLinkedMixPeak drives the team investment read through the provider on
// a client with the API's statement settings and returns the linked mix
// statement's lowest peak over warm runs.
func servedLinkedMixPeak(t *testing.T, ctx context.Context, provider contextfabric.FactProvider, direct clickhousedriver.Conn, orgID string) float64 {
	t.Helper()
	best := -1.0
	for run := 0; run < linkedShapeRuns+1; run++ {
		mark := serverNowMicros(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{teamSubject(linkedShapeTeam)},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
		}
		factFor(t, result, teamSubject(linkedShapeTeam))
		peaks := statementMemorySince(t, ctx, direct, statementFilter{Table: "work_unit_investments", QueryContains: "uniqExact(m.repo_uuid)"}, mark)
		if len(peaks) != 1 {
			t.Fatalf("measurement did not happen: %d linked mix statements logged on a served read, want 1", len(peaks))
		}
		if run == 0 {
			continue
		}
		for _, peak := range peaks {
			if mib := float64(peak) / (1 << 20); best < 0 || mib < best {
				best = mib
			}
		}
	}
	return best
}

// The team linked pull request mix at the largest prod organization's
// volume, measured whole and fragment by fragment, with the parts unmerged and
// after OPTIMIZE FINAL. Prod peaked 233.98 MiB of its 256 MiB cap for one
// team read; CI reads about 0.80-0.86 of prod.
func TestTeamLinkedInvestmentStatementMemoryOnTheProdShape(t *testing.T) {
	ctx := context.Background()
	maxBytes := uint64(linkedShapeMaxBytesToRead)
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(o *runtimeclickhouse.Options) { o.MaxBytesToRead = &maxBytes })
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-linked-prod-shape"
	tables := []string{"work_unit_investments", "work_items", "work_graph_issue_pr", "git_pull_requests", "team_project_ownership", "projects", "repos", "team_repo_ownership"}
	t.Cleanup(func() {
		for _, table := range tables {
			// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
			_ = direct.Exec(context.Background(), "SYSTEM START MERGES "+table)
		}
	})
	for _, table := range tables {
		// devhealthschema:not-a-production-replica this statement maintains a table the production DDL created; no column, type or engine is declared here.
		if err := direct.Exec(ctx, "SYSTEM STOP MERGES "+table); err != nil {
			t.Fatal(err)
		}
	}
	ids := seedLinkedProdShape(t, ctx, direct, orgID, linkedMemoryProdShape)
	var rows, units, prRefs, issueRefs, lookupRefs, maxPRs, maxIssues uint64
	var avgJSON float64
	if err := direct.QueryRow(ctx, `SELECT count(), uniqExact(work_unit_id), sum(np), sum(ni), sum(nl), max(np), max(ni), avg(jl) FROM (
	SELECT length(JSONExtract(structural_evidence_json, 'prs', 'Array(String)')) AS np,
		length(JSONExtract(structural_evidence_json, 'issues', 'Array(String)')) AS ni,
		length(arrayFilter(r -> startsWith(r, 'ghpr:'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))) AS nl,
		length(structural_evidence_json) AS jl, work_unit_id
	FROM work_unit_investments WHERE org_id = ?)`, orgID).Scan(&rows, &units, &prRefs, &issueRefs, &lookupRefs, &maxPRs, &maxIssues, &avgJSON); err != nil {
		t.Fatalf("read the seeded shape: %v", err)
	}
	t.Logf("seeded: %d unit rows / %d units (%d in scope), PR refs %d (max %d), issue refs %d (max %d), lookups %d, evidence avg %.0f B",
		rows, units, len(ids), prRefs, maxPRs, issueRefs, maxIssues, lookupRefs, avgJSON)
	fragments, missing := devhealthfacts.TeamLinkedMixFragmentsForTest(ids)
	if missing != "" {
		t.Fatalf("fragment %s: cut marker not in the rendered statement", missing)
	}
	capped := contextpacket.NewMemoryCappedQueryClient(query, contextpacket.APIStatementMaxMemoryUsage)
	provider := findProvider(t, devhealthfacts.NewProviders(capped), contextfabric.FactInvestment)

	explain := fragmentNamed(t, fragments, "statement")
	var plan []string
	if err := readers.QueryOrgScopedNamed(ctx, query, "LinkedMixExplain", "EXPLAIN PLAN "+explain.SQL+"\nSETTINGS max_result_rows = 0", orgID, []string{linkedShapeTeam}, func(row readers.RowScanner) error {
		var line string
		if err := row.Scan(&line); err != nil {
			return err
		}
		if len(plan) < 70 {
			plan = append(plan, line)
		}
		return nil
	}, explain.Bindings...); err != nil {
		t.Fatalf("explain: %v: %s", err, readFailureCause(err))
	}
	t.Logf("plan head of the statement:\n%s", strings.Join(plan, "\n"))
	unmerged := measureLinkedMixFragments(t, ctx, query, direct, orgID, "unmerged", fragments)
	servedUnmerged := servedLinkedMixPeak(t, ctx, provider, direct, orgID)
	for _, table := range tables {
		// devhealthschema:not-a-production-replica these statements maintain tables the production DDL created; no column, type or engine is declared here.
		if err := direct.Exec(ctx, "SYSTEM START MERGES "+table); err != nil {
			t.Fatal(err)
		}
		// devhealthschema:not-a-production-replica these statements maintain tables the production DDL created; no column, type or engine is declared here.
		if err := direct.Exec(ctx, "OPTIMIZE TABLE "+table+" FINAL"); err != nil {
			t.Fatal(err)
		}
	}
	optimized := measureLinkedMixFragments(t, ctx, query, direct, orgID, "optimized", fragments)
	servedOptimized := servedLinkedMixPeak(t, ctx, provider, direct, orgID)
	t.Logf("%s%sserved (API settings, 256 MiB cap): unmerged %.2f MiB, optimized %.2f MiB", linkedFragmentTable("unmerged", unmerged), linkedFragmentTable("optimized", optimized), servedUnmerged, servedOptimized)
	teams := make([]string, linkedMemoryProdShape.teams)
	for i := range teams {
		teams[i] = fmt.Sprintf("T-%d", i)
	}
	current := readLinkedMixRows(t, ctx, query, orgID, teams, fragmentNamed(t, fragments, "statement"))
	candidate := readLinkedMixRows(t, ctx, query, orgID, teams, fragmentNamed(t, fragments, "candidate_per_pr_first"))
	t.Logf("differential over %d teams: current statement served %d team rows, candidate %d", len(teams), len(current), len(candidate))
	if len(current) == 0 {
		t.Errorf("differential did not happen: the current statement returned no team row")
	}
	if len(candidate) != len(current) {
		t.Errorf("candidate returned %d team rows, the current statement %d", len(candidate), len(current))
	}
	for team, want := range current {
		got, ok := candidate[team]
		if !ok || got.repos != want.repos || !closeEnough(got.bugfix, want.bugfix) || len(got.theme) != len(want.theme) {
			t.Errorf("team %s: candidate %+v, current %+v", team, got, want)
			continue
		}
		for theme, v := range want.theme {
			if !closeEnough(got.theme[theme], v) {
				t.Errorf("team %s theme %s: candidate %v, current %v", team, theme, got.theme[theme], v)
			}
		}
	}
	for stage, peak := range map[string]float64{"unmerged": servedUnmerged, "optimized": servedOptimized} {
		if peak > linkedShapeBoundMiB {
			t.Errorf("%s: the served team linked pull request mix peaked at %.2f MiB at the prod shape, bound %.0f MiB", stage, peak, linkedShapeBoundMiB)
		}
	}
}
