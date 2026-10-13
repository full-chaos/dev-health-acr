package devhealthfacts_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// scopeStatementCeiling is the most one scope expander statement may use at
// either size: half of the work-item reader's 64 MiB statement cap.
const scopeStatementCeiling = uint64(32 << 20)

// workItemSelectionCeiling bounds the two work-item selections, whose
// authorization relations cost a fixed amount per thread: the peak measured
// at two threads (33 MB, 100k and 300k unrelated work items) plus the slope.
const workItemSelectionCeiling = uint64(statementMemorySlope * 33_000_000)

// scopeExpanderStatements names each scope expander statement that reads work
// items, link rows or transitions by a fragment only it carries, and a table
// it reads (query_log's tables).
// devhealthschema:not-a-production-replica: these names select the guarded statements' query_log rows; they declare no table.
var scopeExpanderStatements = []struct {
	label, table, fragment string
	threadBound            bool
}{
	{"projectRepositories", "work_items", "min(p.id), argMax(l.provenance, l.link_rank)", false},
	{"projectRepositoriesAsOf", "work_items", "max(a.via_history) AS via_history", false},
	{"teamRepositories", "team_repo_ownership", "argMin(source, source_rank) AS best_source", false},
	{"teamWorkItems", "work_items", "min(a.team_id) AS origin_id", true},
	{"projectWorkItems", "work_items", "p.id)) AS origin_id", true},
	{"projectLinkedRepositories", "work_items", "AS link_state", false},
	{"projectUnlinkedIssueCount", "work_items", "SELECT uniqExact(w.work_item_id)\nFROM work_items AS w FINAL", false},
	{"projectUnlinkedIssueCountAsOf", "work_items", "SELECT uniqExact(a.work_item_id)", false},
	{"teamUnlinkedIssueCount", "work_items", "SELECT uniqExact(w.work_item_id)\nFROM (SELECT provider, id, team_id", false},
	{"historicalDropCount", "work_graph_issue_pr", "AS ever_matched", false},
	{"historicalTouchCount", "project_membership_transitions", "AS malformed_touches", false},
}

// fillScopeOrganization adds the every-root filler (work items, attributions,
// ownership and links of teams and projects nobody requested) plus the pull
// requests those links name, unrelated pull requests, and membership
// transitions of the filler work items.
func fillScopeOrganization(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, f everyRootFixture, prefix string, rows uint64) {
	t.Helper()
	fillEveryRootOrganization(t, ctx, direct, f, prefix, rows)
	at := time.Now().UTC().Truncate(time.Second)
	for label, statement := range map[string]struct {
		sql  string
		args []any
	}{
		"linked pull requests": {`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 10), 'filler', 'open', ?, ?, NULL, NULL, 'filler', '' FROM numbers(1000)`,
			[]any{f.shared, f.orgID, at, at}},
		"unrelated pull requests": {`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT generateUUIDv4(), ?, toUInt32(number % 100 + 1), 'filler', 'open', ?, ?, NULL, NULL, 'filler', '' FROM numbers(?)`,
			[]any{f.orgID, at, at, rows / 10}},
		"transitions": {`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id)
SELECT ?, NULL, ?, 'work_item', concat(?, '-', repeat('x', 64), '-', toString(number)), 'linear', '', concat('FILLER-', toString(number % 500)), '', '', '', ?, ?, concat(?, '-event-', toString(number)) FROM numbers(?)`,
			[]any{f.orgID, f.shared, prefix, at.Add(-time.Hour), at, prefix, rows / 2}},
	} {
		if err := direct.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("%s filler: %v", label, err)
		}
	}
}

type movedOwnership struct{ repo, projectRepo string }

// seedMovedOwnership seeds ownership that moved from requested team B to
// unrequested team C in a later version: a repository B owned (its row closed
// by a later version of the same key) and a project B owned the same way,
// whose issue links a pull request in a repository nobody else reaches.
func seedMovedOwnership(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, f everyRootFixture) movedOwnership {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Second)
	since, closed := at.Add(-30*24*time.Hour), at.Add(-time.Hour)
	moved := movedOwnership{repo: repoUUID(f.orgID + "-moved"), projectRepo: repoUUID(f.orgID + "-moved-project")}
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed moved ownership: %v\n%s", err, statement)
		}
	}
	for _, repo := range []struct{ id, slug string }{{moved.repo, "acme/moved"}, {moved.projectRepo, "acme/moved-project"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo.id, f.orgID, repo.slug, "github", at)
	}
	for _, own := range []struct {
		team    string
		validTo any
		updated time.Time
	}{{"B", nil, since}, {"B", closed, at}, {"C", nil, at}} {
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.orgID, "linear", own.team, moved.repo, "acme/moved", "exact", "native", uint8(1), uint16(100), int32(0), since, own.validTo, own.updated)
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			f.orgID, "linear", own.team, "M", nil, "native", since, own.validTo, own.updated)
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"M", f.orgID, "linear", nil, "Project M", uint8(1), "active", "", at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"issue-M", f.shared, f.orgID, "linear", "issue M", "open", "", "", "M", at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		moved.projectRepo, f.orgID, uint32(4), "pr", "open", at, at, "feature", "")
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		moved.projectRepo, "issue-M", uint32(4), float32(1), "native", "", at, f.orgID)
	return moved
}

// scopeRows runs a statement (its SETTINGS clause removed) and returns its rows
// as sorted text, so two statements' results compare whole.
func scopeRows(t *testing.T, ctx context.Context, client *runtimeclickhouse.Client, captured capturedStatement) []string {
	t.Helper()
	body := captured.statement
	if at := strings.LastIndex(body, "\nSETTINGS "); at >= 0 {
		body = body[:at]
	}
	rows, err := client.Query(ctx, `SELECT arraySort(groupArray(formatRowNoNewline('TSVRaw', *))) FROM (`+body+`)`, captured.bindings)
	if err != nil {
		t.Fatalf("rows: %v\n%s", err, body)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		if err := rows.Scan(&out); err != nil {
			t.Fatalf("scan rows: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// Every scope expander statement reads the requested origins' work items,
// links and transitions only. On one dataset, at two sizes of unrelated
// organization data: each statement returns the same rows as its
// organization-wide form, its peak memory does not grow with the unrelated
// rows and stays under the ceiling, and the organization-wide form does grow
// (the guard's planted defect, measured on the same data).
func TestScopeExpanderStatementsFollowTheRequestedScopeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(append(append([]string{}, sharedClickHouseTables...), "project_membership_transitions", "work_item_dependencies")...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "0c7d2e94-5a1b-4f63-8b0e-9d4c2a7f1e38")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	if err := direct.Exec(ctx, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', 'issue-Q', 'linear', '', 'Q', '', '', '', ?, ?, 'linear:issue-Q-add')`,
		fixture.orgID, fixture.shared, since, since); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	moved := seedMovedOwnership(t, ctx, direct, fixture)
	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)

	// A repository and a project team B owned in an earlier version and team
	// C owns now never reach the requested teams; the same statement read
	// without FINAL on the ownership tables does reach them, so this row sees
	// a stale ownership version if a filter ever lets one through.
	t.Run("ownership moved away from a requested team", func(t *testing.T) {
		recorder.reset()
		var teamRepositories contextfabric.FactScopeExpansionRequest
		for _, tc := range fixture.cases() {
			if tc.name == "team repositories" {
				teamRepositories = tc.request
			}
		}
		result, err := expander.ExpandFactScope(ctx, teamRepositories)
		if err != nil {
			t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
		}
		for _, target := range result.Targets {
			for _, repo := range []string{moved.repo, moved.projectRepo} {
				if strings.HasSuffix(target.CanonicalID, repo) {
					t.Fatalf("target %s is reached through ownership the requested team no longer holds", target.CanonicalID)
				}
			}
		}
		captured, ok := recorder.find("argMin(source, source_rank) AS best_source")
		if !ok {
			t.Fatal("teamRepositories statement was not sent")
		}
		stale := strings.ReplaceAll(strings.ReplaceAll(captured.statement, "FROM team_repo_ownership FINAL", "FROM team_repo_ownership"), "FROM team_project_ownership FINAL", "FROM team_project_ownership")
		if stale == captured.statement {
			t.Fatal("the statement reads no ownership table with FINAL")
		}
		rows := strings.Join(scopeRows(t, ctx, query, capturedStatement{statement: stale, bindings: captured.bindings}), "\n")
		for _, repo := range []string{moved.repo, moved.projectRepo} {
			if !strings.Contains(rows, repo) {
				t.Fatalf("the statement without FINAL does not reach %s, so this row cannot see a stale ownership version:\n%s", repo, rows)
			}
		}
	})

	// served sends every fixture request through the expander and returns
	// each guarded statement as sent, with its organization-wide form.
	type servedStatement struct{ scoped, orgWide capturedStatement }
	served := func(t *testing.T) map[string]servedStatement {
		recorder.reset()
		for _, tc := range fixture.cases() {
			if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
				t.Fatalf("%s: ExpandFactScope: %v: %s", tc.name, err, readFailureCause(err))
			}
		}
		out := map[string]servedStatement{}
		for _, statement := range scopeExpanderStatements {
			captured, ok := recorder.find(statement.fragment)
			if !ok {
				t.Fatalf("%s: no statement carrying %q was sent", statement.label, statement.fragment)
			}
			orgWide, filtered, clean := devhealthfacts.OrgWideScopeStatementForTest(captured.statement)
			if !filtered || !clean {
				t.Fatalf("%s: the served statement does not carry a removable scope filter (filtered %v, clean %v):\n%s", statement.label, filtered, clean, captured.statement)
			}
			out[statement.label] = servedStatement{scoped: captured, orgWide: capturedStatement{statement: orgWide, bindings: captured.bindings}}
		}
		return out
	}
	differential := func(t *testing.T, statements map[string]servedStatement) {
		for _, statement := range scopeExpanderStatements {
			pair := statements[statement.label]
			scoped, orgWide := scopeRows(t, ctx, query, pair.scoped), scopeRows(t, ctx, query, pair.orgWide)
			if len(scoped) == 0 {
				t.Fatalf("%s: the statement returned no rows, the comparison did not happen", statement.label)
			}
			if !slices.Equal(scoped, orgWide) {
				t.Errorf("%s: rows differ from the organization-wide form:\nscoped   %q\norg-wide %q", statement.label, scoped, orgWide)
			}
		}
	}

	// runPeak runs one statement as sent and returns its peak memory and
	// duration from query_log (a failed run is measured too: a statement over
	// a limit is a peak, not a missing reading).
	runPeak := func(t *testing.T, table, fragment string, captured capturedStatement) (uint64, uint64) {
		// query_log is matched on the fragment's first line: one statement
		// runs after the mark, and the logged text need not keep newlines.
		fragment, _, _ = strings.Cut(fragment, "\n")
		mark := serverNowMicros(t, ctx, direct)
		if rows, err := query.Query(ctx, captured.statement, captured.bindings); err == nil {
			for rows.Next() {
			}
			_ = rows.Close()
		}
		return peakOf(t, ctx, direct, table, fragment, mark), durationOf(t, ctx, direct, table, fragment, mark)
	}
	type variant struct{ peak, spread, duration uint64 }
	type reading struct{ scoped, orgWide, unbounded variant }
	sample := func(t *testing.T, table, fragment string, captured capturedStatement, runs int) variant {
		var peaks, durations []uint64
		for attempt := 0; attempt < runs; attempt++ {
			peak, duration := runPeak(t, table, fragment, captured)
			peaks, durations = append(peaks, peak), append(durations, duration)
		}
		return variant{peak: slices.Min(peaks), spread: slices.Max(peaks) - slices.Min(peaks), duration: slices.Min(durations)}
	}
	measure := func(t *testing.T, statements map[string]servedStatement) map[string]reading {
		readings := map[string]reading{}
		for _, statement := range scopeExpanderStatements {
			pair := statements[statement.label]
			r := reading{
				scoped:  sample(t, statement.table, statement.fragment, pair.scoped, 3),
				orgWide: sample(t, statement.table, statement.fragment, pair.orgWide, 3),
			}
			if statement.threadBound {
				unbounded := strings.Replace(pair.scoped.statement, ", max_threads = 2", "", 1)
				if unbounded == pair.scoped.statement {
					t.Fatalf("%s: the served statement carries no max_threads = 2:\n%s", statement.label, pair.scoped.statement)
				}
				r.unbounded = sample(t, statement.table, statement.fragment, capturedStatement{statement: unbounded, bindings: pair.scoped.bindings}, 3)
			}
			readings[statement.label] = r
		}
		return readings
	}
	// grew applies the slope guard: the larger reading is over the slope AND
	// beyond both sizes' run-to-run spread and the absolute floor.
	grew := func(small, large variant) bool {
		return float64(large.peak) > statementMemorySlope*float64(small.peak) && large.peak > small.peak+max(arrayMemoryFloor, small.spread+large.spread)
	}

	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	smallStatements := served(t)
	t.Run("rows at the smaller size", func(t *testing.T) { differential(t, smallStatements) })
	measure(t, smallStatements)
	small := measure(t, smallStatements)
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	largeStatements := served(t)
	t.Run("rows at the larger size", func(t *testing.T) { differential(t, largeStatements) })
	large := measure(t, largeStatements)

	for _, statement := range scopeExpanderStatements {
		label := statement.label
		s, l := small[label], large[label]
		t.Logf("PEAK %s scoped small=%d large=%d (spread %d/%d) org-wide small=%d large=%d (spread %d/%d)", label, s.scoped.peak, l.scoped.peak, s.scoped.spread, l.scoped.spread, s.orgWide.peak, l.orgWide.peak, s.orgWide.spread, l.orgWide.spread)
		if s.scoped.peak == 0 || l.scoped.peak == 0 || s.orgWide.peak == 0 || l.orgWide.peak == 0 {
			t.Fatalf("%s: measurement did not happen (small %+v, large %+v)", label, s, l)
		}
		if grew(s.scoped, l.scoped) {
			t.Errorf("%s grew with unrelated organization rows: %d bytes at the larger size, %d at the smaller", label, l.scoped.peak, s.scoped.peak)
		}
		ceiling := scopeStatementCeiling
		if statement.threadBound {
			ceiling = workItemSelectionCeiling
		}
		for _, peak := range []uint64{s.scoped.peak, l.scoped.peak} {
			if peak >= ceiling {
				t.Errorf("%s peaked at %d bytes, want under %d", label, peak, ceiling)
			}
		}
		if !statement.threadBound {
			if !grew(s.orgWide, l.orgWide) {
				t.Errorf("%s: the organization-wide form did not grow (%d bytes at the larger size, %d at the smaller): the guard cannot fail on this data", label, l.orgWide.peak, s.orgWide.peak)
			}
			continue
		}
		// The selection's cost is fixed per thread, not grown with the
		// organization: its planted defect is the unbounded statement, which
		// must break the ceiling on the same data.
		t.Logf("DURATION %s small: %d ms at two threads, %d ms unbounded; large: %d ms at two threads, %d ms unbounded; unbounded peak small=%d large=%d", label, s.scoped.duration, s.unbounded.duration, l.scoped.duration, l.unbounded.duration, s.unbounded.peak, l.unbounded.peak)
		for _, peak := range []uint64{s.unbounded.peak, l.unbounded.peak} {
			if peak < ceiling {
				t.Errorf("%s: the statement without max_threads peaked at %d bytes, under the %d ceiling: the guard cannot fail on this data", label, peak, ceiling)
			}
		}
	}
}

// durationOf is the longest query_duration_ms of the statements matching
// table and fragment logged after mark.
func durationOf(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, table, fragment string, mark int64) uint64 {
	t.Helper()
	var duration uint64
	if err := direct.QueryRow(ctx, `SELECT max(query_duration_ms) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0`, mark, table, fragment).Scan(&duration); err != nil {
		t.Fatalf("read query_log duration: %v", err)
	}
	return duration
}
