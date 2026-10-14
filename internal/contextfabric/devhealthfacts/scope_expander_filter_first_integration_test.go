package devhealthfacts_test

import (
	"context"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	"slices"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// scopeStatementCeiling is the most one scope expander statement may use at
// either size: half of the work-item reader's 64 MiB statement cap.
const scopeStatementCeiling = uint64(32 << 20)

// slopeFloors replaces the slope floor for the statements whose peak moves
// between runs of one dataset by more than the default floor: 8 MiB for the two
// historical statements (spread up to 6.6 MB over 36 samples), 6 MiB for
// projectRepositoriesAsOf (spread up to 4.5 MB, a +2.9 MB diff failed on CI).
// All from hosted runs 38461148186, 38463043081, 38464327840: 18.7-24.0 MB at
// both sizes, no trend with size. The 1.15x rule is unchanged, and each
// statement's organization-wide form grows by 260 MB or more.
var slopeFloors = map[string]uint64{"historicalDropCount": 8 << 20, "historicalTouchCount": 8 << 20, "projectRepositoriesAsOf": 6 << 20}

// workItemSelectionCeiling bounds the two work-item selections, whose
// authorization relations cost a fixed amount per thread: the peak measured
// at two threads on merged parts (21.8 MB at 100k and 300k unrelated work
// items; unbounded 36.7-37.2 MB) plus the slope.
const workItemSelectionCeiling = uint64(statementMemorySlope * 22_000_000)

// scopeExpanderStatements names each scope expander statement that reads work
// items, link rows or transitions by a fragment only it carries, and a table
// it reads (query_log's tables).
// devhealthschema:not-a-production-replica: these names select the guarded statements' query_log rows; they declare no table.
var scopeExpanderStatements = []struct {
	label, table, fragment   string
	threadBound, unmergedLog bool
}{
	{"projectRepositories", "work_items", "min(p.id), argMax(l.provenance, l.link_rank)", false, true},
	{"projectRepositoriesAsOf", "work_items", "max(a.via_history) AS via_history", false, false},
	{"teamRepositories", "team_repo_ownership", "argMin(source, source_rank) AS best_source", false, false},
	{"teamWorkItems", "work_items", "min(a.team_id) AS origin_id", true, false},
	{"projectWorkItems", "work_items", "p.id)) AS origin_id", true, false},
	{"projectLinkedRepositories", "work_items", "AS link_state", false, true},
	{"projectUnlinkedIssueCount", "work_items", "SELECT uniqExact(w.work_item_id)\nFROM work_items AS w FINAL", false, true},
	{"projectUnlinkedIssueCountAsOf", "work_items", "SELECT uniqExact(a.work_item_id)", false, false},
	{"teamUnlinkedIssueCount", "work_items", "SELECT uniqExact(w.work_item_id)\nFROM (SELECT provider, id, team_id", false, false},
	{"historicalDropCount", "work_graph_issue_pr", "AS ever_matched", false, false},
	{"historicalTouchCount", "project_membership_transitions", "AS malformed_touches", false, false},
}

// fillScopeOrganization adds the every-root filler (work items, attributions,
// ownership and links of teams and projects nobody requested) plus the pull
// requests those links name, unrelated pull requests, and membership
// transitions of the filler work items.
func fillScopeOrganization(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, f everyRootFixture, prefix string, rows uint64) {
	t.Helper()
	// devhealthschema:not-a-production-replica: test seed and merge control over tables the shared DDL creates; they declare no table.
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

// devhealthschema:not-a-production-replica: test seed and merge control over tables the shared DDL creates; they declare no table.
// mergeScopeTables leaves one part per filled table, so the guarded readings
// follow the rows, not how many parts the inserts happen to leave unmerged.
func mergeScopeTables(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	tables, err := direct.Query(ctx, `SELECT name FROM system.tables WHERE database = currentDatabase() AND engine LIKE '%MergeTree%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	tables.Close()
	if len(names) == 0 {
		t.Fatal("no table to merge: the layout step did not happen")
	}
	for _, name := range names {
		if err := direct.Exec(ctx, "OPTIMIZE TABLE `"+name+"` FINAL"); err != nil {
			t.Fatalf("optimize %s: %v", name, err)
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
	// devhealthschema:not-a-production-replica: test seed and merge control over tables the shared DDL creates; they declare no table.
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
	// A blocker edge between two linked issues of the requested projects, and
	// a pull request in their repository that no link row names.
	exec(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"issue-Q", "issue-P", "blocks", "Blocks", at, f.orgID)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		f.shared, f.orgID, uint32(77), "unlinked", "open", at, at, "feature", "")
	// Requested issues with membership transitions in a second repository:
	// issue-Q moves into Q there too, and issue-P has transitions only there,
	// so the as-of touches and the no-history arm's NOT IN set must read every
	// repository of a kept subject.
	for _, move := range []struct{ subject, project, event string }{{"issue-Q", "Q", "linear:issue-Q-own-add"}, {"issue-P", "P", "linear:issue-P-own-add"}} {
		exec(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
			f.orgID, f.own, move.subject, move.project, since, since, move.event)
	}
	// A project whose id is empty, owned by requested team A: the library's
	// ownership and identity readers match an empty id, so the fixture's
	// project-less issues are its work items, and one of them links a pull
	// request in a repository only that project reaches.
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"", f.orgID, "linear", nil, "Project with an empty id", uint8(1), "active", "", at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		f.orgID, "linear", "A", "", nil, "native", since, nil, at)
	emptyProjectRepo := repoUUID(f.orgID + "-empty-project")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, emptyProjectRepo, f.orgID, "acme/empty-project", "github", at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		emptyProjectRepo, f.orgID, uint32(5), "pr", "open", at, at, "feature", "")
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		emptyProjectRepo, "item-both-blocker", uint32(5), float32(1), "native", "", at, f.orgID)
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
	// Every declared table: the link helper's other callers are read through
	// every provider, and a provider's own tables must exist.
	for _, statement := range devhealthschema.DDL() {
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
	// devhealthschema:not-a-production-replica: test seed and merge control over tables the shared DDL creates; they declare no table.
	// Merges stay off on the ownership tables until the moved-ownership row
	// has read the stale versions it seeds: a background merge would remove
	// them, and the row would no longer see one.
	for _, table := range []string{"team_repo_ownership", "team_project_ownership"} {
		if err := direct.Exec(ctx, `SYSTEM STOP MERGES `+table); err != nil {
			t.Fatalf("stop merges on %s: %v", table, err)
		}
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
		// devhealthschema:not-a-production-replica: test seed and merge control over tables the shared DDL creates; they declare no table.
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
	// devhealthschema:not-a-production-replica: test seed and merge control over tables the shared DDL creates; they declare no table.
	for _, table := range []string{"team_repo_ownership", "team_project_ownership"} {
		if err := direct.Exec(ctx, `SYSTEM START MERGES `+table); err != nil {
			t.Fatalf("start merges on %s: %v", table, err)
		}
	}

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
	// grew is the slope guard: the larger size's reading (the smallest of
	// three warm runs, both sizes on one merged part per table) over 1.15x
	// the smaller size's AND over it by more than a fixed floor. A cost that
	// grows with the rows lifts all three runs, so the minimum carries it;
	// the floor keeps a small statement's jitter out.
	grew := func(small, large variant, floor uint64) bool {
		return float64(large.peak) > statementMemorySlope*float64(small.peak) && large.peak > small.peak+floor
	}

	// The guard reads merged parts; production tables are not one part, so
	// the project statements are also logged on the layout the inserts
	// leave, every run, at both sizes.
	unmerged := func(t *testing.T, size string) {
		statements := served(t)
		for _, statement := range scopeExpanderStatements {
			if !statement.unmergedLog {
				continue
			}
			var peaks []uint64
			for attempt := 0; attempt < 3; attempt++ {
				peak, _ := runPeak(t, statement.table, statement.fragment, statements[statement.label].scoped)
				peaks = append(peaks, peak)
			}
			t.Logf("UNMERGED %s %s peaks %v", statement.label, size, peaks)
		}
	}

	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	unmerged(t, "small")
	var callers []linkHelperCaller
	t.Run("link helper callers outside the expander", func(t *testing.T) { callers = linkHelperCallersDifferential(t, ctx, query, fixture) })
	t.Run("link helper callers at the smaller size", func(t *testing.T) { linkHelperCallersCost(t, ctx, query, direct, callers) })
	mergeScopeTables(t, ctx, direct)
	smallStatements := served(t)
	t.Run("rows at the smaller size", func(t *testing.T) { differential(t, smallStatements) })
	measure(t, smallStatements)
	small := measure(t, smallStatements)
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	unmerged(t, "large")
	t.Run("link helper callers at the larger size", func(t *testing.T) { linkHelperCallersCost(t, ctx, query, direct, callers) })
	mergeScopeTables(t, ctx, direct)
	largeStatements := served(t)
	t.Run("rows at the larger size", func(t *testing.T) { differential(t, largeStatements) })
	measure(t, largeStatements)
	large := measure(t, largeStatements)

	for _, statement := range scopeExpanderStatements {
		label := statement.label
		s, l := small[label], large[label]
		t.Logf("PEAK %s scoped small=%d large=%d (spread %d/%d) org-wide small=%d large=%d (spread %d/%d)", label, s.scoped.peak, l.scoped.peak, s.scoped.spread, l.scoped.spread, s.orgWide.peak, l.orgWide.peak, s.orgWide.spread, l.orgWide.spread)
		if s.scoped.peak == 0 || l.scoped.peak == 0 || s.orgWide.peak == 0 || l.orgWide.peak == 0 {
			t.Fatalf("%s: measurement did not happen (small %+v, large %+v)", label, s, l)
		}
		floor := arrayMemoryFloor
		if own, ok := slopeFloors[label]; ok {
			floor = own
		}
		if grew(s.scoped, l.scoped, floor) {
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
			if !grew(s.orgWide, l.orgWide, floor) {
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

// linkHelperCallerSites names each statement outside the scope expander that
// reads the link rows through issueLinkRowsAmongSQL with a candidate set.
var blockedByKeySQL = dependencyrelation.KeySQL("d.relationship_type")

var linkHelperCallerSites = []struct{ label, fragment string }{
	{"blocker repository edges", "AS rid, if(" + blockedByKeySQL + " = 'blocks:inv', d.source_work_item_id, d.target_work_item_id) AS tgt"},
	{"blocker project edges", "SELECT pl.team_id AS team_id, if(" + blockedByKeySQL + " = 'blocks:inv', d.source_work_item_id, d.target_work_item_id) AS tgt"},
	{"blocked items without a link", "if(" + blockedByKeySQL + " = 'blocks:inv', d.source_work_item_id, d.target_work_item_id) NOT IN (SELECT work_item_id FROM"},
	{"project linked pull requests", "SELECT pi.project_key AS project_key, l.pr_repo_id AS pr_repo_id"},
}

// linkHelperCallersDifferential reads every fact kind for the fixture's
// teams, projects and repository through the real providers, and compares
// each statement that reads the narrowed pull request rows with the same
// statement over the whole pull request table: the rows must be equal.
// linkHelperCaller is one statement outside the expander that reads the
// narrowed pull request rows, with its whole-table reference form.
type linkHelperCaller struct {
	label  string
	served capturedStatement
	whole  string
}

func linkHelperCallersDifferential(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, f everyRootFixture) []linkHelperCaller {
	recorder := &recordingQueryClient{inner: query}
	principal := storage.Principal{OrgID: f.orgID, RepositoryScopes: []string{"*"}}
	subjects := []contextfabric.SubjectRef{f.teamA, f.teamB, f.projectP, f.projectQ, {Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + f.shared, Label: "acme/shared"}}
	for _, provider := range devhealthfacts.NewProviders(recorder) {
		kind := provider.Capability().Kind
		for _, subject := range subjects {
			_, _ = provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind, Subjects: []contextfabric.SubjectRef{subject},
			})
		}
	}
	recorder.mu.Lock()
	captured := append([]capturedStatement(nil), recorder.captured...)
	recorder.mu.Unlock()
	reached := map[string]int{}
	compared := 0
	var callers []linkHelperCaller
	for _, statement := range captured {
		whole, rewritten, ok := devhealthfacts.UnprunedPullRequestStatementForTest(statement.statement)
		if !ok {
			t.Fatalf("a narrowed pull request read is not closed as the rewrite expects:\n%s", statement.statement)
		}
		whole, _ = devhealthfacts.OrgWideProjectRollupStatementForTest(whole)
		if rewritten == 0 {
			continue
		}
		label := "other"
		for _, site := range linkHelperCallerSites {
			if strings.Contains(statement.statement, site.fragment) {
				reached[site.label]++
				label = site.label
			}
		}
		callers = append(callers, linkHelperCaller{label: label, served: statement, whole: whole})
		head, _, _ := strings.Cut(strings.TrimSpace(statement.statement), "\n")
		t.Logf("DIFFERENTIAL comparing %.120s", head)
		narrowed := scopeRows(t, ctx, query, statement)
		wholeRows := scopeRows(t, ctx, query, capturedStatement{statement: whole, bindings: statement.bindings})
		if !slices.Equal(narrowed, wholeRows) {
			t.Errorf("rows differ from the whole pull request table:\nnarrowed %q\nwhole    %q\n%s", narrowed, wholeRows, statement.statement)
		}
		compared++
		t.Logf("DIFFERENTIAL link helper caller: %d rows equal (%d narrowed reads)", len(narrowed), rewritten)
	}
	for _, site := range linkHelperCallerSites {
		if reached[site.label] == 0 {
			t.Errorf("%s: no statement was sent, the comparison did not happen", site.label)
		}
		t.Logf("DIFFERENTIAL %s: %d statements compared", site.label, reached[site.label])
	}
	if compared == 0 {
		t.Fatal("no statement read the narrowed pull request rows")
	}
	return callers
}

// linkHelperCallersCost runs each link helper caller as served, and its
// whole-table reference form, at the larger size, and holds the served form
// under the caps production applies: its own max_memory_usage or the API
// statement default, and the client's max_bytes_to_read.
func linkHelperCallersCost(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, direct clickhousedriver.Conn, callers []linkHelperCaller) {
	if len(callers) == 0 {
		t.Fatal("no link helper caller was captured at the smaller size")
	}
	var workItems, links uint64
	if err := direct.QueryRow(ctx, `SELECT (SELECT count() FROM work_items), (SELECT count() FROM work_graph_issue_pr)`).Scan(&workItems, &links); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	t.Logf("CALLER stage: work_items %d rows, work_graph_issue_pr %d rows", workItems, links)
	run := func(statement capturedStatement) (memory, readBytes, readRows, scalarMisses, scalarHits uint64, code int32, err error) {
		mark := serverNowMicros(t, ctx, direct)
		rows, err := query.Query(ctx, statement.statement, statement.bindings)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			_ = rows.Close()
		}
		if flushErr := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); flushErr != nil {
			t.Fatalf("flush logs: %v", flushErr)
		}
		if scanErr := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_bytes), max(read_rows), max(ProfileEvents['ScalarSubqueriesCacheMiss']), max(ProfileEvents['ScalarSubqueriesLocalCacheHit'] + ProfileEvents['ScalarSubqueriesGlobalCacheHit']), max(exception_code) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND position(query, 'git_pull_requests') > 0`, mark).Scan(&memory, &readBytes, &readRows, &scalarMisses, &scalarHits, &code); scanErr != nil {
			t.Fatalf("read query_log: %v", scanErr)
		}
		return memory, readBytes, readRows, scalarMisses, scalarHits, code, err
	}
	for _, caller := range callers {
		memoryCap := contextpacket.APIStatementMaxMemoryUsage
		if at := strings.Index(caller.served.statement, "max_memory_usage = "); at >= 0 {
			if _, scanErr := fmt.Sscanf(caller.served.statement[at:], "max_memory_usage = %d", &memoryCap); scanErr != nil {
				t.Fatalf("%s: unreadable max_memory_usage: %v", caller.label, scanErr)
			}
		}
		readCap := uint64(config.DefaultClickHouseMaxBytesToRead)
		memory, readBytes, readRows, misses, hits, code, err := run(caller.served)
		wholeMemory, wholeRead, wholeRows, _, _, wholeCode, wholeErr := run(capturedStatement{statement: caller.whole, bindings: caller.served.bindings})
		t.Logf("CALLER %s served: memory=%d read_bytes=%d read_rows=%d scalar_misses=%d scalar_hits=%d code=%d; whole-table form: memory=%d read_bytes=%d read_rows=%d code=%d (caps: memory %d, read %d)", caller.label, memory, readBytes, readRows, misses, hits, code, wholeMemory, wholeRead, wholeRows, wholeCode, memoryCap, readCap)
		// The project roll-up's candidate ids are one scalar subquery,
		// referenced five times: it must be built exactly once (one cache
		// miss; zero means the measurement did not happen).
		if caller.label == "project linked pull requests" && misses != 1 {
			t.Errorf("%s: scalar subquery executed %d times (reused %d), want once", caller.label, misses, hits)
		}
		if wholeErr != nil {
			t.Logf("CALLER %s whole-table form failed: %v", caller.label, wholeErr)
		}
		if err != nil || code != 0 {
			t.Errorf("%s: the served statement failed at the larger size (code %d): %v", caller.label, code, err)
		}
		if memory == 0 || memory >= memoryCap {
			t.Errorf("%s: served peak memory %d, want above 0 and under %d", caller.label, memory, memoryCap)
		}
		if readBytes >= readCap {
			t.Errorf("%s: served read %d bytes, want under %d", caller.label, readBytes, readCap)
		}
	}
}
