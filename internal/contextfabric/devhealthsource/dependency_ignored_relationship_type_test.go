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

const ignoredLine = "context_fabric: projection rows ignored by documented relationship type"

func projectCapturingAllLogs(t *testing.T, tables []fakeTable, cursor string) (contextfabric.ProjectionBatch, bool, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source := quarantineSourceFor(t, tables).WithLogger(logger)
	batch, available, err := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		lines = append(lines, entry)
	}
	return batch, available, lines
}

func TestExternalIssueKeyRelationshipsAreIgnoredWithoutQuarantine(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created),
		unresolvedDependencyRow("WI-2", "EXT-2", " external_issue_key ", at.Add(time.Second), created),
	}
	batch, available, lines := projectCapturingAllLogs(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))

	if available {
		if len(batch.Relationships) != 0 {
			t.Fatalf("an external_issue_key row produced %d edges", len(batch.Relationships))
		}
		for _, entity := range batch.Entities {
			if entity.Subject.Kind == "work_item_ref" {
				t.Fatalf("an external_issue_key row produced a stub node: %+v", entity.Subject)
			}
		}
	}
	ignored, ignoredLines := 0, 0
	for _, entry := range lines {
		msg, _ := entry["msg"].(string)
		if strings.Contains(msg, quarantineLine) {
			t.Fatalf("external_issue_key was quarantined: %v", entry)
		}
		if entry["level"] == "WARN" {
			t.Fatalf("unexpected WARN: %v", entry)
		}
		if strings.Contains(msg, ignoredLine) {
			if entry["ignored_relationship_type"] != "external_issue_key" {
				t.Fatalf("ignored type = %v", entry["ignored_relationship_type"])
			}
			ignoredLines++
			if entry["level"] != "INFO" {
				t.Fatalf("ignored line level = %v, want INFO", entry["level"])
			}
			if entry["pass_outcome"] == nil || entry["pass_outcome"] == "" {
				t.Fatalf("ignored line carries no pass_outcome: %v", entry)
			}
			ignored += int(entry["ignored_count"].(float64))
		}
	}
	if ignoredLines != 1 {
		t.Fatalf("ignored lines = %d, want one per run", ignoredLines)
	}
	if ignored != len(rows) {
		t.Fatalf("ignored count = %d, want %d", ignored, len(rows))
	}
}

func TestUnmappedRelationshipTypeStillQuarantinesWithWarn(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{unresolvedDependencyRow("WI-1", "EXT-1", "UNMAPPED_TEST_TYPE", at, created)}
	_, _, lines := projectCapturingAllLogs(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	for _, entry := range lines {
		msg, _ := entry["msg"].(string)
		if strings.Contains(msg, quarantineLine) && entry["level"] == "WARN" && entry["quarantine_reason"] == "unknown_relationship_type" {
			return
		}
	}
	t.Fatalf("no unknown_relationship_type WARN in %v", lines)
}

// TestIgnoredOnlyPageAdvancesTheCursorPastTheIgnoredRows pins the progress
// contract of a documented ignore: a page whose rows are ALL ignored publishes
// nothing, so the only way the checkpoint moves is the consumed-progress memo.
// A stall here re-reads and re-ignores the same tail on every tick forever.
func TestIgnoredOnlyPageAdvancesTheCursorPastTheIgnoredRows(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	last := at.Add(time.Second)
	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created),
		unresolvedDependencyRow("WI-2", "EXT-2", "EXTERNAL_ISSUE_KEY", last, created),
	}
	start := testCursor(t, at.Add(-time.Hour), "")
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: dependencyTablesOnly(t, at, rows)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: start}

	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 1: err=%v available=%v, want no batch and no error", err, available)
	}
	progress, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("consumed progress: %v", err)
	}
	if !ok {
		t.Fatal("no consumed progress for an ignored-only page: the checkpoint cannot advance")
	}
	raw, err := base64.RawURLEncoding.DecodeString(progress.NextCursor)
	if err != nil {
		t.Fatalf("decode NextCursor: %v", err)
	}
	var state struct {
		Since time.Time `json:"since"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode NextCursor state: %v", err)
	}
	if !state.Since.Equal(last) {
		t.Fatalf("NextCursor since = %s, want the last ignored row %s", state.Since, last)
	}

	buf.Reset()
	advanced := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: progress.NextCursor}
	if _, available, err := source.NextProjectionBatch(context.Background(), advanced); err != nil || available {
		t.Fatalf("tick 2: err=%v available=%v, want no batch and no error", err, available)
	}
	if n := strings.Count(buf.String(), ignoredLine); n != 0 {
		t.Fatalf("tick 2 re-ignored rows (%d lines): the cursor did not pass them", n)
	}
}
