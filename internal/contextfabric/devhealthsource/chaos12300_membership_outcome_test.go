package devhealthsource_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func membershipOutcomeLines(t *testing.T, buffer *bytes.Buffer) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, entry := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(entry), &decoded) != nil || decoded["msg"] != devhealthsource.WorkItemMembershipOutcomeLogMessage {
			continue
		}
		table, _ := decoded["table"].(string)
		if out[table] != nil {
			t.Fatalf("two outcome lines for table %s:\n%s", table, buffer.String())
		}
		out[table] = decoded
	}
	return out
}

func assertOutcomes(t *testing.T, line map[string]any, want map[string]float64) {
	t.Helper()
	if line == nil || line["level"] != "INFO" {
		t.Fatalf("outcome line = %v, want one Info line", line)
	}
	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s = %v, want %v (line %v)", key, line[key], value, line)
		}
	}
}

// CHAOS-12300 (the #1821 review F3): the entity table logs, at Info, how many
// work items it stated as members and how many in no project, by why: no
// membership row (missing) apart from rows that resolve to no single project
// (stale). Never folded into one "absent" count, and never an id.
func TestTheEntityTableLogsEachMembershipOutcome(t *testing.T) {
	var buffer bytes.Buffer
	defer devhealthsource.SwapMembershipOutcomeLoggerForTest(slog.New(slog.NewJSONHandler(&buffer, nil)))()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: chaos12226Tables(at, [][]any{
		membershipRow("repo-1", "WIDGET-101", "github", "P-1", 1),
		membershipRow(chaos12226Linear, "WIDGET-LINEAR", "linear", "X", 2),
	}, nil)}
	if _, err := chaos12226Batch(t, client, true); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	lines := membershipOutcomeLines(t, &buffer)
	assertOutcomes(t, lines["work_items"], map[string]float64{"work_items": 3, "member": 1, "stale": 1, "missing": 1, "cleared": 0})
	if strings.Contains(buffer.String(), "WIDGET") || strings.Contains(buffer.String(), "P-1") {
		t.Fatalf("the outcome line carries an id:\n%s", buffer.String())
	}
}

// The refresh table re-reads a work item after a membership change: no row
// any more is the loss of its last project (cleared), not missing.
func TestTheRefreshTableLogsAClearedMembership(t *testing.T) {
	var buffer bytes.Buffer
	defer devhealthsource.SwapMembershipOutcomeLoggerForTest(slog.New(slog.NewJSONHandler(&buffer, nil)))()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := chaos12226Tables(at, nil, nil)
	for i, table := range tables {
		if table.match == "FROM work_items AS w" {
			tables[i].rows = nil
		}
	}
	tables = append(tables, fakeTable{match: workItemMembershipRefreshMarker, rows: [][]any{
		{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime, "bug", "CHAOS", "Widget Reliability", []string{"checkout", "flake"}, at.Add(time.Hour)},
	}})
	if _, err := chaos12226Batch(t, &fakeClient{tables: tables}, true); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	lines := membershipOutcomeLines(t, &buffer)
	assertOutcomes(t, lines["work_items_project_membership"], map[string]float64{"work_items": 1, "member": 0, "stale": 0, "missing": 0, "cleared": 1})
	if _, ok := lines["work_items"]; ok {
		t.Fatalf("the entity table read no work item and still logged an outcome:\n%s", buffer.String())
	}
}
