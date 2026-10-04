package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// TestPendingIgnoredCountFlushPoints pins WHEN the ignored count is reported,
// which is also the window in which a restart loses it. The count is an
// in-memory, log-only ledger: a call that publishes a batch does NOT flush it
// (so one pass reports one line per type), the call that ends without
// publishing does (caught up, or an error).
func TestPendingIgnoredCountFlushPoints(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created),
		dependencyRow("WI-2", "WI-3", "blocks", at.Add(time.Second), created),
	}
	tables := dependencyTablesOnly(t, at, rows)
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursor(t, at.Add(-time.Hour))}

	// Tick 1 publishes a batch carrying the ignored row's page: nothing is
	// reported yet. This is the loss window (a restart here drops the count).
	batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil || !available {
		t.Fatalf("tick 1: err=%v available=%v, want a published batch", err, available)
	}
	if n := len(ignoredLines(t, buf.String())); n != 0 {
		t.Fatalf("a published batch flushed the ignored count (%d lines): one pass would report several lines", n)
	}

	// Tick 2 from the advanced cursor is caught up: the pending count is
	// reported once, outcome caught_up_or_yielded.
	checkpoint.Cursor = batch.NextCursor
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 2: err=%v available=%v, want caught up", err, available)
	}
	lines := ignoredLines(t, buf.String())
	if len(lines) != 1 || lines[0]["ignored_count"] != float64(1) || lines[0]["pass_outcome"] != "caught_up_or_yielded" {
		t.Fatalf("caught-up flush = %v, want one line with ignored_count 1 and pass_outcome caught_up_or_yielded", lines)
	}
}

// TestPendingIgnoredCountIsFlushedWhenTheCallErrors: a failing call must not
// carry the pending count into the next pass.
func TestPendingIgnoredCountIsFlushedWhenTheCallErrors(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created),
		dependencyRow("WI-2", "WI-3", "blocks", at.Add(time.Second), created),
	}
	tables := dependencyTablesOnly(t, at, rows)
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursor(t, at.Add(-time.Hour))}
	batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil || !available {
		t.Fatalf("tick 1: err=%v available=%v", err, available)
	}
	for i := range tables {
		tables[i].err = errors.New("synthetic read failure")
	}
	checkpoint.Cursor = batch.NextCursor
	if _, _, err := source.NextProjectionBatch(context.Background(), checkpoint); err == nil {
		t.Fatal("tick 2: want a read error")
	}
	lines := ignoredLines(t, buf.String())
	if len(lines) != 1 || lines[0]["ignored_count"] != float64(1) || lines[0]["pass_outcome"] != "error" {
		t.Fatalf("error flush = %v, want one line with ignored_count 1 and pass_outcome error", lines)
	}
}
