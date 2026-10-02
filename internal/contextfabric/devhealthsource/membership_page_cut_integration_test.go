package devhealthsource_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

const pageCutStamp = "fromUnixTimestamp64Milli(toInt64(?), 'UTC')"

func pageCutProject(n int) string {
	return fmt.Sprintf("6241316a-7659-4000-8000-00000000000%d", n)
}

// plantWalkedAttributions seeds n primary attributions, with their work items
// (no project, so they stay out of the column arm), at positions older than
// every membership row.
func plantWalkedAttributions(f *ingestColumnsFixture, prefix string, at time.Time, n int) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT concat(?, toString(number)), ?, ?, 'issue', 'open', '', '', 'linear', '', ?, ?, `+pageCutStamp+` FROM numbers(?)`,
		prefix, zeroUUID, f.h.orgID, f.old, f.old, at.UnixMilli(), uint64(n))
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)
SELECT ?, ?, concat(?, toString(number)), 'T-drain', 'native_team', 1, 'high', `+pageCutStamp+` + toIntervalMillisecond(intDiv(number, 250)) FROM numbers(?)`,
		f.h.orgID, zeroUUID, prefix, at.UnixMilli(), uint64(n))
}

func plantColumnRows(f *ingestColumnsFixture, prefix string, project string, at time.Time, n int) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT concat(?, leftPad(toString(number), 4, '0')), ?, ?, 'issue', 'open', '', '', 'linear', ?, ?, ?, `+pageCutStamp+` FROM numbers(?)`,
		prefix, zeroUUID, f.h.orgID, project, f.old, f.old, at.UnixMilli(), uint64(n))
}

// plantTransitions seeds n subjects prefix0000.. with one ADD each, all on one
// ingest stamp.
func plantTransitions(f *ingestColumnsFixture, prefix string, project string, at time.Time, n int) {
	plantColumnRows(f, prefix, project, at, n)
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, ?, 'work_item', concat(?, leftPad(toString(number), 4, '0')), 'linear', '', ?, '', '', '', ?, ?, concat('linear:', toString(generateUUIDv4())), `+pageCutStamp+` FROM numbers(?)`,
		f.h.orgID, zeroUUID, prefix, project, f.old, f.old, at.UnixMilli(), uint64(n))
}

type pageLine struct {
	n                                      int
	statementRows, consumed, consumedTotal int
	more                                   bool
	lastStamp, digest                      string
}

func pageLines(t *testing.T, logs *bytes.Buffer) map[string][]pageLine {
	t.Helper()
	out := map[string][]pageLine{}
	for _, line := range membershipLogLines(t, logs, "devhealthsource project membership page") {
		arm, _ := line["arm"].(string)
		num := func(k string) int { v, _ := line[k].(float64); return int(v) }
		stamp, _ := line["last_stamp"].(string)
		digest, _ := line["last_key_digest"].(string)
		more, _ := line["statement_more"].(bool)
		out[arm] = append(out[arm], pageLine{n: num("page_n"), statementRows: num("statement_rows"), consumed: num("consumed_rows"), consumedTotal: num("consumed_total"), more: more, lastStamp: stamp, digest: digest})
	}
	return out
}

// The shape of the production read of 2026-10-01T23:53Z: about 3.5k rows of
// another table sit before the first membership row in the shared cursor, three
// transitions sit on the legacy transition stamp, 64 column rows on the legacy
// work item stamp, and the later transitions on a later stamp. Every membership
// statement returns the same rows, and the page cut holds none of them, until
// the cursor has walked the other table.
func TestMembershipPagesConsumeNoRowWhileTheSharedCursorWalksAnotherTable(t *testing.T) {
	const walked, legacyTransitions, legacyColumn, laterTransitions = 3450, 3, 64, 400
	var logs bytes.Buffer
	f := newIngestColumnsFixture(t, "76590000-0000-4000-8000-000000000003", nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	migration := migrationStamp(f)
	f.team("T-drain", f.old, f.old)
	for n := 1; n <= 4; n++ {
		f.project(pageCutProject(n), fmt.Sprintf("K%d", n), f.old, f.old)
	}
	plantWalkedAttributions(f, "linear:ATT-", migration.Add(-3*time.Hour), walked)
	// Attributions computed between the legacy stamps and the later transitions
	// fill the rest of page 19, as in production.
	plantWalkedAttributions(f, "linear:ATB-", migration.Add(time.Hour), 200-(walked+5)%200-legacyTransitions-legacyColumn)
	plantTransitions(f, "linear:MOV-", pageCutProject(4), migration.Add(-26*time.Millisecond), legacyTransitions)
	plantColumnRows(f, "linear:LEG-", pageCutProject(1), migration, legacyColumn)
	plantTransitions(f, "linear:NEW-", pageCutProject(3), migration.Add(2*time.Hour), laterTransitions)

	got := drainMemberships(t, f, nil)
	if len(got.column) != legacyColumn || len(got.transition) != legacyTransitions+laterTransitions {
		t.Fatalf("read %d column and %d transition edges, planted %d and %d", len(got.column), len(got.transition), legacyColumn, legacyTransitions+laterTransitions)
	}

	lines := pageLines(t, &logs)
	column, transition := lines["work_item_column"], lines["transition"]
	for i, line := range column {
		t.Logf("column page_n=%d statement_rows=%d more=%v consumed=%d total=%d last_stamp=%s digest=%s | transition statement_rows=%d consumed=%d last_stamp=%s",
			line.n, line.statementRows, line.more, line.consumed, line.consumedTotal, line.lastStamp, line.digest, transition[i].statementRows, transition[i].consumed, transition[i].lastStamp)
		if i > 22 {
			break
		}
	}
	// 1 team + 4 projects + 3450 attributions = 3455 rows before the first
	// membership row: the snapshot attempt (page 1, cap 150) and 17 full pages
	// of 200 (pages 2-18) hold no membership row.
	const zeroPages = 1 + (walked+5)/200
	if zeroPages != 18 {
		t.Fatalf("the fixture gives %d zero-consume pages, the production read had 18", zeroPages)
	}
	legacyStamp := migration.UTC().Format(time.RFC3339Nano)
	for i := 0; i < zeroPages; i++ {
		c, tr := column[i], transition[i]
		wantTransition := 136
		if i == 0 {
			wantTransition = 86
		}
		if c.n != i+1 || c.statementRows != legacyColumn || !c.more || c.consumed != 0 || c.lastStamp != legacyStamp || c.digest != column[0].digest {
			t.Fatalf("column page %d = %+v, want statement_rows %d, more, consumed 0, the legacy stamp and one digest", i+1, c, legacyColumn)
		}
		if tr.statementRows != wantTransition || tr.consumed != 0 {
			t.Fatalf("transition page %d = %+v, want statement_rows %d and consumed 0", i+1, tr, wantTransition)
		}
	}
	if c := column[zeroPages]; c.n != 19 || c.statementRows != legacyColumn || c.consumed != legacyColumn {
		t.Fatalf("column page 19 = %+v, want the %d rows consumed", c, legacyColumn)
	}
	if tr := transition[zeroPages]; tr.statementRows != 136 || tr.consumed != legacyTransitions {
		t.Fatalf("transition page 19 = %+v, want statement_rows 136 and the %d legacy transitions consumed", tr, legacyTransitions)
	}
	// Rows that are read again while the cursor walks another table are not
	// consumed again: neither guard line is written.
	if errs, bounded := membershipLogLines(t, &logs, notNewRowsMessage), membershipLogLines(t, &logs, boundedPageMessage); len(errs) != 0 || len(bounded) != 0 {
		t.Fatalf("a read with one row per cursor position wrote %d not-new-rows lines and %d bounded-page lines", len(errs), len(bounded))
	}
	for _, line := range membershipLogLines(t, &logs, "devhealthsource project membership read drained") {
		if line["arm"] == "work_item_column" {
			if total, _ := line["rows_total"].(float64); int(total) != legacyColumn {
				t.Fatalf("column rows_total = %v, want %d", line["rows_total"], legacyColumn)
			}
			if scanned, _ := line["scanned_rows_total"].(float64); int(scanned) != 19*legacyColumn {
				t.Fatalf("column scanned_rows_total = %v, want %d (19 statements of %d rows)", line["scanned_rows_total"], 19*legacyColumn, legacyColumn)
			}
		}
	}
}

const (
	notNewRowsMessage  = "devhealthsource project membership page consumed rows that are not new and is not a replay"
	boundedPageMessage = "devhealthsource page ended at the last row a truncated table returned; rows of that table share one cursor position"
)

// Two rows of one statement share one ingest stamp and one row key: a replayed
// history event (same event_id, a different occurred_at) gives the interval row
// and the duplicate-ADD row of that event the same key, and on the legacy stamp
// the same position. The page cut counts them as one row, so a full statement
// of 200 rows fills 199 slots, and the row that would complete the page comes
// from another table and lies past rows the membership statement has not
// returned yet. In the second shape the two rows lie across the statement
// limit, and a page that ends on the first of them makes the next keyset
// predicate pass the second.
func TestPageCutKeepsEveryRowWhenTwoStatementRowsShareStampAndKey(t *testing.T) {
	const legacyTransitions, legacyColumn, liveColumn = 600, 600, 50
	for _, tc := range []struct {
		name, org, subject string
		// pairPage is the statement whose page moves the cursor past the two
		// rows; thirdStatementRows is what statement 3 keeps inside its limit.
		pairPage, thirdStatementRows, boundedPages int
	}{
		// Statement 3 returns the rows 195..394 of the arm; the two rows are 300 and 301.
		{name: "both rows inside one statement", org: "76590000-0000-4000-8000-000000000004", subject: "linear:MOV-0300", pairPage: 3, thirdStatementRows: 200, boundedPages: 1},
		// The two rows are 394 and 395: the last row inside the limit of
		// statement 3 and the first row past it.
		{name: "the rows across the statement limit", org: "76590000-0000-4000-8000-000000000005", subject: "linear:MOV-0394", pairPage: 4, thirdStatementRows: 199, boundedPages: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := newIngestColumnsFixture(t, tc.org, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
			migration := migrationStamp(f)
			transitionStamp := migration.Add(-26 * time.Millisecond)
			f.team("T-drain", f.old, f.old)
			for n := 1; n <= 4; n++ {
				f.project(pageCutProject(n), fmt.Sprintf("K%d", n), f.old, f.old)
			}
			plantTransitions(f, "linear:MOV-", pageCutProject(4), transitionStamp, legacyTransitions)
			// The replay: one subject gets one event at two occurred_at values.
			for i := 0; i < 2; i++ {
				mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, 'linear:replayed-event', `+pageCutStamp,
					f.h.orgID, zeroUUID, tc.subject, pageCutProject(2), f.old.Add(time.Duration(i+1)*time.Hour), f.old, transitionStamp.UnixMilli())
			}
			plantColumnRows(f, "linear:LEG-", pageCutProject(1), migration, legacyColumn)
			// Rows written after the migration: a team and the work items of a later sync.
			later := migration.Add(2 * time.Hour).Truncate(time.Second)
			f.team("T-later", later, later)
			plantColumnRows(f, "linear:LIVE-", pageCutProject(3), later.Add(time.Minute), liveColumn)

			got := drainMemberships(t, f, nil)
			lines := pageLines(t, &logs)
			consumed := map[string]int{}
			for i, line := range lines["work_item_column"] {
				tr := lines["transition"][i]
				consumed["transition"], consumed["work_item_column"] = consumed["transition"]+tr.consumed, consumed["work_item_column"]+line.consumed
				t.Logf("page_n=%d more=%v | transition statement_rows=%d consumed=%d total=%d last_stamp=%s | column statement_rows=%d consumed=%d total=%d last_stamp=%s",
					line.n, line.more, tr.statementRows, tr.consumed, tr.consumedTotal, tr.lastStamp, line.statementRows, line.consumed, line.consumedTotal, line.lastStamp)
			}
			// 600 intervals of MOV-0000..0599 in project 4 and one of the replayed subject in project 2.
			if want := legacyTransitions + 1; len(got.transition) != want {
				t.Errorf("transition arm: %d edges read, %d planted", len(got.transition), want)
			}
			if want := legacyColumn + liveColumn; len(got.column) != want {
				t.Errorf("work_item_column arm: %d edges read in %d pages, %d planted", len(got.column), got.pages, want)
			}
			// The cursor moves past every statement row once: the 600 ADD rows,
			// the interval row and the duplicate-ADD row of the replayed event.
			// An edge count cannot show a lost duplicate-ADD row; this does.
			if want := legacyTransitions + 2; consumed["transition"] != want {
				t.Errorf("transition arm: the pages consumed %d statement rows, %d planted", consumed["transition"], want)
			}
			if want := legacyColumn + liveColumn; consumed["work_item_column"] != want {
				t.Errorf("work_item_column arm: the pages consumed %d statement rows, %d planted", consumed["work_item_column"], want)
			}
			if third := lines["transition"][2]; third.n != 3 || third.statementRows != tc.thirdStatementRows {
				t.Errorf("statement 3 kept %d transition rows inside its limit (page_n %d), want %d", third.statementRows, third.n, tc.thirdStatementRows)
			}
			// The page that holds both rows says that two rows share a position.
			errs := membershipLogLines(t, &logs, notNewRowsMessage)
			if len(errs) != 1 {
				t.Fatalf("%d not-new-rows lines, want 1: %v", len(errs), errs)
			}
			if e := errs[0]; e["level"] != "ERROR" || e["arm"] != "transition" || e["page_n"] != float64(tc.pairPage) || e["consumed_rows"] != float64(200) || e["consumed_new"] != float64(199) || e["shared_position_rows"] != float64(1) {
				t.Errorf("not-new-rows line = %v, want transition page %d with 200 rows, 199 new, 1 on a shared position", e, tc.pairPage)
			}
			bounded := membershipLogLines(t, &logs, boundedPageMessage)
			if len(bounded) != tc.boundedPages {
				t.Fatalf("%d bounded-page lines, want %d: %v", len(bounded), tc.boundedPages, bounded)
			}
			for _, line := range bounded {
				if line["table"] != "project_membership_presence" || line["last_stamp"] != transitionStamp.UTC().Format(time.RFC3339Nano) {
					t.Errorf("bounded-page line = %v, want the membership table on the legacy transition stamp", line)
				}
			}
		})
	}
}
