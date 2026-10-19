package devhealthfacts_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
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
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
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
	// Every unit with u % extremeEvery = 3 is as wide as the widest in prod:
	// extremePRs pull requests and extremeIssues issue references.
	extremeEvery, extremePRs, extremeIssues int
	padHashes                               int
	// fallbackEvery makes every link with k % fallbackEvery = 0 an
	// explicit_text link next to the native ones (0: every link native).
	fallbackEvery int
	// wideUnitIDs gives every unit id prod's width (about 73 characters, a
	// hash suffix) instead of the short wu-<n>.
	wideUnitIDs bool
}

// unitIDSQL is the unit id of the number in col, as ClickHouse spells it.
func (s linkedProdShape) unitIDSQL(col string) string {
	if s.wideUnitIDs {
		return "concat('wu-', toString(" + col + "), '-', lower(hex(SHA256(toString(" + col + ")))))"
	}
	return "concat('wu-', toString(" + col + "))"
}

// unitID is the unit id of i, equal to unitIDSQL's.
// linkTierSQL is the provenance of link number k.
func (s linkedProdShape) linkTierSQL() string {
	if s.fallbackEvery > 0 {
		return fmt.Sprintf("if(k %% %d = 0, 'explicit_text', 'native')", s.fallbackEvery)
	}
	return "'native'"
}

func (s linkedProdShape) unitID(i int) string {
	if s.wideUnitIDs {
		return fmt.Sprintf("wu-%d-%x", i, sha256.Sum256([]byte(strconv.Itoa(i))))
	}
	return fmt.Sprintf("wu-%d", i)
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
	extremeEvery: 6000, extremePRs: 205, extremeIssues: 776,
	padHashes: 9,
}

const (
	linkedShapeTeam = "T-0"
	linkedShapeRuns = 3
	// The API's statement settings in prod: max_memory_usage from
	// contextpacket.APIStatementMaxMemoryUsage and max_bytes_to_read from
	// config.DefaultClickHouseMaxBytesToRead.
	linkedShapeMaxBytesToRead = 256 << 20
	// The served statement stays at or under linkedShapePeakBoundMiB (3/8 of
	// the 256 MiB statement cap) and at or under linkedShapeSplitFactor times
	// the peak of its own split rows plus linkedShapeSplitSlackMiB.
	linkedShapePeakBoundMiB  = 96.0
	linkedShapeSplitFactor   = 1.25
	linkedShapeSplitSlackMiB = 8.0
	// The unit that names one pull request three times. Its weight is one,
	// and it carries a theme no other unit has, so a team's value of that
	// theme shows whether the repeated pull request was counted once (0.5) or
	// per mention. Its stored effort is 0: an unsized unit is kept.
	linkedShapeRepeatedUnit  = "wu-repeated-key"
	linkedShapeRepeatedTheme = "repeated_probe"
	linkedShapeRepeatedTeam  = "T-1"
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
SELECT repos_arr[1 + pr % ?], concat('wi-', toString(k % ?)), toUInt32(1 + intDiv(pr, ?)), 1, `+s.linkTierSQL()+`, '', ?, ?
FROM (SELECT number AS k, (number * 7) % ? AS pr FROM numbers(?))`,
		uint64(s.ownedRepos), uint64(s.linkedItems), uint64(s.ownedRepos), at, orgID, uint64(s.pullRequests), uint64(s.links))
	// One insert per version: a single insert block would collapse the two
	// versions of a unit (optimize_on_insert).
	for version := uint64(0); version < 2; version++ {
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
	WITH `+repoArr+` AS repos_arr, ['feature_delivery','operational','maintenance','quality','risk'] AS themes
	SELECT `+s.unitIDSQL("u")+`, ? - toIntervalDay(u % 300), ?, NULL, toFloat64(1 + u % 9),
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
				multiIf(u % ? = 3, ?, u % ? = 0, ?, u % ? = 0, ?, toUInt64(u % 100 < ?)) AS np,
				multiIf(u % ? = 3, ?, u % ? = 1, ?, u % ? = 1, ?, 1 + toUInt64(u % 100 < ?)) AS ni
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
			uint64(s.extremeEvery), uint64(s.extremePRs), uint64(s.prTailEvery), uint64(s.prTailN), uint64(s.prMidEvery), uint64(s.prMidN), uint64(s.prBasePct),
			uint64(s.extremeEvery), uint64(s.extremeIssues), uint64(s.issueTailEvery), uint64(s.issueTailN), uint64(s.issueMidEvery), uint64(s.issueMidN), uint64(s.issueBasePct),
			uint64(2*s.units), version, uint64(s.secondVersionPct))
	}
	exec(`INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?, 'run-1', ?)`, orgID, at.Add(time.Hour))
	exec(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)
SELECT ?, 'issue', concat('n', toString(number)), `+s.unitIDSQL("number")+`, 'theme', 'feature_delivery', ?, 'run-1' FROM numbers(?) WHERE number % 100 < ?`,
		orgID, at.Add(time.Hour), uint64(s.units), uint64(s.scopePct))
	// One unit names the same pull request three times (by repository id and
	// twice through the same GitHub lookup) and one other pull request, in a
	// repository with no link. Its distinct references are 2, so half of its
	// effort goes to the first pull request, which is linked to an issue of a
	// project the other teams own. Counting the repeats would give 3 of 4.
	item := s.teamLinkedItems
	for item%(s.projects-s.teamProjects) != 0 {
		item++
	}
	pr := (item * 7) % s.pullRequests
	repoLabel := fmt.Sprintf("shape-r-%d", pr%s.ownedRepos)
	number := 1 + pr/s.ownedRepos
	exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,NULL,?,?,?,?,?,?)`,
		linkedShapeRepeatedUnit, at, at, 0.0, map[string]float64{linkedShapeRepeatedTheme: 1.0}, map[string]float64{},
		fmt.Sprintf(`{"issues":["ghpr:acme/%s#%d","ghpr:acme/%s#%d"],"prs":["%s#pr%d","%s#pr1"]}`, repoLabel, number, repoLabel, number, repoUUID(repoLabel), number, repoUUID(fmt.Sprintf("shape-r-%d", s.repos-1))),
		at, orgID)
	exec(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?, 'issue', 'n-repeated', ?, 'theme', 'feature_delivery', ?, 'run-1')`,
		orgID, linkedShapeRepeatedUnit, at.Add(time.Hour))
	ids := []string{linkedShapeRepeatedUnit}
	for i := 0; i < s.units; i++ {
		if i%100 < s.scopePct {
			ids = append(ids, s.unitID(i))
		}
	}
	return ids
}

type linkedFragmentPeak struct {
	name     string
	mib      float64
	readRows uint64
}

// measureLinkedMixFragments runs each form linkedShapeRuns times with no
// memory cap and no result row cap and keeps its lowest peak: the forms are
// the floor (split rows) and the negative control, never the bounded read.
func measureLinkedMixFragments(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID, stage string, fragments []devhealthfacts.LinkedMixFragment) map[string]linkedFragmentPeak {
	t.Helper()
	out := map[string]linkedFragmentPeak{}
	for _, frag := range fragments {
		best := linkedFragmentPeak{name: frag.Name, mib: -1}
		for run := 0; run < linkedShapeRuns; run++ {
			tag := fmt.Sprintf("linked-%s-%s-%d-%d", stage, frag.Name, run, time.Now().UnixNano())
			statement := frag.SQL + "\nSETTINGS max_result_rows = 0, max_memory_usage = 0, log_comment = '" + tag + "'"
			if err := readers.QueryOrgScopedNamed(ctx, query, "LinkedMixFragment", statement, orgID, []string{linkedShapeTeam},
				func(readers.RowScanner) error { return nil }, frag.Bindings...); err != nil {
				t.Fatalf("%s %s: %v: %s", stage, frag.Name, err, readFailureCause(err))
			}
			if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
				t.Fatalf("flush logs (measurement did not happen): %v", err)
			}
			var mem, readRows, threadless uint64
			if err := direct.QueryRow(ctx, `SELECT memory_usage, read_rows, `+statementthreads.Threadless+` FROM system.query_log
WHERE type = 'QueryFinish' AND log_comment = ? ORDER BY event_time_microseconds DESC LIMIT 1`, tag).Scan(&mem, &readRows, &threadless); err != nil {
				t.Fatalf("%s %s: no query_log row for %s (measurement did not happen): %v", stage, frag.Name, tag, err)
			}
			statementthreads.RequireThreadBound(t, stage+" "+frag.Name, threadless)
			if mib := float64(mem) / (1 << 20); best.mib < 0 || mib < best.mib {
				best = linkedFragmentPeak{name: frag.Name, mib: mib, readRows: readRows}
			}
		}
		out[frag.Name] = best
	}
	return out
}

func linkedFragmentTable(stage string, fragments []devhealthfacts.LinkedMixFragment, peaks map[string]linkedFragmentPeak) string {
	var b strings.Builder
	fmt.Fprintf(&b, "team linked pull request mix, stage=%s (min of %d warm runs; max_memory_usage=0, max_result_rows=0)\n", stage, linkedShapeRuns)
	for _, frag := range fragments {
		p := peaks[frag.Name]
		fmt.Fprintf(&b, "  %-32s peak=%8.2f MiB read_rows=%10d\n", p.name, p.mib, p.readRows)
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
		var unitHashes, sizedHashes []uint64
		dest := []any{&team, &r.repos, &r.theme, &r.bugfix}
		if strings.Contains(frag.SQL, "unit_hashes") {
			dest = append(dest, &unitHashes, &sizedHashes)
		}
		if err := row.Scan(dest...); err != nil {
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

// repeatedKeyShare is true when a team row carries half of the repeated-key
// unit's weight: the repeated pull request counted once.
func repeatedKeyShare(rows map[string]linkedMixRow) (float64, bool) {
	share := rows[linkedShapeRepeatedTeam].theme[linkedShapeRepeatedTheme]
	return share, closeEnough(share, 0.5)
}

// assertLinkedMixDifferential compares the production statement with the
// statement before the folded split and with the form that reads the
// reference array above the expansion, for every team of the seed, and pins
// the repeated-key unit's share in all three.
func assertLinkedMixDifferential(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID, stage string, fragments []devhealthfacts.LinkedMixFragment) {
	t.Helper()
	teams := make([]string, linkedMemoryProdShape.teams)
	for i := range teams {
		teams[i] = fmt.Sprintf("T-%d", i)
	}
	served := readLinkedMixRows(t, ctx, query, orgID, teams, fragmentNamed(t, fragments, "statement"))
	if len(served) < 2 {
		t.Fatalf("%s: differential did not happen: the production statement returned %d team rows", stage, len(served))
	}
	if share, ok := repeatedKeyShare(served); !ok {
		t.Errorf("%s: production statement: team %s %s effort %v, want half of the repeated-key unit (0.5)", stage, linkedShapeRepeatedTeam, linkedShapeRepeatedTheme, share)
	}
	for _, name := range []string{"legacy_statement", "reference_array_above_expansion"} {
		other := readLinkedMixRows(t, ctx, query, orgID, teams, fragmentNamed(t, fragments, name))
		t.Logf("%s: differential over %d teams: production statement %d team rows, %s %d", stage, len(teams), len(served), name, len(other))
		if len(other) != len(served) {
			t.Errorf("%s: %s returned %d team rows, the production statement %d", stage, name, len(other), len(served))
		}
		for team, got := range served {
			want, ok := other[team]
			if !ok || got.repos != want.repos || !closeEnough(got.bugfix, want.bugfix) || len(got.theme) != len(want.theme) {
				t.Errorf("%s: team %s: production %+v, %s %+v", stage, team, got, name, want)
				continue
			}
			for theme, v := range want.theme {
				if !closeEnough(got.theme[theme], v) {
					t.Errorf("%s: team %s theme %s: production %v, %s %v", stage, team, theme, got.theme[theme], name, v)
				}
			}
		}
		if share, ok := repeatedKeyShare(other); !ok {
			t.Errorf("%s: %s: team %s %s effort %v, want half of the repeated-key unit", stage, name, linkedShapeRepeatedTeam, linkedShapeRepeatedTheme, share)
		}
	}
	// The repeated-key row can tell a repeat counted from a repeat ignored.
	counted := readLinkedMixRows(t, ctx, query, orgID, teams, fragmentNamed(t, fragments, "repeats_counted"))
	if share, ok := repeatedKeyShare(counted); ok {
		t.Errorf("%s: the repeated-key row cannot fail: with c forced to 2 team %s still reads %v", stage, linkedShapeRepeatedTeam, share)
	}
}

// servedLinkedMixPeak drives the team investment read through the provider on
// a client with the API's statement settings and returns the linked mix
// statement's lowest and highest peak over warm runs.
func servedLinkedMixPeak(t *testing.T, ctx context.Context, provider contextfabric.FactProvider, direct clickhousedriver.Conn, orgID string) (lowest, highest float64) {
	t.Helper()
	lowest = -1.0
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
			mib := float64(peak) / (1 << 20)
			if lowest < 0 || mib < lowest {
				lowest = mib
			}
			if mib > highest {
				highest = mib
			}
		}
	}
	return lowest, highest
}

// The team linked pull request mix at the largest prod organization's
// volume and skew, with the parts unmerged and after OPTIMIZE FINAL. Prod
// peaked 233.98 MiB of its 256 MiB statement cap for one team read while the
// statement read each unit's reference array on every expanded
// (unit, pull request) row; this seed reads 232-239 MiB in that form.
//
// Two bounds hold the served statement. The peak bound keeps it far from the
// cap. The mechanism bound ties it to the split rows it is built on, so a
// unit array carried through the expansion again fails whatever the size of
// the organization. The form that reads the array above the expansion is
// measured beside it and must break both bounds, or the seed has lost the
// skew that makes the row able to fail.
//
// repoScopedSplitMemoryCeiling (64 MiB) is not applied here: it bounds the
// repository-scoped split reads on a shape without wide units, and this
// statement's floor at the prod skew is its own split (60-72 MiB).
// newLinkedProdShapeStore starts ClickHouse with the API's read settings,
// stops merges on the tables the statement reads and seeds shape for orgID.
func newLinkedProdShapeStore(t *testing.T, ctx context.Context, orgID string, shape linkedProdShape) (*runtimeclickhouse.Client, clickhousedriver.Conn, []string, []string) {
	t.Helper()
	maxBytes := uint64(linkedShapeMaxBytesToRead)
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(o *runtimeclickhouse.Options) { o.MaxBytesToRead = &maxBytes })
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	tables := []string{"work_unit_investments", "work_unit_issue_size", "work_items", "work_graph_issue_pr", "git_pull_requests", "team_project_ownership", "projects", "repos", "team_repo_ownership"}
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
	return query, direct, tables, seedLinkedProdShape(t, ctx, direct, orgID, shape)
}

func TestTeamLinkedInvestmentStatementMemoryOnTheProdShape(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-linked-prod-shape"
	query, direct, tables, ids := newLinkedProdShapeStore(t, ctx, orgID, linkedMemoryProdShape)
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
	if maxPRs < uint64(linkedMemoryProdShape.extremePRs) || maxIssues < uint64(linkedMemoryProdShape.extremeIssues) {
		t.Fatalf("the seed lost its widest units: max %d PR refs and %d issue refs", maxPRs, maxIssues)
	}
	fragments, missing := devhealthfacts.TeamLinkedMixFragmentsForTest(ids)
	if missing != "" {
		t.Fatalf("form %s: marker not in the rendered statement", missing)
	}
	capped := statementthreads.API(query)
	provider := findProvider(t, devhealthfacts.NewProviders(capped), contextfabric.FactInvestment)

	check := func(stage string) {
		t.Helper()
		peaks := measureLinkedMixFragments(t, ctx, capped, direct, orgID, stage, fragments)
		// Every warm served read must hold the bounds, so the highest one is
		// compared; the split rows and the form that must break the bounds are
		// taken at their lowest, the side that makes each check harder.
		lowest, served := servedLinkedMixPeak(t, ctx, provider, direct, orgID)
		mechanism := linkedShapeSplitFactor*peaks["split_rows"].mib + linkedShapeSplitSlackMiB
		t.Logf("%sserved through the provider (API settings, %d MiB cap), stage=%s: highest %.2f MiB, lowest %.2f MiB over %d warm reads; bounds: peak %.0f MiB, mechanism %.2f MiB (%.2f x split rows %.2f MiB + %.0f MiB)",
			linkedFragmentTable(stage, fragments, peaks), contextpacket.APIStatementMaxMemoryUsage>>20, stage, served, lowest, linkedShapeRuns,
			linkedShapePeakBoundMiB, mechanism, linkedShapeSplitFactor, peaks["split_rows"].mib, linkedShapeSplitSlackMiB)
		if served > linkedShapePeakBoundMiB {
			t.Errorf("%s: the served team linked pull request mix peaked at %.2f MiB at the prod shape, bound %.0f MiB", stage, served, linkedShapePeakBoundMiB)
		}
		if served > mechanism {
			t.Errorf("%s: the served team linked pull request mix peaked at %.2f MiB, over %.2f MiB (%.2f x its split rows + %.0f MiB): it carries more than its split", stage, served, mechanism, linkedShapeSplitFactor, linkedShapeSplitSlackMiB)
		}
		if above := peaks["reference_array_above_expansion"].mib; above <= linkedShapePeakBoundMiB || above <= mechanism {
			t.Errorf("%s: the row cannot fail: the form that reads the reference array above the expansion peaked at %.2f MiB, inside the bounds (%.0f and %.2f MiB)", stage, above, linkedShapePeakBoundMiB, mechanism)
		}
		assertLinkedMixDifferential(t, ctx, query, orgID, stage, fragments)
	}
	check("unmerged")
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
	check("optimized")
}
