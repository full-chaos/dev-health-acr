package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

const tableReadPageMessage = "devhealthsource table read page"

func tableReadLines(t *testing.T, logs *bytes.Buffer, message string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var line map[string]any
		if raw == "" || json.Unmarshal([]byte(raw), &line) != nil || line["msg"] != message {
			continue
		}
		table, _ := line["table"].(string)
		out[table] = append(out[table], line)
	}
	return out
}

// Every table of the cursor, not only the membership producer, states per page
// the rows its statement returned and the rows the cursor moved past.
func TestEveryNonMembershipTableLogsItsReadPerPage(t *testing.T) {
	var logs bytes.Buffer
	source := enabledTeamsProjectsSource(t, liveShapedEdgeClient())
	source.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	if _, ok, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}); err != nil || !ok {
		t.Fatalf("NextProjectionBatch ok=%v err=%v", ok, err)
	}
	names := devhealthsource.NonMembershipTableNamesForTest()
	if len(names) != 5 {
		t.Fatalf("registry names %d non-membership tables, want 5: %v", len(names), names)
	}
	lines := tableReadLines(t, &logs, tableReadPageMessage)
	for _, name := range names {
		got := lines[name]
		if len(got) != 1 {
			t.Fatalf("table %s: %d page lines, want 1\n%s", name, len(got), logs.String())
		}
		line := got[0]
		if line["statement_rows"].(float64) < 1 || line["consumed_rows"].(float64) != line["statement_rows"] || line["consumed_new"] != line["consumed_rows"] || line["page_n"] != float64(1) || line["replay"] != false {
			t.Fatalf("table %s page line = %v, want statement rows consumed once, all new, page 1", name, line)
		}
	}
}

// A large organization reads teams and projects whole in the dimension phase,
// before the fact walk. Every dimension page states the rows its statement
// returned and the rows the cursor moved past on the same line, and the pages
// add up to every row of each table (a later fact-walk page may read rows
// again and cut its page, so only the first page and the new rows are fixed).
func TestDimensionPhasePagesLogTheirReadAndConsumption(t *testing.T) {
	at := time.Date(2026, 10, 10, 3, 55, 0, 0, time.UTC)
	var teams, projects [][]any
	for n := 0; n < 200; n++ {
		stamp := at.Add(time.Duration(n) * time.Millisecond)
		teams = append(teams, teamRow(fmt.Sprintf("gh:team-%03d", n), fmt.Sprintf("Team %d", n), "", "github", fmt.Sprintf("team-%03d", n), 1, stamp, nil, nil))
		projects = append(projects, projectRow(fmt.Sprintf("00000000-0000-4000-9000-%012d", n), fmt.Sprintf("Project %d", n), "", "linear", "backlog", fmt.Sprintf("https://linear.app/acme/project/p-%012d", n), 1, stamp))
	}
	stampOf := func(column int) func([]any) (time.Time, string) {
		return func(row []any) (time.Time, string) { return row[column].(time.Time), row[0].(string) }
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM teams AS tm FINAL", rows: teams, cursorOf: stampOf(7)},
		{match: "FROM projects FINAL\nWHERE", rows: projects, cursorOf: stampOf(7)},
	}}
	var logs bytes.Buffer
	source := enabledTeamsProjectsSource(t, client)
	source.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}
	for call := 1; call <= 10; call++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil || !available {
			t.Fatalf("call %d: available=%v err=%v", call, available, err)
		}
		checkpoint.Cursor = batch.NextCursor
		if devhealthsource.CursorFactPositionMovedForTest(t, batch.NextCursor) {
			break
		}
	}
	lines := tableReadLines(t, &logs, tableReadPageMessage)
	for _, name := range []string{"teams", "projects"} {
		newRows := 0
		for _, line := range lines[name] {
			if line["page_n"] == float64(1) && line["statement_rows"].(float64) != line["consumed_rows"].(float64) {
				t.Fatalf("table %s first page states %v statement rows and %v consumed: %v", name, line["statement_rows"], line["consumed_rows"], line)
			}
			newRows += int(line["consumed_new"].(float64))
		}
		if newRows != 200 {
			t.Fatalf("table %s pages consumed %d new rows, want 200: %v", name, newRows, lines[name])
		}
	}
}
