package devhealthsource

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func edgeRow(at time.Time, key string) candidate { return candidate{observedAt: at, sortKey: key} }

// The upper edge is a keyset position: (position, row key), compared in the
// order the readers page in.
func TestWithinEdgeCutsAtTheKeysetPosition(t *testing.T) {
	at := pageCutStamp
	edge := cursorState{Since: at, After: "m"}
	for _, tc := range []struct {
		name string
		row  candidate
		past bool
	}{
		{"an earlier position, a later key", edgeRow(at.Add(-time.Microsecond), "z"), false},
		{"the edge position, an earlier key", edgeRow(at, "a"), false},
		{"the edge row itself", edgeRow(at, "m"), false},
		{"the edge position, a later key", edgeRow(at, "n"), true},
		{"a later position, an earlier key", edgeRow(at.Add(time.Microsecond), "a"), true},
	} {
		if got := pastEdge(tc.row, edge); got != tc.past {
			t.Errorf("%s: pastEdge = %v, want %v", tc.name, got, tc.past)
		}
	}
	page := []candidate{edgeRow(at.Add(-time.Second), "z"), edgeRow(at, "a"), edgeRow(at, "m"), edgeRow(at, "n"), edgeRow(at.Add(time.Second), "a")}
	inside, edged := withinEdge(page, edge)
	if len(inside) != 3 || !edged {
		t.Fatalf("withinEdge kept %d rows (edged=%v), want the 3 rows at or before the edge and edged", len(inside), edged)
	}
	if inside, edged := withinEdge(page[:3], edge); len(inside) != 3 || edged {
		t.Fatalf("a page that ends on the edge row: kept %d (edged=%v), want 3 and not edged", len(inside), edged)
	}
	if inside, edged := withinEdge(page[3:], edge); len(inside) != 0 || !edged {
		t.Fatalf("a page wholly past the edge: kept %d (edged=%v), want 0 and edged", len(inside), edged)
	}
}

func repositoryKeysetTable(store contextpacket.ClickHouseQueryClient) entityTable {
	return entityTable{name: "repos", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, store, "", rowLimitBindings(orgID, cursor, limit), limit, scanRepositoryKeysetRow)
	}}
}

// The pass ends at the frontier it started at. Rows past it (a later key on
// the frontier's position, or a later position) are the paged read's: the
// walk neither emits them nor waits for them.
func TestOverlapWalkLeavesRowsPastTheFrontierToThePagedRead(t *testing.T) {
	at := pageCutStamp
	key := func(n int) string { return "00000000-0000-4000-8000-00000000000" + string(rune('0'+n)) }
	store := &liveKeysetRows{rows: keysetRows{
		{at: at.Add(-time.Minute), key: key(1)}, // inside
		{at: at, key: key(2)},                   // inside: the frontier's position, an earlier key
		{at: at, key: key(3)},                   // the frontier row
		{at: at, key: key(4)},                   // past: the frontier's position, a later key
		{at: at.Add(time.Second), key: key(1)},  // past: a later position, an earlier key
	}}
	now := at.Add(time.Minute)
	plan := sourcePlan{
		client: keysetRows{}, source: "overlap_edge_test", version: ClickHouseSourceVersion,
		tables: []entityTable{repositoryKeysetTable(store)},
		now:    func() time.Time { return now }, overlap: 15 * time.Minute, window: newWindowMemo(), windowScope: windowScopeFor("org", 0),
	}
	frontier := cursorState{Since: at, After: key(3)}
	cursor, err := encodeCursor(frontier)
	if err != nil {
		t.Fatal(err)
	}
	batch, available, err := plan.overlapBatch(context.Background(), "org", cursor, frontier)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the window batch", available, err)
	}
	var got []string
	for _, e := range batch.Entities {
		got = append(got, strings.TrimPrefix(e.Subject.CanonicalID, "repository:")+"@"+e.ObservedAt.Format(time.RFC3339Nano))
	}
	want := []string{key(1) + "@" + at.Add(-time.Minute).Format(time.RFC3339Nano), key(2) + "@" + at.Format(time.RFC3339Nano), key(3) + "@" + at.Format(time.RFC3339Nano)}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the window batch carries\n  %v\nwant exactly the rows at or before the frontier\n  %v", got, want)
	}
	// Applied: the next call settles the batch, finds nothing left inside the
	// window and ends the pass, with the rows past the frontier still there.
	next, err := decodeCursor(batch.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	plan.window.settle(plan.windowScope, next.Ack)
	if _, available, err := plan.overlapBatch(context.Background(), "org", batch.NextCursor, frontier); err != nil || available {
		t.Fatalf("second call: available=%v err=%v, want no batch", available, err)
	}
	if pass := plan.window.pass(plan.windowScope); pass.walking {
		t.Fatalf("the pass is still open after the window was walked to the frontier (walk at %s/%s)", pass.walk.Since.Format(time.RFC3339Nano), pass.walk.After)
	}
}

func passLines(t *testing.T, raw string) (out []map[string]any) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(line), &decoded) != nil {
			continue
		}
		switch decoded["msg"] {
		case openPassMessage, overduePassMessage, endedPassMessage:
			out = append(out, decoded)
		}
	}
	return out
}

// One line per tick while a pass is open, a warning only once the pass is
// older than one overlap, one line when it ends; and the pass report the
// coordinator reads says the same at every tick.
func TestOpenPassIsReportedAndLoggedUntilItEnds(t *testing.T) {
	const overlap = time.Minute
	r := newChurnRig(t, churnStart)
	r.plan.overlap, r.plan.windowPagesPerCall = overlap, 1
	// Seven pages inside the two-minute window: at one page per tick the first
	// pass is open at the end of six ticks, 0 to 75 seconds old.
	r.land(1400, churnStart.Add(-100*time.Second), churnStart.Add(-10*time.Second))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org"}
	report := func() contextfabric.ProjectionWindowPass {
		return reportWindowPass(r.plan.window, r.plan.overlap, r.now, checkpoint)
	}
	if got := report(); got != (contextfabric.ProjectionWindowPass{}) {
		t.Fatalf("report before any pass = %+v, want the zero value", got)
	}
	type tickView struct {
		level, msg string
		age        float64
		line       map[string]any
		report     contextfabric.ProjectionWindowPass
	}
	var ticks []tickView
	var ended []map[string]any
	for tick := 0; tick < 7; tick++ {
		if tick > 0 {
			r.advance()
		}
		r.runTick()
		lines := passLines(t, r.logs.String())
		view := tickView{report: report()}
		for _, line := range lines {
			if line["msg"] == endedPassMessage {
				ended = append(ended, line)
				continue
			}
			if view.msg != "" {
				t.Fatalf("tick %d wrote more than one open-pass line: %v", tick+1, lines)
			}
			view.level, view.msg = line["level"].(string), line["msg"].(string)
			view.age, view.line = line["pass_age_seconds"].(float64), line
			for _, field := range []string{"source", "org_id", "window_low", "window_high", "resume_after", "frontier", "pages_this_call", "pass_bound_seconds", "pass_pages", "pass_rows", "remaining_span_seconds", "remaining_rows_estimate"} {
				if _, ok := line[field]; !ok {
					t.Fatalf("tick %d: the open-pass line has no %q field: %v", tick+1, field, line)
				}
			}
		}
		ticks = append(ticks, view)
	}
	for i, view := range ticks[:6] {
		age := float64(i * 15)
		wantLevel, wantMsg, wantOverdue := "INFO", openPassMessage, false
		if age > overlap.Seconds() {
			wantLevel, wantMsg, wantOverdue = "WARN", overduePassMessage, true
		}
		if view.level != wantLevel || view.msg != wantMsg || view.age != age {
			t.Errorf("tick %d: line level=%q age=%v msg=%q, want level=%q age=%v msg=%q", i+1, view.level, view.age, view.msg, wantLevel, age, wantMsg)
		}
		want := contextfabric.ProjectionWindowPass{Open: true, StartedAt: churnStart, Age: time.Duration(age) * time.Second, Bound: overlap, Overdue: wantOverdue}
		if view.report != want {
			t.Errorf("tick %d: report = %+v, want %+v", i+1, view.report, want)
		}
	}
	// Tick 1 read one page: 200 of the 1,400 rows, which lie 64.285714 ms
	// apart from 100 s to 10 s before the start. The window runs from 130 s
	// before the start (frontier - 2 x overlap) to the last row, so the walk
	// stands 42.857 s into it with 77.143 s left: 200 x 77.143 / 42.857 = 360.
	for field, want := range map[string]float64{"pages_this_call": 1, "pass_pages": 1, "pass_rows": 200, "pass_bound_seconds": 60, "remaining_span_seconds": 77, "remaining_rows_estimate": 360} {
		if got := ticks[0].line[field]; got != want {
			t.Errorf("tick 1: %s = %v, want %v", field, got, want)
		}
	}
	if ticks[4].report.Overdue || !ticks[5].report.Overdue {
		t.Fatalf("a pass exactly one overlap old is inside its bound and one tick later is not: tick 5 overdue=%v, tick 6 overdue=%v", ticks[4].report.Overdue, ticks[5].report.Overdue)
	}
	// Tick 7 ends the first pass (seven pages) and starts the next one, which
	// stops at the page bound at once: one ended line, and a young open pass.
	if len(ended) != 1 || ended[0]["pass_pages"].(float64) != 7 || ended[0]["pass_rows"].(float64) != 1400 || ended[0]["pass_seconds"].(float64) != 90 {
		t.Fatalf("ended-pass lines = %v, want one line: 7 pages, 1400 rows, 90 seconds", ended)
	}
	if last := ticks[6]; last.level != "INFO" || last.age != 0 || !last.report.Open || last.report.Overdue || !last.report.StartedAt.Equal(r.now) {
		t.Fatalf("tick 7: line level=%q age=%v report=%+v, want a new pass, open and not overdue", last.level, last.age, last.report)
	}
	// The new pass has read nothing yet, and says so: no count is carried
	// over from the pass before it, and there is no estimate to give.
	for field, want := range map[string]float64{"pass_pages": 0, "pass_rows": 0, "remaining_rows_estimate": -1} {
		if got := ticks[6].line[field]; got != want {
			t.Errorf("tick 7: %s = %v, want %v", field, got, want)
		}
	}
	// Another epoch of the organization has its own pass.
	if got := reportWindowPass(r.plan.window, r.plan.overlap, r.now, contextfabric.ProjectionCheckpoint{OrgID: "org", Epoch: 2}); got.Open {
		t.Fatalf("the report for another epoch = %+v, want no pass", got)
	}
}

// A pass that starts and ends inside one call was never reported open, so it
// writes no ended line: the line closes the open-pass lines of a pass that
// spanned calls, and only those.
func TestAPassThatStartsAndEndsInOneCallWritesNoEndedLine(t *testing.T) {
	const overlap = time.Minute
	r := newChurnRig(t, churnStart)
	r.plan.overlap, r.plan.windowPagesPerCall = overlap, 3
	// The first pass's window starts 2 x overlap behind the frontier. 700 rows
	// (3.5 pages) sit in its first five seconds, 10 rows at the frontier.
	frontier := churnStart.Add(-10 * time.Second)
	r.land(700, frontier.Add(-2*overlap), frontier.Add(-2*overlap+5*time.Second))
	r.land(10, frontier.Add(-time.Second), frontier)
	// Tick 1 (10 s after the frontier's stamp): 3 pages, the pass stays open.
	r.runTick()
	if lines := passLines(t, r.logs.String()); len(lines) != 1 || lines[0]["msg"] != openPassMessage {
		t.Fatalf("tick 1 lines = %v, want one open-pass line", lines)
	}
	// Tick 2: page 4 ends the first pass. The next pass starts in the same
	// call with its lower edge at (first pass start - 2 x overlap), which is
	// past the 700 rows: its window is the 10 rows, one page, and it ends in
	// this call too.
	r.advance()
	r.runTick()
	lines := passLines(t, r.logs.String())
	if len(lines) != 1 || lines[0]["msg"] != endedPassMessage || lines[0]["pass_rows"].(float64) != 710 || lines[0]["pass_pages"].(float64) != 4 {
		t.Fatalf("tick 2 lines = %v, want exactly one ended line, for the first pass (4 pages, 710 rows)", lines)
	}
	if pass := r.pass(); pass.walking || pass.pages != 1 || pass.rows != 10 {
		t.Fatalf("after tick 2: walking=%v pages=%d rows=%d, want the second pass ended after 1 page of 10 rows", pass.walking, pass.pages, pass.rows)
	}
}

// One source row can carry several candidates; the pass counts rows.
func TestSourceRowsCountsEachRowOnce(t *testing.T) {
	at := pageCutStamp
	page := []candidate{
		edgeRow(at, "a"), edgeRow(at, "a"), // one row, two candidates
		edgeRow(at, "b"),                  // the same position, another key
		edgeRow(at.Add(time.Second), "b"), // the same key, another position
		edgeRow(at.Add(time.Second), "b"),
	}
	if got := sourceRows(page); got != 3 {
		t.Fatalf("sourceRows = %d, want 3", got)
	}
	if got := sourceRows(nil); got != 0 {
		t.Fatalf("sourceRows(nil) = %d, want 0", got)
	}
}

// Both production sources report the pass of their OWN memo, on their own
// clock and overlap. A nil source reports none.
func TestBothSourcesReportTheirOwnWindowPass(t *testing.T) {
	started := churnStart
	now := started.Add(20 * time.Minute)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: " org ", Epoch: 3}
	open := windowPass{walking: true, passStart: started}
	want := contextfabric.ProjectionWindowPass{Open: true, StartedAt: started, Age: 20 * time.Minute, Bound: 30 * time.Minute}

	clickhouse, err := NewClickHouseProjectionSource(keysetRows{})
	if err != nil {
		t.Fatal(err)
	}
	clickhouse.now, clickhouse.overlap = func() time.Time { return now }, 30*time.Minute
	teams, err := NewTeamsProjectsSource(keysetRows{}, true)
	if err != nil {
		t.Fatal(err)
	}
	teams.now, teams.overlap = func() time.Time { return now }, 30*time.Minute

	for name, source := range map[string]contextfabric.ProjectionWindowReporter{"clickhouse": clickhouse, "teams_projects": teams} {
		if got := source.ProjectionWindowPass(checkpoint); got != (contextfabric.ProjectionWindowPass{}) {
			t.Errorf("%s: report with no pass = %+v, want the zero value", name, got)
		}
	}
	clickhouse.window.setPass(windowScopeFor("org", 3), open)
	if got := clickhouse.ProjectionWindowPass(checkpoint); got != want {
		t.Errorf("clickhouse: report = %+v, want %+v", got, want)
	}
	if got := teams.ProjectionWindowPass(checkpoint); got.Open {
		t.Errorf("teams_projects reports the clickhouse source's pass: %+v", got)
	}
	teams.window.setPass(windowScopeFor("org", 3), open)
	if got := teams.ProjectionWindowPass(checkpoint); got != want {
		t.Errorf("teams_projects: report = %+v, want %+v", got, want)
	}
	// One overlap (30 min) and a second later: overdue.
	now = started.Add(30*time.Minute + time.Second)
	if got := teams.ProjectionWindowPass(checkpoint); !got.Overdue || got.Age != 30*time.Minute+time.Second {
		t.Errorf("teams_projects after the bound: report = %+v, want overdue", got)
	}
	var none *ClickHouseProjectionSource
	var noTeams *TeamsProjectsSource
	if none.ProjectionWindowPass(checkpoint).Open || noTeams.ProjectionWindowPass(checkpoint).Open {
		t.Error("a nil source reports an open pass")
	}
}
