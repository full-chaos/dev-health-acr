package devhealthsource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// catchUpKey is a repository id no other table of a test uses: the table
// number and the row number are both in it.
func catchUpKey(table, n int) string {
	return fmt.Sprintf("00000000-0000-4000-80%02d-%012d", table, n)
}

func catchUpRows(table, count int, from time.Time, step time.Duration) *liveKeysetRows {
	rows := &liveKeysetRows{}
	for n := 1; n <= count; n++ {
		rows.rows = append(rows.rows, keysetRow{at: from.Add(time.Duration(n) * step), key: catchUpKey(table, n)})
	}
	return rows
}

func namedRepositoryTable(name string, store *liveKeysetRows) entityTable {
	table := repositoryKeysetTable(store)
	table.name = name
	return table
}

func entityKeys(batch contextfabric.ProjectionBatch) map[string]bool {
	keys := map[string]bool{}
	for _, e := range batch.Entities {
		keys[strings.TrimPrefix(e.Subject.CanonicalID, "repository:")] = true
	}
	return keys
}

func logLinesWith(t *testing.T, raw, message string) (out []map[string]any) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(line), &decoded) == nil && decoded["msg"] == message {
			out = append(out, decoded)
		}
	}
	return out
}

func stringsOf(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// catchUpPlan is a from-zero plan over three tables: 400 old rows that the
// walk pages through, a table of exactly the snapshot cap whose rows are
// stamped "now" (the walk reaches them last), and a table one row over the
// cap, also stamped "now".
func catchUpPlan(t *testing.T, now time.Time, logs *bytes.Buffer) (sourcePlan, map[string]*liveKeysetRows) {
	t.Helper()
	stores := map[string]*liveKeysetRows{
		"history":  catchUpRows(1, 400, now.Add(-90*24*time.Hour), time.Hour),
		"complete": catchUpRows(2, snapshotPerQueryCap, now.Add(-10*time.Minute), time.Second),
		"over_cap": catchUpRows(3, snapshotPerQueryCap+1, now.Add(-10*time.Minute), time.Second),
	}
	plan := sourcePlan{
		client: keysetRows{}, source: "catch_up_test", version: ClickHouseSourceVersion,
		tables: []entityTable{
			namedRepositoryTable("history", stores["history"]),
			namedRepositoryTable("complete", stores["complete"]),
			namedRepositoryTable("over_cap", stores["over_cap"]),
		},
		now: func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
	}
	if logs != nil {
		plan.logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	return plan, stores
}

// The first batch of a from-zero walk carries the first page AND every row of
// a table the from-zero read returned whole. A table one row over the cap is
// not whole: none of its rows goes early. The cursor is the page's.
func TestFromZeroWalkEmitsCompleteTablesWithItsFirstBatch(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	plan, stores := catchUpPlan(t, now, &logs)
	batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source})
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the first batch", available, err)
	}
	got := entityKeys(batch)
	page, complete, overCap := 0, 0, 0
	for _, row := range stores["history"].rows[:incrementalBatchCap] {
		if got[row.key] {
			page++
		}
	}
	for _, row := range stores["complete"].rows {
		if got[row.key] {
			complete++
		}
	}
	for _, row := range stores["over_cap"].rows {
		if got[row.key] {
			overCap++
		}
	}
	if page != incrementalBatchCap || complete != snapshotPerQueryCap || overCap != 0 || len(batch.Entities) != incrementalBatchCap+snapshotPerQueryCap {
		t.Fatalf("first batch: %d of the page's %d rows, %d of the complete table's %d, %d of the truncated table's rows, %d entities; want the page, the complete table, nothing else",
			page, incrementalBatchCap, complete, snapshotPerQueryCap, overCap, len(batch.Entities))
	}
	next, err := decodeCursor(batch.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	last := stores["history"].rows[incrementalBatchCap-1]
	if !next.Since.Equal(last.at) || next.After != last.key {
		t.Fatalf("next cursor = (%s, %s), want the page's last row (%s, %s): the early rows must not move it", next.Since, next.After, last.at, last.key)
	}
	lines := logLinesWith(t, logs.String(), completeTablesMessage)
	if len(lines) != 1 || lines[0]["level"] != "INFO" || !reflect.DeepEqual(stringsOf(lines[0]["tables_emitted"]), []string{"complete"}) ||
		!reflect.DeepEqual(stringsOf(lines[0]["tables_left_truncated"]), []string{"history", "over_cap"}) ||
		len(stringsOf(lines[0]["tables_left_to_the_walk"])) != 0 || lines[0]["rows_emitted"].(float64) != snapshotPerQueryCap || lines[0]["fallback"] != "" {
		t.Fatalf("complete-tables lines = %v, want one INFO line: emitted [complete], truncated [history over_cap], %d rows", lines, snapshotPerQueryCap)
	}

	// The second call is an ordinary page: nothing goes early again.
	logs.Reset()
	second, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source, Cursor: batch.NextCursor})
	if err != nil || !available {
		t.Fatalf("second call: available=%v err=%v", available, err)
	}
	if len(second.Entities) != incrementalBatchCap || len(logLinesWith(t, logs.String(), completeTablesMessage)) != 0 {
		t.Fatalf("second batch has %d entities and %d complete-tables lines, want one page of %d and none", len(second.Entities), len(logLinesWith(t, logs.String(), completeTablesMessage)), incrementalBatchCap)
	}
	for _, row := range stores["complete"].rows {
		if entityKeys(second)[row.key] {
			t.Fatalf("the second batch carries a row of the complete table before the walk reached it")
		}
	}
}

// The walk still emits every row of the complete table when it reaches it,
// and the first batch is the same batch when it is built again (a first batch
// whose apply failed is retried from the same empty cursor).
func TestFromZeroWalkRepeatsItsFirstBatchAndStillWalksTheCompleteTable(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	plan, stores := catchUpPlan(t, now, nil)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	first, _, err := plan.nextBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := plan.nextBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if again.BatchID != first.BatchID || again.NextCursor != first.NextCursor || !reflect.DeepEqual(entityKeys(again), entityKeys(first)) {
		t.Fatalf("the first batch built twice differs: ids %q/%q, %d/%d entities", first.BatchID, again.BatchID, len(first.Entities), len(again.Entities))
	}
	emitted := map[string]int{}
	cursor := ""
	for calls := 0; calls < 50; calls++ {
		batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !available {
			break
		}
		for key := range entityKeys(batch) {
			emitted[key]++
		}
		cursor = batch.NextCursor
	}
	for name, store := range stores {
		want := 1
		if name == "complete" {
			want = 2 // once with the first batch, once when the walk reaches the row
		}
		for _, row := range store.rows {
			if emitted[row.key] != want {
				t.Fatalf("table %s row %s was emitted %d times over the whole walk, want %d", name, row.key, emitted[row.key], want)
			}
		}
	}
}

// Only a from-zero read knows which tables are complete. A walk that resumes
// from a saved cursor emits pages only.
func TestAWalkFromASavedCursorEmitsNoTableEarly(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	plan, stores := catchUpPlan(t, now, &logs)
	saved, err := encodeCursor(cursorState{Since: stores["history"].rows[9].at, After: stores["history"].rows[9].key})
	if err != nil {
		t.Fatal(err)
	}
	batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source, Cursor: saved})
	if err != nil || !available {
		t.Fatalf("available=%v err=%v", available, err)
	}
	got := entityKeys(batch)
	if len(batch.Entities) != incrementalBatchCap || got[stores["complete"].rows[0].key] || len(logLinesWith(t, logs.String(), completeTablesMessage)) != 0 {
		t.Fatalf("a walk from a saved cursor built a batch of %d entities (complete table row in it: %v, complete-tables lines: %d), want one page and nothing early",
			len(batch.Entities), got[stores["complete"].rows[0].key], len(logLinesWith(t, logs.String(), completeTablesMessage)))
	}
}

func entityRows(table string, keys ...string) []candidate {
	out := make([]candidate, 0, len(keys))
	for i, key := range keys {
		entity := contractsv1.ContextFabricEntityProjection{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + key, Label: key}}
		out = append(out, candidate{table: table, observedAt: pageCutStamp.Add(time.Duration(i) * time.Second), sortKey: key, entity: &entity})
	}
	return out
}

// Whole tables only, in table order, never a row the page carries, and never
// past the contract's batch bounds: a table that does not fit is left whole,
// and a later table that fits still goes.
func TestSelectCompleteTables(t *testing.T) {
	page := entityRows("history", "h1", "h2")
	shared := entityRows("small", "s1", "s2", "s3")
	pageWithShared := append(append([]candidate{}, page...), shared[1])
	// The page holds 3 entities and the small table adds 2: 995 more fill the
	// batch to its bound of 1,000 exactly, 996 are one over.
	big := make([]string, contractsv1.ContextFabricProjectionBatchMaxEntities-5)
	for i := range big {
		big[i] = fmt.Sprintf("b%04d", i)
	}
	fits := entityRows("fits", big...)
	tooMany := entityRows("too_many", append(append([]string{}, big...), "b-extra")...)
	tail := entityRows("tail", "t1")
	for _, tc := range []struct {
		name          string
		page          []candidate
		complete      []completeTable
		wantRows      int
		wantEmitted   []string
		wantLeft      []string
		absent, there string
	}{
		{name: "no complete table", page: page},
		{name: "a row the page carries is not doubled", page: pageWithShared, complete: []completeTable{{"small", shared}}, wantRows: 2, wantEmitted: []string{"small"}, absent: "s2", there: "s3"},
		{name: "a table that fills the batch exactly goes", page: pageWithShared, complete: []completeTable{{"small", shared}, {"fits", fits}}, wantRows: 2 + len(fits), wantEmitted: []string{"small", "fits"}},
		{name: "a table one row over the bound is left whole, a later one still goes", page: pageWithShared, complete: []completeTable{{"small", shared}, {"too_many", tooMany}, {"tail", tail}}, wantRows: 3, wantEmitted: []string{"small", "tail"}, wantLeft: []string{"too_many"}, absent: "b0000", there: "t1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, emitted, left := selectCompleteTables(tc.page, tc.complete)
			if len(rows) != tc.wantRows || !reflect.DeepEqual(emitted, tc.wantEmitted) || !reflect.DeepEqual(left, tc.wantLeft) {
				t.Fatalf("selected %d rows, emitted %v, left %v; want %d, %v, %v", len(rows), emitted, left, tc.wantRows, tc.wantEmitted, tc.wantLeft)
			}
			keys := map[string]bool{}
			for _, c := range rows {
				keys[c.sortKey] = true
			}
			if (tc.absent != "" && keys[tc.absent]) || (tc.there != "" && !keys[tc.there]) {
				t.Fatalf("selection holds %q=%v and %q=%v, want absent and present", tc.absent, keys[tc.absent], tc.there, keys[tc.there])
			}
			if e, _, _ := candidateCounts(append(append([]candidate{}, tc.page...), rows...)); e > contractsv1.ContextFabricProjectionBatchMaxEntities {
				t.Fatalf("page and selection hold %d entities, over the batch bound", e)
			}
		})
	}
}

// A complete table whose rows make the batch invalid beside the page (here:
// a subject the page also carries, from another row) is left to the walk.
// The page goes alone, with a warning; the walk never stops on the tables.
func TestCompleteTablesThatInvalidateTheBatchAreLeftToTheWalk(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	history := catchUpRows(1, 400, now.Add(-90*24*time.Hour), time.Hour)
	// The same repository id as the page's first row, in another table and on
	// another stamp: another row, the same subject.
	clash := &liveKeysetRows{rows: keysetRows{{at: now.Add(-time.Minute), key: history.rows[0].key}, {at: now.Add(-time.Minute), key: catchUpKey(2, 1)}}}
	plan := sourcePlan{
		client: keysetRows{}, source: "catch_up_test", version: ClickHouseSourceVersion,
		tables: []entityTable{namedRepositoryTable("history", history), namedRepositoryTable("clash", clash)},
		now:    func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source})
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the page as a batch", available, err)
	}
	if len(batch.Entities) != incrementalBatchCap || entityKeys(batch)[catchUpKey(2, 1)] {
		t.Fatalf("the batch has %d entities (a row of the clashing table in it: %v), want the page alone", len(batch.Entities), entityKeys(batch)[catchUpKey(2, 1)])
	}
	lines := logLinesWith(t, logs.String(), completeTablesMessage)
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["fallback"] != "batch_invalid_with_tables" ||
		!reflect.DeepEqual(stringsOf(lines[0]["tables_left_to_the_walk"]), []string{"clash"}) || len(stringsOf(lines[0]["tables_emitted"])) != 0 {
		t.Fatalf("complete-tables lines = %v, want one WARN line: fallback, [clash] left to the walk", lines)
	}
}

// A catch-up pass opens with the first page a call publishes, with the
// source clock as its edge. It ends when a page ends at or past the edge, or
// when the read finds nothing beyond the cursor. The report gives the cursor's
// stamp and the open pass, per scope.
func TestCatchUpPassAndReport(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	memo := newWindowMemo()
	scope := windowScopeFor("org", 3)
	if _, done := memo.endCatchUp(scope); done {
		t.Fatal("a scope with no pass ended one")
	}
	if _, done := memo.advanceCatchUp(scope, now, now.Add(-time.Hour), 200); done {
		t.Fatal("a page that ends before the edge ended the pass")
	}
	if _, done := memo.advanceCatchUp(scope, now.Add(time.Minute), now.Add(-time.Nanosecond), 150); done {
		t.Fatal("a page that ends one nanosecond before the edge ended the pass")
	}
	if pass := memo.catchUp(scope); !pass.open || !pass.edge.Equal(now) || pass.pages != 2 || pass.rows != 350 {
		t.Fatalf("open pass = %+v, want edge at the first page's clock, 2 pages, 350 rows", pass)
	}
	cursor := func(state cursorState) string {
		encoded, err := encodeCursorIn(state.Space, state)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	stamp := now.Add(-3 * time.Hour)
	for _, tc := range []struct {
		name       string
		checkpoint contextfabric.ProjectionCheckpoint
		want       contextfabric.ProjectionCatchUp
	}{
		{"the scope's pass and a cursor in the ingest space", contextfabric.ProjectionCheckpoint{OrgID: " org ", Epoch: 3, Cursor: cursor(cursorState{Since: stamp, After: "k", Space: cursorSpaceIngest})},
			contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: stamp, PassOpen: true, PassEdge: now}},
		{"a cursor in the ingest-columns space", contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 3, Cursor: cursor(cursorState{Since: stamp, Space: cursorSpaceIngestColumns})},
			contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: stamp, PassOpen: true, PassEdge: now}},
		{"no cursor yet", contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 3}, contextfabric.ProjectionCatchUp{PassOpen: true, PassEdge: now}},
		{"a cursor of no known space", contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 3, Cursor: cursor(cursorState{Since: stamp})}, contextfabric.ProjectionCatchUp{PassOpen: true, PassEdge: now}},
		{"a cursor in the ingest space with no stamp", contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 3, Cursor: cursor(cursorState{After: "k", Space: cursorSpaceIngest})}, contextfabric.ProjectionCatchUp{PassOpen: true, PassEdge: now}},
		{"a cursor that does not decode", contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 3, Cursor: "%%%"}, contextfabric.ProjectionCatchUp{PassOpen: true, PassEdge: now}},
		{"another epoch has no pass", contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 4, Cursor: cursor(cursorState{Since: stamp, Space: cursorSpaceIngest})}, contextfabric.ProjectionCatchUp{CursorKnown: true, CursorAt: stamp}},
	} {
		if got := reportCatchUp(memo, tc.checkpoint); got != tc.want {
			t.Errorf("%s: report = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	// A copy of the memo (a peek) cannot move this pass.
	memo.snapshot().advanceCatchUp(scope, now.Add(time.Hour), now.Add(time.Hour), 1000)
	ended, done := memo.advanceCatchUp(scope, now.Add(2*time.Minute), now, 50)
	if !done || ended.pages != 3 || ended.rows != 400 || !ended.edge.Equal(now) {
		t.Fatalf("a page that ends on the edge: done=%v pass=%+v, want the pass ended with 3 pages and 400 rows", done, ended)
	}
	if pass := memo.catchUp(scope); pass.open {
		t.Fatalf("the pass is still open after it ended: %+v", pass)
	}
	// The next page opens a new pass with a new edge; a caught-up read ends it.
	later := now.Add(time.Hour)
	if _, done := memo.advanceCatchUp(scope, later, now, 10); done {
		t.Fatal("the first page of a new pass ended it")
	}
	if ended, done := memo.endCatchUp(scope); !done || !ended.edge.Equal(later) || ended.pages != 1 || ended.rows != 10 {
		t.Fatalf("caught up: done=%v pass=%+v, want the new pass (edge at its own start, 1 page, 10 rows)", done, ended)
	}
	var nilMemo *windowMemo
	if _, done := nilMemo.advanceCatchUp(scope, now, now, 1); done || nilMemo.catchUp(scope).open {
		t.Fatal("a source without a memo reports a pass")
	}
}

// The walk writes one line when a catch-up pass ends: here it ends because
// the read is caught up, after three pages.
func TestCatchUpPassEndedLine(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	plan, _ := catchUpPlan(t, now, &logs)
	cursor, batches := "", 0
	for calls := 0; calls < 50; calls++ {
		batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !available {
			break
		}
		batches++
		cursor = batch.NextCursor
	}
	lines := logLinesWith(t, logs.String(), catchUpEndedMessage)
	rows := 400 + snapshotPerQueryCap + snapshotPerQueryCap + 1
	if len(lines) != 1 || lines[0]["end_reason"] != catchUpEndNothingAhead || lines[0]["pass_pages"].(float64) != float64(batches) ||
		lines[0]["pass_rows"].(float64) != float64(rows) || lines[0]["pass_edge"] != now.Format(time.RFC3339Nano) || lines[0]["pass_seconds"].(float64) != 0 {
		t.Fatalf("catch-up ended lines = %v, want one: caught_up, %d pages, %d rows, edge at the walk's start", lines, batches, rows)
	}
	if report := reportCatchUp(plan.window, contextfabric.ProjectionCheckpoint{OrgID: "org", Cursor: cursor}); report.PassOpen || !report.CursorKnown {
		t.Fatalf("after the walk: report = %+v, want no open pass and a known cursor", report)
	}
}

// Both production sources report from their own memo; a nil source reports
// nothing.
func TestBothSourcesReportTheirOwnCatchUp(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 2}
	clickhouse, err := NewClickHouseProjectionSource(keysetRows{})
	if err != nil {
		t.Fatal(err)
	}
	teams, err := NewTeamsProjectsSource(keysetRows{}, true)
	if err != nil {
		t.Fatal(err)
	}
	clickhouse.window.advanceCatchUp(windowScopeFor("org", 2), now, now.Add(-time.Hour), 1)
	if got := clickhouse.ProjectionCatchUp(checkpoint); !got.PassOpen || !got.PassEdge.Equal(now) {
		t.Errorf("clickhouse: report = %+v, want its open pass", got)
	}
	if got := teams.ProjectionCatchUp(checkpoint); got.PassOpen {
		t.Errorf("teams_projects reports the clickhouse source's pass: %+v", got)
	}
	teams.window.advanceCatchUp(windowScopeFor("org", 2), now.Add(time.Minute), now.Add(-time.Hour), 1)
	if got := teams.ProjectionCatchUp(checkpoint); !got.PassOpen || !got.PassEdge.Equal(now.Add(time.Minute)) {
		t.Errorf("teams_projects: report = %+v, want its own open pass", got)
	}
	var none *ClickHouseProjectionSource
	var noTeams *TeamsProjectsSource
	if none.ProjectionCatchUp(checkpoint) != (contextfabric.ProjectionCatchUp{}) || noTeams.ProjectionCatchUp(checkpoint) != (contextfabric.ProjectionCatchUp{}) {
		t.Error("a nil source reports a catch-up")
	}
}

// progressRows is a table whose rows carry nothing publishable: keysetTable
// scans them into progress markers, and the walk moves past them.
func progressRows(count int, from time.Time, step time.Duration) keysetRows {
	rows := make(keysetRows, 0, count)
	for n := 1; n <= count; n++ {
		rows = append(rows, keysetRow{at: from.Add(time.Duration(n) * step), key: fmt.Sprintf("h%04d", n)})
	}
	return rows
}

// The first page of the walk can carry nothing publishable. The complete
// table still goes with the first batch, and that batch moves the cursor past
// the page.
func TestCompleteTablesGoWithAFirstPageThatCarriesNothing(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	history := progressRows(400, now.Add(-90*24*time.Hour), time.Hour)
	complete := catchUpRows(2, 3, now.Add(-10*time.Minute), time.Second)
	plan := sourcePlan{
		client: keysetRows{}, source: "catch_up_test", version: ClickHouseSourceVersion,
		tables: []entityTable{keysetTable("history", history), namedRepositoryTable("complete", complete)},
		now:    func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
	}
	batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source})
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want a batch with the complete table", available, err)
	}
	if got := entityKeys(batch); len(batch.Entities) != 3 || !got[complete.rows[0].key] || !got[complete.rows[2].key] {
		t.Fatalf("the batch has %d entities, want the 3 rows of the complete table", len(batch.Entities))
	}
	next, err := decodeCursor(batch.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if last := history[incrementalBatchCap-1]; !next.Since.Equal(last.at) || next.After != last.key {
		t.Fatalf("next cursor = (%s, %s), want the first page's last row (%s, %s)", next.Since, next.After, last.at, last.key)
	}
}

// The complete tables are judged once. When neither the first page nor the
// tables carry anything publishable the walk moves on, and the tables' rows
// are not judged again with every page it skips.
func TestCompleteTablesAreJudgedOnce(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	history := progressRows(400, now.Add(-90*24*time.Hour), time.Hour)
	// Three rows whose entity the contract refuses (no label).
	invalid := &liveKeysetRows{rows: keysetRows{{at: now, key: "bad1"}, {at: now, key: "bad2"}, {at: now, key: "bad3"}}}
	refused := entityTable{name: "refused", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, invalid, "", rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
			var at time.Time
			var key string
			if err := r.Scan(&at, &key); err != nil {
				return nil, err
			}
			entity := contractsv1.ContextFabricEntityProjection{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + key}, ObservedAt: at, SourceVersion: ClickHouseSourceVersion}
			return []candidate{{observedAt: at, sortKey: key, entity: &entity}}, nil
		})
	}}
	quarantined := 0
	plan := sourcePlan{
		client: keysetRows{}, source: "catch_up_test", version: ClickHouseSourceVersion,
		tables: []entityTable{keysetTable("history", history), refused},
		now:    func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
		observeQuarantine: func(quarantineObservation) { quarantined++ },
	}
	if _, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}); err != nil || available {
		t.Fatalf("available=%v err=%v, want no batch: nothing in the source is publishable", available, err)
	}
	// Once with the first page as a complete table, once where the walk
	// reaches the three rows: 6. A second judgement with every skipped page
	// would add 3 per page.
	if quarantined != 6 {
		t.Fatalf("the three refused rows were judged %d times, want 6 (once early, once in the walk)", quarantined)
	}
}

// A from-zero read that returned no table whole says so: one line, no table
// emitted, every table named as truncated.
func TestFromZeroWalkWithNoCompleteTableSaysSo(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	plan := sourcePlan{
		client: keysetRows{}, source: "catch_up_test", version: ClickHouseSourceVersion,
		tables: []entityTable{namedRepositoryTable("history", catchUpRows(1, 400, now.Add(-90*24*time.Hour), time.Hour))},
		now:    func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	batch, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source})
	if err != nil || !available || len(batch.Entities) != incrementalBatchCap {
		t.Fatalf("available=%v err=%v entities=%d, want the first page", available, err, len(batch.Entities))
	}
	lines := logLinesWith(t, logs.String(), completeTablesMessage)
	if len(lines) != 1 || lines[0]["level"] != "INFO" || len(stringsOf(lines[0]["tables_emitted"])) != 0 ||
		!reflect.DeepEqual(stringsOf(lines[0]["tables_left_truncated"]), []string{"history"}) || lines[0]["rows_emitted"].(float64) != 0 {
		t.Fatalf("complete-tables lines = %v, want one INFO line: nothing emitted, [history] truncated", lines)
	}
}

// What the complete tables add is a copy, made early, of what the walk emits
// anyway. Against the same walk without them: the first batch has the same
// page and the same next cursor, plus the tables' rows; every later batch is
// the same batch; and every early row comes again, unchanged, where the walk
// reaches it. So whatever a backend holds after the walk alone, it holds after
// this walk too.
func TestEarlyRowsAreACopyOfWhatTheWalkEmits(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	drain := func(first func(sourcePlan) (contextfabric.ProjectionBatch, bool, error)) []contextfabric.ProjectionBatch {
		t.Helper()
		plan, _ := catchUpPlan(t, now, nil)
		var batches []contextfabric.ProjectionBatch
		batch, available, err := first(plan)
		for calls := 0; available && calls < 50; calls++ {
			if err != nil {
				t.Fatal(err)
			}
			batches = append(batches, batch)
			batch, available, err = plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source, Cursor: batch.NextCursor})
		}
		if err != nil {
			t.Fatal(err)
		}
		return batches
	}
	with := drain(func(p sourcePlan) (contextfabric.ProjectionBatch, bool, error) {
		return p.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: p.source})
	})
	// The same walk from zero with no table handed to it.
	alone := drain(func(p sourcePlan) (contextfabric.ProjectionBatch, bool, error) {
		p.windowScope = windowScopeFor("org", 0)
		return p.pagedBatch(context.Background(), "org", "", cursorState{}, true)
	})
	if len(with) != len(alone) || len(with) < 3 {
		t.Fatalf("the walk built %d batches with the complete tables and %d without, want the same number (3 or more)", len(with), len(alone))
	}
	// The clock is pinned, so two builds of one batch are equal value for value.
	if with[0].BatchID != alone[0].BatchID || with[0].NextCursor != alone[0].NextCursor {
		t.Fatalf("first batch: id %q next %q with the tables, id %q next %q without", with[0].BatchID, with[0].NextCursor, alone[0].BatchID, alone[0].NextCursor)
	}
	page := len(alone[0].Entities)
	if !reflect.DeepEqual(with[0].Entities[:page], alone[0].Entities) {
		t.Fatal("the page part of the first batch differs from the walk's own first batch")
	}
	early := with[0].Entities[page:]
	if len(early) != snapshotPerQueryCap {
		t.Fatalf("the first batch adds %d rows to the page, want the complete table's %d", len(early), snapshotPerQueryCap)
	}
	for i := 1; i < len(with); i++ {
		if !reflect.DeepEqual(with[i], alone[i]) {
			t.Fatalf("batch %d differs between the walk with the complete tables and the walk without", i+1)
		}
	}
	later := map[string]contractsv1.ContextFabricEntityProjection{}
	for _, batch := range with[1:] {
		for _, e := range batch.Entities {
			later[e.Subject.CanonicalID] = e
		}
	}
	for _, e := range early {
		again, ok := later[e.Subject.CanonicalID]
		if !ok || !reflect.DeepEqual(again, e) {
			t.Fatalf("early row %s: emitted again by the walk=%v, unchanged=%v", e.Subject.CanonicalID, ok, reflect.DeepEqual(again, e))
		}
	}
}
