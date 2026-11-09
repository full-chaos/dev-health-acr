package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

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
