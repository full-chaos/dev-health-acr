package devhealthsource_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// DRAFT-ONLY cell (CHAOS-14548), never merged. It runs the real BELONGS_TO_PROJECT
// edge count statement (statement_sha 7cbf3538fee3, as logged by the projector)
// over project_membership_transitions part layouts and checks which layout puts the
// FINAL read of that table on the layered path (FilterSortedStreamByRange).

const (
	c14548tOrg          = "f0f0f0f0-0000-4000-8000-00000000000a"
	c14548tRepo         = "toUUID('14548000-0000-4000-8000-000000000001')"
	c14548tBase         = "toDateTime64('2026-09-01 00:00:00', 3, 'UTC')"
	c14548tStatementSHA = "7cbf3538fee3"
	c14548tSubjects     = 4658 // x 3 touches = 13,974 rows: two granules (8,192 + 5,782), like prod
)

func c14548tProject(n string) string {
	return fmt.Sprintf("concat('14548200-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", n)
}

const c14548tColumns = "INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) "

// c14548tBig is one org's history: every subject added, moved and moved back.
func c14548tBig() string {
	return c14548tColumns + fmt.Sprintf(`SELECT '%s', NULL, %s, 'work_item', concat('linear:W-', leftPad(toString(n), 6, '0')), 'linear',
  multiIf(step = 0, '', step = 1, %s, %s), multiIf(step = 0, %s, step = 1, %s, %s), '', '', '',
  %s + toIntervalSecond(n * 7 + step), %s + toIntervalSecond(n * 7 + step), concat('ev-', toString(n), '-', toString(step)), %s + toIntervalDay(1)
FROM (SELECT number AS n FROM numbers(%d)) ARRAY JOIN [0, 1, 2] AS step`,
		c14548tOrg, c14548tRepo, c14548tProject("n % 40"), c14548tProject("(n + 1) % 40"),
		c14548tProject("n % 40"), c14548tProject("(n + 1) % 40"), c14548tProject("n % 40"),
		c14548tBase, c14548tBase, c14548tBase, c14548tSubjects)
}

// c14548tSmall is one hourly write of 15 rows. subject maps number (0..14) to the
// subject number; prefix 'W' = existing subjects (a new touch after their last one),
// other prefixes = new subjects.
func c14548tSmall(prefix, subject string) string {
	return c14548tColumns + fmt.Sprintf(`SELECT '%s', NULL, %s, 'work_item', concat('linear:%s-', leftPad(toString(n), 6, '0')), 'linear',
  if('%s' = 'W', %s, ''), %s, '', '', '',
  %s + toIntervalDay(30) + toIntervalSecond(number), %s + toIntervalDay(30) + toIntervalSecond(number), concat('ev-', toString(n), '-new'), %s + toIntervalDay(31)
FROM (SELECT number, %s AS n FROM numbers(15))`,
		c14548tOrg, c14548tRepo, prefix, prefix, c14548tProject("n % 40"), c14548tProject("(n + 2) % 40"),
		c14548tBase, c14548tBase, c14548tBase, subject)
}

// c14548tLayout: insideOneGranule = a small part whose keys all fall inside ONE
// granule of the big part. layered / readRows = the values measured on CH 26.7
// (run 45191320448, job 137750393680), pinned so the cell fails if they move.
type c14548tLayout struct {
	name             string
	smalls           []string
	insideOneGranule bool
	layered          int
	readRows         uint64
}

type c14548tStep struct {
	selects, filters, replacing uint64
	rows                        uint64
}

func TestCHAOS14548TransitionsLayoutPicksTheFinalPath(t *testing.T) {
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

	statement, _ := devhealthsource.MembershipEdgeCountSQLForTest(true)
	sum := sha256.Sum256([]byte(statement))
	if got := hex.EncodeToString(sum[:6]); got != c14548tStatementSHA {
		t.Fatalf("statement_sha %s, want %s (the projector's statement)", got, c14548tStatementSHA)
	}

	exec("repos", fmt.Sprintf(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (%s, '%s', 'acme/repo', 'linear', %s)`, c14548tRepo, c14548tOrg, c14548tBase))
	exec("projects", fmt.Sprintf(`INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at)
SELECT %s, '%s', concat('P', toString(number)), '', 'linear', 'started', '', 1, %s FROM numbers(40)`, c14548tProject("number"), c14548tOrg, c14548tBase))
	exec("work items", fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at)
SELECT concat('linear:W-', leftPad(toString(number), 6, '0')), %s, '%s', 'w', 'open', '', '', 'linear', if(number %% 5 < 3, %s, ''), %s, %s FROM numbers(2000)`,
		c14548tRepo, c14548tOrg, c14548tProject("number % 40"), c14548tBase, c14548tBase))

	// Subject n of the big part sits at rows 3n..3n+2; granule 1 = subjects 0..2730, granule 2 = 2731..4657.
	layouts := []c14548tLayout{
		{"L0-big-only", nil, false, 1, 63721},
		{"L1-after-max-new-subjects", []string{c14548tSmall("W", "900000 + number")}, false, 1, 63781},
		{"L2-before-min-new-subjects", []string{c14548tSmall("A", "number")}, false, 1, 66191},
		{"L3-inside-granule-1", []string{c14548tSmall("W", "100 + number")}, true, 4, 81127},
		{"L4-inside-granule-2", []string{c14548tSmall("W", "4000 + number")}, true, 4, 81127},
		{"L5-across-the-granule-border", []string{c14548tSmall("W", "2724 + number")}, false, 0, 57999},
		{"L6-spread-over-both-granules", []string{c14548tSmall("W", "number * 300")}, false, 0, 57999},
		{"L7-after-max-plus-inside-granule-2", []string{c14548tSmall("W", "900000 + number"), c14548tSmall("W", "4000 + number")}, true, 4, 81187},
		{"L8-two-after-max", []string{c14548tSmall("W", "900000 + number"), c14548tSmall("W", "910000 + number")}, false, 1, 63841},
	}
	token := fmt.Sprintf("c14548t-%d", time.Now().UnixNano())
	var report []string
	stepCount := -1
	for _, layout := range layouts {
		exec("truncate", "TRUNCATE TABLE project_membership_transitions")
		exec("start merges", "SYSTEM START MERGES project_membership_transitions")
		exec("big part", c14548tBig())
		exec("optimize", "OPTIMIZE TABLE project_membership_transitions FINAL")
		exec("stop merges", "SYSTEM STOP MERGES project_membership_transitions")
		for _, small := range layout.smalls {
			exec("small part", small)
		}
		var parts, total, bigMarks uint64
		if err := direct.QueryRow(ctx, `SELECT count(), sum(rows), max(marks) FROM system.parts WHERE active AND database = currentDatabase() AND table = 'project_membership_transitions'`).Scan(&parts, &total, &bigMarks); err != nil {
			t.Fatal(err)
		}
		if parts != uint64(1+len(layout.smalls)) {
			t.Fatalf("%s: %d parts, want %d", layout.name, parts, 1+len(layout.smalls))
		}
		var id string
		var readRows uint64
		for r := 1; r <= 2; r++ {
			comment := fmt.Sprintf("%s|%s|r%d", token, layout.name, r)
			rows, err := query.Query(ctx, statement+" SETTINGS max_threads = 4, log_comment = '"+comment+"', log_processors_profiles = 1", []contextpacket.ClickHouseBinding{{Name: "org_id", Value: c14548tOrg}})
			if err != nil {
				t.Fatalf("%s run %d: %v", layout.name, r, err)
			}
			for rows.Next() {
				var source string
				var edges uint64
				if err := rows.Scan(&source, &edges); err != nil {
					t.Fatal(err)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		exec("flush logs", "SYSTEM FLUSH LOGS")
		if err := direct.QueryRow(ctx, `SELECT query_id, read_rows FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ?`,
			fmt.Sprintf("%s|%s|r2", token, layout.name)).Scan(&id, &readRows); err != nil {
			t.Fatalf("%s: query_log: %v", layout.name, err)
		}
		steps := c14548tSteps(t, ctx, direct, id)
		if stepCount < 0 {
			stepCount = len(steps)
		}
		var rowsPerStep []string
		layered, plain, over := 0, 0, 0
		for _, s := range steps {
			rowsPerStep = append(rowsPerStep, fmt.Sprint(s.rows))
			if s.filters > 0 {
				layered++
			} else {
				plain++
			}
			if s.rows > total {
				over++
			}
		}
		line := fmt.Sprintf("%s: parts=%d rows=%d big_marks=%d read_rows=%d transitions_steps=%d layered=%d plain=%d over_total=%d step_rows=[%s]",
			layout.name, parts, total, bigMarks, readRows, len(steps), layered, plain, over, strings.Join(rowsPerStep, " "))
		report = append(report, line)
		t.Logf("LAYOUT %s", line)
		if len(steps) == 0 || len(steps) != stepCount {
			t.Errorf("%s: %d transitions read steps, want %d (> 0)", layout.name, len(steps), stepCount)
		}
		// The flip: a small part inside ONE granule of the big part layers more transitions
		// reads (each re-reads a granule of the big part); any other layout does not.
		if layout.insideOneGranule != (layered > 1) || layered != over {
			t.Errorf("WANT %s: inside one granule = %v, so layered > 1 must be %v and every layered step must read more than the %d rows; got layered=%d over=%d",
				layout.name, layout.insideOneGranule, layout.insideOneGranule, total, layered, over)
		}
		if layered != layout.layered || readRows != layout.readRows {
			t.Errorf("WANT %s: layered=%d read_rows=%d (measured, pinned); got layered=%d read_rows=%d", layout.name, layout.layered, layout.readRows, layered, readRows)
		}
	}
	t.Logf("server %s, statement_sha %s\n%s", version, c14548tStatementSHA, strings.Join(report, "\n"))
}

func c14548tSteps(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, id string) []c14548tStep {
	t.Helper()
	rows, err := direct.Query(ctx, `SELECT countIf(startsWith(name, 'MergeTreeSelect')), countIf(name = 'FilterSortedStreamByRange'), countIf(name = 'ReplacingSorted'),
  sumIf(output_rows, startsWith(name, 'MergeTreeSelect'))
FROM system.processors_profile_log
WHERE query_id = ? AND plan_step_name = 'ReadFromMergeTree' AND endsWith(plan_step_description, '.project_membership_transitions')
GROUP BY plan_step ORDER BY plan_step`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []c14548tStep
	for rows.Next() {
		var s c14548tStep
		if err := rows.Scan(&s.selects, &s.filters, &s.replacing, &s.rows); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return steps
}
