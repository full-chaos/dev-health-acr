package devhealthsource_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// membershipDrain is every BELONGS_TO_PROJECT edge a from-scratch drain of the
// teams/projects source emitted, by relationship id, split by arm: a
// work_item_column edge carries no interval, a transition edge does.
type membershipDrain struct {
	column, transition map[string]bool
	pages              int
}

func drainMemberships(t *testing.T, f *ingestColumnsFixture, between func(page int)) membershipDrain {
	t.Helper()
	out := membershipDrain{column: map[string]bool{}, transition: map[string]bool{}}
	cursor := ""
	replays := map[string]bool{}
	for ; out.pages < 400; out.pages++ {
		b, ok, err := f.h.src.NextProjectionBatch(f.ctx, contextfabric.ProjectionCheckpoint{OrgID: f.h.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: NextProjectionBatch: %v", out.pages, err)
		}
		if !ok {
			return out
		}
		requireCursorProgress(t, "membership drain", cursor, b, replays)
		cursor = b.NextCursor
		for _, r := range b.Relationships {
			if r.Type != contractsv1.ContextFabricRelationshipBelongsToProject {
				continue
			}
			if r.ValidFrom == nil {
				out.column[r.RelationshipID] = true
			} else {
				out.transition[r.RelationshipID] = true
			}
		}
		if between != nil {
			between(out.pages)
		}
	}
	t.Fatalf("the drain did not converge in %d pages", out.pages)
	return out
}

// plantMembershipShape seeds the shape a migrated organization holds, with the
// id formats the providers write (linear:<identifier> work items, UUID
// projects): every legacy work item carries the ONE migration stamp, the legacy
// transitions carry one stamp 26 ms before it, work items synced after the
// migration carry their own stamps, and the attributions of the same work
// items sit at an older position in the shared cursor, with a few recomputed
// among the later stamps.
//
// Every ingest stamp is written through fromUnixTimestamp64Milli: a time bound
// as a statement argument reaches ClickHouse at second precision, which would
// put the two legacy stamps on whole seconds.
func plantMembershipShape(f *ingestColumnsFixture, migration time.Time, legacy, live, secondBulk, transitions, attributions int) {
	const stamp = "fromUnixTimestamp64Milli(toInt64(?), 'UTC')"
	project := func(n int) string { return fmt.Sprintf("6241316a-7659-4000-8000-00000000000%d", n) }
	f.team("T-drain", f.old, f.old)
	for n := 1; n <= 4; n++ {
		f.project(project(n), fmt.Sprintf("K%d", n), f.old, f.old)
	}
	workItems := `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT concat(?, toString(number)), ?, ?, 'issue', 'open', '', '', 'linear', ?, ?, ?, ` + stamp
	mustExec(f.t, f.ctx, f.h.direct, workItems+` FROM numbers(?)`,
		"linear:LEG-", zeroUUID, f.h.orgID, project(1), f.old, f.old, migration.UnixMilli(), uint64(legacy))
	mustExec(f.t, f.ctx, f.h.direct, workItems+` + toIntervalSecond(number + 1) FROM numbers(?)`,
		"linear:LIVE-", zeroUUID, f.h.orgID, project(2), f.old, f.old, migration.UnixMilli(), uint64(live))
	mustExec(f.t, f.ctx, f.h.direct, workItems+` FROM numbers(?)`,
		"linear:BULK-", zeroUUID, f.h.orgID, project(3), f.old, f.old, migration.Add(2*time.Hour).UnixMilli(), uint64(secondBulk))
	// The subjects with history also hold a work_items row with a project; the
	// view keeps them out of the column arm.
	mustExec(f.t, f.ctx, f.h.direct, workItems+` FROM numbers(?)`,
		"linear:MOV-", zeroUUID, f.h.orgID, project(4), f.old, f.old, migration.UnixMilli(), uint64(transitions))
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, ?, 'work_item', concat('linear:MOV-', toString(number)), 'linear', '', ?, '', '', '', ?, ?, concat('linear:', toString(generateUUIDv4())), `+stamp+` FROM numbers(?)`,
		f.h.orgID, zeroUUID, project(4), f.old, f.old, migration.Add(-26*time.Millisecond).UnixMilli(), uint64(transitions))
	attribution := `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)
SELECT ?, ?, concat(?, toString(number)), 'T-drain', 'native_team', 1, 'high', ` + stamp
	mustExec(f.t, f.ctx, f.h.direct, attribution+` + toIntervalMillisecond(intDiv(number, 250)) FROM numbers(?)`,
		f.h.orgID, zeroUUID, "linear:LEG-", migration.Add(-3*time.Hour).UnixMilli(), uint64(attributions))
	// Attributions recomputed after the migration interleave with the work
	// items synced after it, so the page cut falls inside the later stamps.
	mustExec(f.t, f.ctx, f.h.direct, attribution+` + toIntervalMillisecond(500 + 1000 * number) FROM numbers(?)`,
		f.h.orgID, zeroUUID, "linear:LIVE-", migration.UnixMilli(), uint64(live))
}

// migrationStamp is a legacy stamp with a millisecond fraction, as a mutation
// writes it (04:37:41.423 in the incident), six hours back.
func migrationStamp(f *ingestColumnsFixture) time.Time {
	return f.now.Add(-6 * time.Hour).Add(423 * time.Millisecond)
}

// membershipLogLines are the JSON log records with msg.
func membershipLogLines(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("log line is not JSON: %q", scanner.Text())
		}
		if record["msg"] == msg {
			out = append(out, record)
		}
	}
	return out
}

// A from-scratch drain reads every work_item_column membership: the legacy
// rows share one ingest stamp across many pages, and the cursor first walks
// another table's older rows. The page log states, per arm, the distinct rows
// the cursor moved past; the scanned counter does not.
func TestMembershipColumnArmDrainsEveryRowAtASharedIngestStamp(t *testing.T) {
	const legacy, live, secondBulk, transitions, attributions = 1500, 300, 450, 450, 900
	var logs bytes.Buffer
	f := newIngestColumnsFixture(t, "76590000-0000-4000-8000-000000000001", nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	migration := migrationStamp(f)
	plantMembershipShape(f, migration, legacy, live, secondBulk, transitions, attributions)
	got := drainMemberships(t, f, nil)
	column := legacy + live + secondBulk
	if len(got.column) != column {
		t.Fatalf("work_item_column arm: %d edges read in %d pages, %d rows planted", len(got.column), got.pages, column)
	}
	if len(got.transition) != transitions {
		t.Fatalf("transition arm: %d edges read, %d rows planted", len(got.transition), transitions)
	}

	summaries := membershipLogLines(t, &logs, "devhealthsource project membership read drained")
	if len(summaries) != 3 {
		t.Fatalf("%d drained summary lines, want one per arm", len(summaries))
	}
	want := map[string]int{"work_item_column": column, "transition": transitions, "column_superseded": transitions}
	for _, line := range summaries {
		arm, _ := line["arm"].(string)
		total, _ := line["rows_total"].(float64)
		if int(total) != want[arm] {
			t.Fatalf("arm %q: summary rows_total = %v, want the %d distinct rows planted", arm, line["rows_total"], want[arm])
		}
		// The scanned counter includes the lookahead row and the rows read
		// again while the cursor walks the attributions.
		if scanned, _ := line["scanned_rows_total"].(float64); int(scanned) <= want[arm] {
			t.Fatalf("arm %q: scanned_rows_total = %v is not above the %d distinct rows; the fixture no longer walks another table first", arm, line["scanned_rows_total"], want[arm])
		}
		if pages, _ := line["pages"].(float64); pages < float64(want[arm]/200) {
			t.Fatalf("arm %q: summary pages = %v for %d rows", arm, line["pages"], want[arm])
		}
	}
	consumed := map[string]int{}
	legacyStamp := migration.UTC().Format(time.RFC3339Nano)
	legacyPages := 0
	for _, line := range membershipLogLines(t, &logs, "devhealthsource project membership page") {
		arm, _ := line["arm"].(string)
		rows, _ := line["consumed_rows"].(float64)
		fresh, _ := line["consumed_new"].(float64)
		if rows != fresh || line["replay"] != false || line["run"] != float64(1) {
			t.Fatalf("arm %q page %v: a first read of each row logged consumed_rows=%v consumed_new=%v replay=%v run=%v", arm, line["page_n"], rows, fresh, line["replay"], line["run"])
		}
		consumed[arm] += int(fresh)
		returned, _ := line["statement_rows"].(float64)
		if (returned > 0 || rows > 0) && (line["last_key_digest"] == "" || line["last_stamp"] == "") {
			t.Fatalf("a page line with rows carries no last position: %v", line)
		}
		if total, _ := line["consumed_total"].(float64); int(total) != consumed[arm] {
			t.Fatalf("arm %q page %v: consumed_total = %v, running sum of consumed_new = %d", arm, line["page_n"], line["consumed_total"], consumed[arm])
		}
		if arm == "work_item_column" && rows > 0 && line["last_stamp"] == legacyStamp {
			legacyPages++
		}
	}
	// The 1500 legacy rows share the one stamp: at least seven pages end on it.
	if legacyPages < legacy/200 {
		t.Fatalf("%d work_item_column pages end on the legacy stamp %s, want at least %d", legacyPages, legacyStamp, legacy/200)
	}
	for arm, n := range want {
		if consumed[arm] != n {
			t.Fatalf("arm %q: the page lines add up to %d consumed rows, want %d", arm, consumed[arm], n)
		}
	}
}

// The same drain while a sync writes the legacy work items again: a row that
// is written again moves to a later ingest stamp, and the drain still reads it.
func TestMembershipColumnArmDrainsEveryRowWhileASyncRewritesThem(t *testing.T) {
	const legacy, live, secondBulk, transitions, attributions = 1500, 300, 450, 450, 900
	f := newIngestColumnsFixture(t, "76590000-0000-4000-8000-000000000002", nil, nil)
	migration := migrationStamp(f)
	plantMembershipShape(f, migration, legacy, live, secondBulk, transitions, attributions)
	const batch = 100
	next := 0
	got := drainMemberships(t, f, func(page int) {
		if next >= legacy {
			return
		}
		stamp := f.now.Add(time.Duration(page) * time.Millisecond)
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT concat('linear:LEG-', toString(number + ?)), ?, ?, 'issue', 'open', '', '', 'linear', '6241316a-7659-4000-8000-000000000001', ?, ?, fromUnixTimestamp64Milli(toInt64(?), 'UTC') FROM numbers(?)`,
			uint64(next), zeroUUID, f.h.orgID, f.old, stamp, stamp.UnixMilli(), uint64(batch))
		next += batch
	})
	if want := legacy + live + secondBulk; len(got.column) != want {
		t.Fatalf("work_item_column arm: %d edges read in %d pages, %d rows planted", len(got.column), got.pages, want)
	}
	if len(got.transition) != transitions {
		t.Fatalf("transition arm: %d edges read, %d rows planted", len(got.transition), transitions)
	}
}
