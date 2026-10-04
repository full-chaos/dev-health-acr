package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// ingestSpaceCursor is testCursor in the current ingest position space. A
// cursor without a space is treated as a legacy one and re-read from the
// start, which would hide the overlap path under test.
func ingestSpaceCursor(t *testing.T, since time.Time) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Since time.Time `json:"since"`
		After string    `json:"after"`
		Space string    `json:"space"`
	}{Since: since, Space: "ingest.v1"})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// ignoredLines returns every documented-ignore INFO line in a captured log.
func ignoredLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" || !strings.Contains(line, ignoredLine) {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// TestLateIgnoredRowFoundByTheOverlapReReadIsCounted: a row that lands behind
// the frontier is judged for the FIRST time by the overlap re-read. If it is
// an ignored row it must reach ignored_count, or the summed count reads low
// against the rows actually ignored.
func TestLateIgnoredRowFoundByTheOverlapReReadIsCounted(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := at.Add(-10 * time.Minute)
	rows := [][]any{unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", late, created)}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: dependencyTablesOnly(t, at, rows)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursor(t, at)}

	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 1: err=%v available=%v, want no batch and no error", err, available)
	}
	lines := ignoredLines(t, buf.String())
	total := 0
	for _, entry := range lines {
		total += int(entry["ignored_count"].(float64))
	}
	if total != 1 {
		t.Fatalf("ignored_count over %d lines = %d, want 1 for the late ignored row", len(lines), total)
	}

	// The row is now judged: a later tick must not count it again.
	buf.Reset()
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 2: err=%v available=%v", err, available)
	}
	if n := len(ignoredLines(t, buf.String())); n != 0 {
		t.Fatalf("tick 2 re-counted the already judged late row (%d lines)", n)
	}
}
