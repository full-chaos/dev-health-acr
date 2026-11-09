package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

const tableReadDrainedMessage = "devhealthsource table read drained"

// statementMatches names the page statement of each dimension table, so a
// test counts the statements the source ran per table.
var statementMatches = map[string]string{
	"teams":    "FROM teams AS tm FINAL",
	"projects": "FROM projects FINAL\nWHERE",
}

type statementCountingClient struct {
	base  *fakeClient
	calls map[string]int
}

func (c *statementCountingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	for table, match := range statementMatches {
		if strings.Contains(statement, match) {
			c.calls[table]++
		}
	}
	return c.base.Query(ctx, statement, bindings)
}

func countingTeamsProjectsSource(t *testing.T, base *fakeClient, logs *bytes.Buffer) (*devhealthsource.TeamsProjectsSource, *statementCountingClient) {
	t.Helper()
	client := &statementCountingClient{base: base, calls: map[string]int{}}
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	source.WithLogger(slog.New(slog.NewJSONHandler(logs, nil)))
	return source, client
}

func snapshotTeamRows(n int) [][]any {
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, teamRow(fmt.Sprintf("gh:snap-%04d", i), fmt.Sprintf("Snap %d", i), "", "github", fmt.Sprintf("snap-%04d", i), 1, at.Add(time.Duration(i)*time.Second), nil, nil))
	}
	return rows
}

func snapshotProjectRows(n int) [][]any {
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, projectRow(fmt.Sprintf("00000000-0000-4000-9000-%012d", i), fmt.Sprintf("Snap %d", i), "", "linear", "backlog", fmt.Sprintf("https://linear.app/snap/p-%012d", i), 1, at.Add(time.Duration(i)*time.Second)))
	}
	return rows
}

func stampCursor(row []any) (time.Time, string) { return row[7].(time.Time), row[0].(string) }

// requireOneLinePerStatement fails unless every statement the table ran is one
// page line, numbered in order: each statement here returns rows.
func requireOneLinePerStatement(t *testing.T, logs *bytes.Buffer, client *statementCountingClient, table string) []map[string]any {
	t.Helper()
	lines := tableReadLines(t, logs, tableReadPageMessage)[table]
	if client.calls[table] == 0 || len(lines) != client.calls[table] {
		t.Fatalf("table %s: %d page lines for %d statements, want one per statement\n%s", table, len(lines), client.calls[table], logs.String())
	}
	for i, line := range lines {
		if line["page_n"] != float64(i+1) || line["statement_rows"].(float64) < 1 {
			t.Fatalf("table %s line %d = %v, want page_n %d with the statement's rows", table, i, line, i+1)
		}
	}
	return lines
}

// requireSnapshotStatement fails unless the first line is the from-scratch
// snapshot statement: the snapshot cap read, more rows beyond, none consumed.
func requireSnapshotStatement(t *testing.T, table string, line map[string]any) {
	t.Helper()
	if line["statement_rows"] != float64(150) || line["statement_more"] != true || line["consumed_rows"] != float64(0) {
		t.Fatalf("table %s first line = %v, want the snapshot statement: 150 rows, more, none consumed", table, line)
	}
}

// Every successful statement of a from-scratch read states its rows on the
// path that read it, whether the snapshot is published, empty, truncated, or
// stopped by a later table's error.
func TestSnapshotStatementsLogTheirRead(t *testing.T) {
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}

	t.Run("accepted_nonempty_snapshot", func(t *testing.T) {
		var logs bytes.Buffer
		source, _ := countingTeamsProjectsSource(t, liveShapedEdgeClient(), &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || !ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v", ok, err)
		}
		lines := tableReadLines(t, &logs, tableReadPageMessage)
		for _, name := range devhealthsource.NonMembershipTableNamesForTest() {
			if len(lines[name]) != 1 {
				t.Fatalf("table %s: %d page lines, want 1\n%s", name, len(lines[name]), logs.String())
			}
		}
	})

	t.Run("empty_snapshot", func(t *testing.T) {
		var logs bytes.Buffer
		source, _ := countingTeamsProjectsSource(t, &fakeClient{}, &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v, want no batch and no error", ok, err)
		}
		drained := tableReadLines(t, &logs, tableReadDrainedMessage)
		for _, name := range devhealthsource.NonMembershipTableNamesForTest() {
			got := drained[name]
			if len(got) != 1 || got[0]["rows_total"] != float64(0) || got[0]["statements"] != float64(1) {
				t.Fatalf("table %s drained lines = %v, want one: 0 rows, 1 statement\n%s", name, got, logs.String())
			}
		}
		if pages := tableReadLines(t, &logs, tableReadPageMessage); len(pages) != 0 {
			t.Fatalf("empty snapshot wrote page lines %v, want none", pages)
		}
	})

	t.Run("truncated_snapshot_then_dimension", func(t *testing.T) {
		var logs bytes.Buffer
		source, client := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
			{match: statementMatches["teams"], rows: snapshotTeamRows(1001), cursorOf: stampCursor},
		}}, &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || !ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v", ok, err)
		}
		lines := requireOneLinePerStatement(t, &logs, client, "teams")
		requireSnapshotStatement(t, "teams", lines[0])
	})

	t.Run("two_truncated_dimension_tables", func(t *testing.T) {
		var logs bytes.Buffer
		source, client := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
			{match: statementMatches["teams"], rows: snapshotTeamRows(600), cursorOf: stampCursor},
			{match: statementMatches["projects"], rows: snapshotProjectRows(500), cursorOf: stampCursor},
		}}, &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || !ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v", ok, err)
		}
		for _, name := range []string{"teams", "projects"} {
			lines := requireOneLinePerStatement(t, &logs, client, name)
			requireSnapshotStatement(t, name, lines[0])
		}
	})

	t.Run("partial_snapshot_error", func(t *testing.T) {
		var logs bytes.Buffer
		source, client := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
			{match: statementMatches["teams"], rows: snapshotTeamRows(1), cursorOf: stampCursor},
			{match: statementMatches["projects"], err: errors.New("projects read failed")},
		}}, &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err == nil || ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v, want the projects read error", ok, err)
		}
		lines := requireOneLinePerStatement(t, &logs, client, "teams")
		if lines[0]["statement_rows"] != float64(1) || lines[0]["consumed_rows"] != float64(0) {
			t.Fatalf("teams line = %v, want the statement's 1 row and none consumed", lines[0])
		}
	})
}

// The drain summary says every table's last statement found nothing more. A
// call that fails before every table answered says nothing of the tables it
// did not reach, so it writes no drain summary, from scratch or mid-run.
func TestAReadThatFailsDoesNotDrain(t *testing.T) {
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}
	failingAfterEmptyTeams := []fakeTable{
		{match: statementMatches["teams"], cursorOf: stampCursor},
		{match: statementMatches["projects"], err: errors.New("projects read failed")},
	}

	t.Run("from_scratch", func(t *testing.T) {
		var logs bytes.Buffer
		source, _ := countingTeamsProjectsSource(t, &fakeClient{tables: failingAfterEmptyTeams}, &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err == nil || ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v, want the projects read error", ok, err)
		}
		if drained := tableReadLines(t, &logs, tableReadDrainedMessage); len(drained) != 0 {
			t.Fatalf("a failed from-scratch read drained %v\n%s", drained, logs.String())
		}
	})

	t.Run("after_rows_were_consumed", func(t *testing.T) {
		var logs bytes.Buffer
		base := liveShapedEdgeClient()
		source, _ := countingTeamsProjectsSource(t, base, &logs)
		batch, ok, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil || !ok {
			t.Fatalf("first call ok=%v err=%v", ok, err)
		}
		base.tables = failingAfterEmptyTeams
		next := checkpoint
		next.Cursor = batch.NextCursor
		if _, ok, err := source.NextProjectionBatch(context.Background(), next); err == nil || ok {
			t.Fatalf("second call ok=%v err=%v, want the projects read error", ok, err)
		}
		if drained := tableReadLines(t, &logs, tableReadDrainedMessage); len(drained) != 0 {
			t.Fatalf("a failed read drained %v\n%s", drained, logs.String())
		}
	})
}
