package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// onePositionRows is n copies of one row: every row shares one cursor
// position and one row key, so a statement whose limit is below n keeps none
// of them and says more rows lie beyond it.
func onePositionRows(row []any, n int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = row
	}
	return rows
}

type pageStatement struct {
	n    float64
	rows float64
	more bool
}

func zeroRowPageLines(t *testing.T, logs *bytes.Buffer, table string) []pageStatement {
	t.Helper()
	var out []pageStatement
	for _, line := range tableReadLines(t, logs, tableReadPageMessage)[table] {
		more, _ := line["statement_more"].(bool)
		n, _ := line["page_n"].(float64)
		rows, _ := line["statement_rows"].(float64)
		out = append(out, pageStatement{n: n, rows: rows, more: more})
	}
	return out
}

// A statement that returned no row inside its limit but says rows lie beyond
// it ran: it writes its page line, on every path that runs it.
func TestZeroRowStatementWithMoreRowsWritesItsPageLine(t *testing.T) {
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}
	at := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	team := teamRow("gh:one-position", "One Position", "", "github", "one-position", 1, at, nil, nil)
	attribution := workItemTeamRow("linear:CHAOS-1", "CHAOS", "native_team", "high", zeroRepositoryUUID, "", at)

	t.Run("snapshot", func(t *testing.T) {
		// 151 rows on one position: the snapshot statement (cap 150) keeps
		// none and says more; the dimension read (cap 200) takes them.
		var logs bytes.Buffer
		source, client := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
			{match: statementMatches["teams"], rows: onePositionRows(team, 151)},
		}}, &logs)
		if _, ok, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || !ok {
			t.Fatalf("NextProjectionBatch ok=%v err=%v, want the dimension batch", ok, err)
		}
		want := []pageStatement{{n: 1, rows: 0, more: true}, {n: 2, rows: 151, more: false}}
		if got := zeroRowPageLines(t, &logs, "teams"); client.calls["teams"] != 2 || !equalStatements(got, want) {
			t.Fatalf("teams page lines = %v for %d statements, want %v (the snapshot statement, then the dimension read)\n%s", got, client.calls["teams"], want, logs.String())
		}
	})

	t.Run("paged_bound_refusal", func(t *testing.T) {
		// 201 rows on one position of a fact table: the snapshot statement
		// and the paged statement (cap 200) keep none and say more; the
		// page bound refuses the paged one.
		var logs bytes.Buffer
		source, _ := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
			{match: "FROM work_item_team_attributions AS a FINAL", rows: onePositionRows(attribution, 201)},
		}}, &logs)
		_, _, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err == nil {
			t.Fatalf("the page bound did not refuse the paged read")
		}
		want := []pageStatement{{n: 1, rows: 0, more: true}, {n: 2, rows: 0, more: true}}
		if got := zeroRowPageLines(t, &logs, "work_item_team_attributions"); !equalStatements(got, want) {
			t.Fatalf("work_item_team_attributions page lines = %v, want %v (snapshot, then the refused paged statement)\n%s", got, want, logs.String())
		}
	})

	t.Run("dimension_bound_refusal", func(t *testing.T) {
		// 201 rows on one position of a dimension table: the snapshot
		// statement and the dimension statement keep none and say more; the
		// page bound refuses the dimension one.
		var logs bytes.Buffer
		source, client := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
			{match: statementMatches["teams"], rows: onePositionRows(team, 201)},
		}}, &logs)
		_, _, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err == nil {
			t.Fatalf("the page bound did not refuse the dimension read")
		}
		want := []pageStatement{{n: 1, rows: 0, more: true}, {n: 2, rows: 0, more: true}}
		if got := zeroRowPageLines(t, &logs, "teams"); client.calls["teams"] != 2 || !equalStatements(got, want) {
			t.Fatalf("teams page lines = %v for %d statements, want %v (snapshot, then the refused dimension statement)\n%s", got, client.calls["teams"], want, logs.String())
		}
	})
}

// The membership producer's statement follows the same rule: a statement that
// returned no row on any arm but says rows lie beyond its limit writes its
// page line for every arm.
func TestZeroRowMembershipStatementWithMoreRowsWritesItsPageLines(t *testing.T) {
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}
	at := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	presence := presenceRow("work_item", zeroRepositoryUUID, "linear:CHAOS-1", "", at, "work_item_column", "linear", "631fcb5f-c3e9-49ff-b17c-07877aaac9b7", "631fcb5f-c3e9-49ff-b17c-07877aaac9b7", 1)

	// 201 rows on one position: the snapshot statement and the paged
	// statement (cap 200) keep none and say more; the page bound refuses the
	// paged one.
	var logs bytes.Buffer
	source, _ := countingTeamsProjectsSource(t, &fakeClient{tables: []fakeTable{
		{match: "FROM project_membership_presence AS m", rows: onePositionRows(presence, 201)},
	}}, &logs)
	if _, _, err := source.NextProjectionBatch(context.Background(), checkpoint); err == nil {
		t.Fatalf("the page bound did not refuse the paged membership read")
	}
	got := map[string][]pageStatement{}
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) != nil || line["msg"] != "devhealthsource project membership page" {
			continue
		}
		arm, _ := line["arm"].(string)
		more, _ := line["statement_more"].(bool)
		n, _ := line["page_n"].(float64)
		rows, _ := line["statement_rows"].(float64)
		got[arm] = append(got[arm], pageStatement{n: n, rows: rows, more: more})
	}
	want := []pageStatement{{n: 1, rows: 0, more: true}, {n: 2, rows: 0, more: true}}
	for _, arm := range []string{"transition", "work_item_column"} {
		if !equalStatements(got[arm], want) {
			t.Fatalf("membership arm %s page lines = %v, want %v (snapshot, then the refused paged statement)\n%s", arm, got[arm], want, logs.String())
		}
	}
}

func equalStatements(got, want []pageStatement) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
