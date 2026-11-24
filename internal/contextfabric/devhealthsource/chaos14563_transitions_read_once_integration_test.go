package devhealthsource_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// CHAOS-14563: the BELONGS_TO_PROJECT edge count read project_membership_transitions
// with FINAL 6 times in its transition arm (two touches branches, classified run
// again at each of its 3 references). A small part inside ONE granule of the big
// part puts each FINAL read on the layered path, which reads one more granule.
// The layouts are the ones of the CHAOS-14548 diagnostic cell (acr #2198).

const (
	c14563Org     = "f0f0f0f0-0000-4000-8000-00000000000b"
	c14563Repo    = "toUUID('14563000-0000-4000-8000-000000000001')"
	c14563Base    = "toDateTime64('2026-09-01 00:00:00', 3, 'UTC')"
	c14563MainSHA = "e9a488c626c2" // membershipEdgeCountSQL(true) at acr main e67c4128
	c14563NewSHA  = "f0b969e82275"
	c14563Shipped = "\nSETTINGS max_threads = 1"
	// c14563Subjects x 3 touches = 13,974 rows: two granules (8,192 + 5,782).
	c14563Subjects = 4658
)

func c14563Project(n string) string {
	return fmt.Sprintf("concat('14563200-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", n)
}

const c14563Columns = "INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) "

// c14563Big is one org's history: every subject added, moved and moved back.
func c14563Big() string {
	return c14563Columns + fmt.Sprintf(`SELECT '%s', NULL, %s, 'work_item', concat('linear:W-', leftPad(toString(n), 6, '0')), 'linear',
  multiIf(step = 0, '', step = 1, %s, %s), multiIf(step = 0, %s, step = 1, %s, %s), '', '', '',
  %s + toIntervalSecond(n * 7 + step), %s + toIntervalSecond(n * 7 + step), concat('ev-', toString(n), '-', toString(step)), %s + toIntervalDay(1)
FROM (SELECT number AS n FROM numbers(%d)) ARRAY JOIN [0, 1, 2] AS step`,
		c14563Org, c14563Repo, c14563Project("n % 40"), c14563Project("(n + 1) % 40"),
		c14563Project("n % 40"), c14563Project("(n + 1) % 40"), c14563Project("n % 40"),
		c14563Base, c14563Base, c14563Base, c14563Subjects)
}

// c14563Small is one hourly write of 15 rows; prefix 'W' = a new touch of an
// existing subject, any other prefix = new subjects.
func c14563Small(prefix, subject string) string {
	return c14563Columns + fmt.Sprintf(`SELECT '%s', NULL, %s, 'work_item', concat('linear:%s-', leftPad(toString(n), 6, '0')), 'linear',
  if('%s' = 'W', %s, ''), %s, '', '', '',
  %s + toIntervalDay(30) + toIntervalSecond(number), %s + toIntervalDay(30) + toIntervalSecond(number), concat('ev-', toString(n), '-new'), %s + toIntervalDay(31)
FROM (SELECT number, %s AS n FROM numbers(15))`,
		c14563Org, c14563Repo, prefix, prefix, c14563Project("n % 40"), c14563Project("(n + 2) % 40"),
		c14563Base, c14563Base, c14563Base, subject)
}

// c14563Layout: insideOneGranule = a small part whose keys all fall inside ONE
// granule of the big part. mainReadRows = acr main's edge count read_rows on CH
// 26.7 at max_threads = 1 (run 45395151455, job 138449238012): no layout takes
// the layered path at one thread there, so main reads 9 times at every layout.
type c14563Layout struct {
	name             string
	smalls           []string
	insideOneGranule bool
	mainReadRows     uint64
}

type c14563Run struct {
	counts   string
	readRows uint64
	steps    int
	layered  int
}

func TestCHAOS14563EdgeCountReadsTheTransitionsOnceInArmOne(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	exec := func(label, statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, statement)
		}
	}
	var version string
	if err := direct.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatal(err)
	}

	current, main := devhealthsource.MembershipEdgeCountMainSQLForTest(true)
	arm, mainArm := devhealthsource.MembershipIntervalsSQLForTest(true)
	sha := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:6]) }
	t.Logf("statement_sha main=%s current=%s", sha(main), sha(current))
	if sha(main) != c14563MainSHA {
		t.Errorf("WANT the oracle is the edge count of acr main e67c4128: statement_sha %s, want %s", sha(main), c14563MainSHA)
	}
	if sha(current) != c14563NewSHA {
		t.Errorf("WANT the shipped edge count statement_sha %s, got %s", c14563NewSHA, sha(current))
	}
	for _, s := range []string{current, main} {
		if !strings.HasSuffix(s, c14563Shipped) {
			t.Fatalf("the edge count no longer ends in %q:\n%s", c14563Shipped, s)
		}
	}

	exec("repos", fmt.Sprintf(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (%s, '%s', 'acme/repo', 'linear', %s)`, c14563Repo, c14563Org, c14563Base))
	exec("projects", fmt.Sprintf(`INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at)
SELECT %s, '%s', concat('P', toString(number)), '', 'linear', 'started', '', 1, %s FROM numbers(40)`, c14563Project("number"), c14563Org, c14563Base))
	exec("work items", fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at)
SELECT concat('linear:W-', leftPad(toString(number), 6, '0')), %s, '%s', 'w', 'open', '', '', 'linear', if(number %% 5 < 3, %s, ''), %s, %s FROM numbers(2000)`,
		c14563Repo, c14563Org, c14563Project("number % 40"), c14563Base, c14563Base))

	// Subject n of the big part sits at rows 3n..3n+2; granule 1 = subjects 0..2730, granule 2 = 2731..4657.
	layouts := []c14563Layout{
		{"L0-big-only", nil, false, 57939},
		{"L1-after-max-new-subjects", []string{c14563Small("W", "900000 + number")}, false, 57999},
		{"L2-before-min-new-subjects", []string{c14563Small("A", "number")}, false, 57999},
		{"L3-inside-granule-1", []string{c14563Small("W", "100 + number")}, true, 57999},
		{"L4-inside-granule-2", []string{c14563Small("W", "4000 + number")}, true, 57999},
		{"L5-across-the-granule-border", []string{c14563Small("W", "2724 + number")}, false, 57999},
		{"L6-spread-over-both-granules", []string{c14563Small("W", "number * 300")}, false, 57999},
		{"L7-after-max-plus-inside-granule-2", []string{c14563Small("W", "900000 + number"), c14563Small("W", "4000 + number")}, true, 58059},
		{"L8-two-after-max", []string{c14563Small("W", "900000 + number"), c14563Small("W", "910000 + number")}, false, 58059},
	}
	token := fmt.Sprintf("c14563-%d", time.Now().UnixNano())
	armStatement := func(sql string) string {
		return "SELECT toString(count()), toString(sum(cityHash64(org_id, subject_kind, repo_id, subject_id, provider, project_id, observed_at, event_id, ingest_at, valid_to, is_malformed, is_duplicate_add))) FROM " + sql + " AS a" + c14563Shipped
	}
	run := func(layout, label, statement string) c14563Run {
		t.Helper()
		var out c14563Run
		for r := 1; r <= 2; r++ {
			comment := fmt.Sprintf("%s|%s|%s|r%d", token, layout, label, r)
			rows, err := query.Query(ctx, statement+", log_comment = '"+comment+"', log_processors_profiles = 1", []contextpacket.ClickHouseBinding{{Name: "org_id", Value: c14563Org}})
			if err != nil {
				t.Fatalf("%s %s run %d: %v", layout, label, r, err)
			}
			var lines []string
			for rows.Next() {
				var key, value string
				var total uint64
				if label == "arm" || label == "arm-main" {
					if err := rows.Scan(&key, &value); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := rows.Scan(&key, &total); err != nil {
						t.Fatal(err)
					}
					value = fmt.Sprint(total)
				}
				lines = append(lines, key+"="+value)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			sort.Strings(lines)
			out.counts = strings.Join(lines, " ")
		}
		exec("flush logs", "SYSTEM FLUSH LOGS")
		var id string
		if err := direct.QueryRow(ctx, `SELECT query_id, read_rows FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ?`,
			fmt.Sprintf("%s|%s|%s|r2", token, layout, label)).Scan(&id, &out.readRows); err != nil {
			t.Fatalf("%s %s: query_log: %v", layout, label, err)
		}
		out.steps, out.layered = c14563Steps(t, ctx, direct, id)
		return out
	}

	var report []string
	for _, layout := range layouts {
		exec("truncate", "TRUNCATE TABLE project_membership_transitions")
		exec("start merges", "SYSTEM START MERGES project_membership_transitions")
		exec("big part", c14563Big())
		exec("optimize", "OPTIMIZE TABLE project_membership_transitions FINAL")
		exec("stop merges", "SYSTEM STOP MERGES project_membership_transitions")
		for _, small := range layout.smalls {
			exec("small part", small)
		}
		var parts uint64
		if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE active AND database = currentDatabase() AND table = 'project_membership_transitions'`).Scan(&parts); err != nil {
			t.Fatal(err)
		}
		if parts != uint64(1+len(layout.smalls)) {
			t.Fatalf("%s: %d parts, want %d", layout.name, parts, 1+len(layout.smalls))
		}
		now, was := run(layout.name, "count", current), run(layout.name, "count-main", main)
		armNow, armWas := run(layout.name, "arm", armStatement(arm)), run(layout.name, "arm-main", armStatement(mainArm))
		line := fmt.Sprintf("%s: count read_rows=%d steps=%d layered=%d | main read_rows=%d steps=%d layered=%d | arm steps=%d layered=%d read_rows=%d | arm-main steps=%d layered=%d read_rows=%d | counts=[%s]",
			layout.name, now.readRows, now.steps, now.layered, was.readRows, was.steps, was.layered,
			armNow.steps, armNow.layered, armNow.readRows, armWas.steps, armWas.layered, armWas.readRows, now.counts)
		report = append(report, line)
		t.Logf("LAYOUT %s", line)
		if now.counts == "" || now.counts != was.counts {
			t.Errorf("WANT %s: the edge counts equal acr main's: got [%s], main [%s]", layout.name, now.counts, was.counts)
		}
		if armNow.counts != armWas.counts {
			t.Errorf("WANT %s: the transition arm rows hash equal to acr main's: got [%s], main [%s]", layout.name, armNow.counts, armWas.counts)
		}
		if armNow.steps != 1 {
			t.Errorf("WANT %s: the transition arm reads project_membership_transitions once, got %d reads", layout.name, armNow.steps)
		}
		if now.steps > 3 {
			t.Errorf("WANT %s: the edge count reads project_membership_transitions at most 3 times (main %d), got %d", layout.name, was.steps, now.steps)
		}
		if was.readRows != layout.mainReadRows || was.steps != 9 || armWas.steps != 6 {
			t.Errorf("WANT %s: acr main reads the table 9 times (transition arm 6) and %d rows (measured, pinned); got %d reads (arm %d), %d rows", layout.name, layout.mainReadRows, was.steps, armWas.steps, was.readRows)
		}
		if now.readRows >= was.readRows {
			t.Errorf("WANT %s: the edge count reads fewer rows than acr main: read_rows %d, main %d", layout.name, now.readRows, was.readRows)
		}
	}
	t.Logf("server %s\n%s", version, strings.Join(report, "\n"))
}

// c14563Steps counts the reads of project_membership_transitions in one finished
// query, and how many of them took the layered FINAL path.
func c14563Steps(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, id string) (steps, layered int) {
	t.Helper()
	rows, err := direct.Query(ctx, `SELECT countIf(name = 'FilterSortedStreamByRange')
FROM system.processors_profile_log
WHERE query_id = ? AND plan_step_name = 'ReadFromMergeTree' AND endsWith(plan_step_description, '.project_membership_transitions')
GROUP BY plan_step ORDER BY plan_step`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var filters uint64
		if err := rows.Scan(&filters); err != nil {
			t.Fatal(err)
		}
		steps++
		if filters > 0 {
			layered++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if steps == 0 {
		t.Fatalf("query %s: no read of project_membership_transitions in processors_profile_log", id)
	}
	return steps, layered
}

// c14563Touch is one transitions row of the exactness fixture.
type c14563Touch struct {
	org, kind, subject, provider, from, to, event string
	occurred, synced, ingested                    int
}

// TestCHAOS14563TransitionArmRowsEqualMain checks the transition arm returns
// exactly acr main's rows, column types included, over every touch class:
// moves, duplicate ADD runs, a first REMOVE and a REMOVE after a REMOVE, two
// events in one millisecond, a native event id stored at two occurred_at
// values, from = to and empty touches, two providers of one subject, a pull
// request subject, a late earlier touch, and another organization's rows.
func TestCHAOS14563TransitionArmRowsEqualMain(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	const other = "f0f0f0f0-0000-4000-8000-00000000000c"
	w := func(n string) string { return "linear:W-" + n }
	insert := func(label string, touches []c14563Touch) {
		t.Helper()
		var values []string
		for _, r := range touches {
			values = append(values, fmt.Sprintf("('%s', NULL, %s, '%s', '%s', '%s', '%s', '%s', '', '', '', %s + toIntervalSecond(%d), %s + toIntervalSecond(%d), '%s', %s + toIntervalSecond(%d))",
				r.org, c14563Repo, r.kind, r.subject, r.provider, r.from, r.to, c14563Base, r.occurred, c14563Base, r.synced, r.event, c14563Base, r.ingested))
		}
		if err := direct.Exec(ctx, c14563Columns+"VALUES "+strings.Join(values, ", ")); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	o, wi, pr, ln := c14563Org, "work_item", "pull_request", "linear"
	insert("part 1", []c14563Touch{
		// moves: add P1, P1 -> P2, P2 -> P1
		{o, wi, w("1"), ln, "", "P1", "1a", 0, 0, 7}, {o, wi, w("1"), ln, "P1", "P2", "1b", 10, 10, 17}, {o, wi, w("1"), ln, "P2", "P1", "1c", 20, 20, 27},
		// duplicate ADD, REMOVE, re-ADD
		{o, wi, w("2"), ln, "", "P1", "2a", 0, 0, 7}, {o, wi, w("2"), ln, "", "P1", "2b", 5, 5, 12}, {o, wi, w("2"), ln, "P1", "", "2c", 10, 10, 17}, {o, wi, w("2"), ln, "", "P1", "2d", 15, 15, 22},
		// a first REMOVE; a REMOVE after a REMOVE
		{o, wi, w("3"), ln, "P1", "", "3a", 0, 0, 7},
		{o, wi, w("4"), ln, "P1", "", "4a", 0, 0, 7}, {o, wi, w("4"), ln, "P1", "", "4b", 5, 5, 12},
		// two events in one millisecond (event_id orders them)
		{o, wi, w("5"), ln, "", "P1", "5a", 0, 0, 7}, {o, wi, w("5"), ln, "P1", "P2", "5b", 0, 0, 7},
		// a native event id, first copy (a later sync moves it to occurred_at 30)
		{o, wi, w("6"), ln, "", "P1", "linear:h1", 0, 0, 7}, {o, wi, w("6"), ln, "P1", "P2", "linear:h2", 20, 20, 27},
		// from = to and an empty touch are no touches
		{o, wi, w("7"), ln, "P1", "P1", "7a", 0, 0, 7}, {o, wi, w("7"), ln, "", "", "7b", 5, 5, 12}, {o, wi, w("7"), ln, "", "P3", "7c", 10, 10, 17},
		// a pull request: ADD, duplicate ADD, move
		{o, pr, "42", ln, "", "P1", "8a", 0, 0, 7}, {o, pr, "42", ln, "", "P1", "8b", 3, 3, 10}, {o, pr, "42", ln, "P1", "P2", "8c", 6, 6, 13},
		// a run of three ADDs, REMOVE, ADD; the first touch ingested late
		{o, wi, w("9"), ln, "", "P1", "9a", 0, 0, 500}, {o, wi, w("9"), ln, "", "P1", "9b", 1, 1, 8}, {o, wi, w("9"), ln, "", "P1", "9c", 2, 2, 9},
		{o, wi, w("9"), ln, "P1", "", "9d", 3, 3, 10}, {o, wi, w("9"), ln, "", "P1", "9e", 4, 4, 11},
		// two providers of one subject
		{o, wi, w("11"), "jira", "", "P1", "11a", 0, 0, 7}, {o, wi, w("11"), ln, "", "P1", "11b", 1, 1, 8},
		// another organization
		{other, wi, w("1"), ln, "", "P1", "1a", 0, 0, 7}, {other, wi, w("1"), ln, "P1", "", "1b", 3, 3, 10},
	})
	insert("part 2", []c14563Touch{
		{o, wi, w("6"), ln, "", "P1", "linear:h1", 30, 40, 47},
		{o, wi, w("2"), ln, "", "P1", "2e", 25, 25, 32},
	})

	for _, ingest := range []bool{false, true} {
		current, main := devhealthsource.MembershipIntervalsSQLForTest(ingest)
		columns := "org_id, subject_kind, repo_id, subject_id, provider, project_id, observed_at, event_id, ingest_at, source, valid_to, is_malformed, is_duplicate_add"
		read := func(sql string) (rows []string, types string) {
			t.Helper()
			bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: c14563Org}}
			result, err := query.Query(ctx, "SELECT toString(tuple("+columns+")) AS r, toTypeName(tuple("+columns+")) FROM "+sql+" AS a ORDER BY r", bindings)
			if err != nil {
				t.Fatalf("ingest=%v: %v", ingest, err)
			}
			defer result.Close()
			for result.Next() {
				var row string
				if err := result.Scan(&row, &types); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
			}
			if err := result.Err(); err != nil {
				t.Fatal(err)
			}
			return rows, types
		}
		got, gotTypes := read(current)
		want, wantTypes := read(main)
		t.Logf("ingest=%v: %d rows, types %s", ingest, len(want), wantTypes)
		if gotTypes != wantTypes {
			t.Errorf("WANT ingest=%v: the column types of acr main %s, got %s", ingest, wantTypes, gotTypes)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("WANT ingest=%v: acr main's rows\n%s\ngot\n%s", ingest, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
		var malformed, duplicate, closed, open, otherOrg int
		for _, row := range want {
			switch {
			case strings.Contains(row, other):
				otherOrg++
			case strings.HasSuffix(row, ",1,0)"):
				malformed++
			case strings.HasSuffix(row, ",0,1)"):
				duplicate++
			case strings.HasSuffix(row, ",NULL,0,0)"):
				open++
			default:
				closed++
			}
		}
		if malformed < 3 || duplicate < 4 || closed < 4 || open < 4 || otherOrg != 0 {
			t.Fatalf("ingest=%v: the fixture lost a touch class: malformed=%d duplicate=%d closed=%d open=%d other_org=%d", ingest, malformed, duplicate, closed, open, otherOrg)
		}
	}
}
