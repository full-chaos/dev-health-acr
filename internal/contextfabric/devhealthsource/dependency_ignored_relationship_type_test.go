package devhealthsource_test

import (
	"bytes"
	"context"
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
