package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// reported once, outcome caught_up.
	checkpoint.Cursor = batch.NextCursor
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 2: err=%v available=%v, want caught up", err, available)
	}
	lines := ignoredLines(t, buf.String())
	if len(lines) != 1 || lines[0]["ignored_count"] != float64(1) || lines[0]["pass_outcome"] != "caught_up" {
		t.Fatalf("caught-up flush = %v, want one line with ignored_count 1 and pass_outcome caught_up", lines)
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

// TestSkipPageYieldDoesNotFlushTheIgnoredLine (CHAOS-8290): a tick that skips
// its bound of fully-ignored pages yields without publishing, but the pass is
// not over. The documented shape is one INFO line per type per pass, so the
// line is held until the pass is caught up, and carries the whole pass's count.
func TestSkipPageYieldDoesNotFlushTheIgnoredLine(t *testing.T) {
	t.Parallel()
	const pageRows, maxSkips = 200, 50
	total := (maxSkips+1)*pageRows + 300 // one tick absorbs maxSkips+1 pages; 300 rows remain
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, unresolvedDependencyRow(fmt.Sprintf("WI-%05d", i), "EXT-1", "EXTERNAL_ISSUE_KEY", at.Add(time.Duration(i)*time.Millisecond), created))
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: dependencyTablesOnly(t, at, rows)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursor(t, at.Add(-time.Hour))}

	// Tick 1: yields after its skip bound, mid-pass.
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 1: err=%v available=%v, want a yield", err, available)
	}
	if n := len(ignoredLines(t, buf.String())); n != 0 {
		t.Fatalf("a skip-page yield flushed the ignored line (%d lines) before the pass was over", n)
	}
	progress, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint)
	if err != nil || !ok {
		t.Fatalf("tick 1 consumed progress: ok=%v err=%v", ok, err)
	}

	// The next ticks reach the end (the trailing overlap walk finishes): one
	// line, the whole pass's count.
	checkpoint.Cursor = progress.NextCursor
	// The window holds ~10.5k rows at 1000 rows per call: about 11 ticks. Each
	// tick persists consumed progress, as the worker does.
	firstLine := 0
	for tick := 2; tick <= 30; tick++ {
		if firstLine == 0 && len(ignoredLines(t, buf.String())) > 0 {
			firstLine = tick
		}
		if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
			t.Fatalf("tick %d: err=%v available=%v, want no batch", tick, err, available)
		}
		if progress, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint); err != nil {
			t.Fatalf("tick %d consumed progress: %v", tick, err)
		} else if ok {
			checkpoint.Cursor = progress.NextCursor
		}
	}
	// The overlap walk of this pass spans ~11 ticks; the line must wait for it.
	if firstLine != 0 && firstLine < 10 {
		t.Fatalf("the ignored line appeared at tick %d, before the overlap walk of the pass finished", firstLine)
	}
	lines := ignoredLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("ignored lines over the pass = %d, want exactly 1", len(lines))
	}
	if lines[0]["ignored_count"] != float64(total) || lines[0]["pass_outcome"] != "caught_up" {
		t.Fatalf("pass line = %v, want ignored_count %d and pass_outcome caught_up", lines[0], total)
	}
}

func ignoredCountSum(lines []map[string]any) (sum int) {
	for _, entry := range lines {
		sum += int(entry["ignored_count"].(float64))
	}
	return sum
}

// TestOverlapWindowContinuationDoesNotFlushTheIgnoredLine: the overlap walk
// is part of the pass. A call that consumes an all-ignored window page, or
// stops at its per-call page bound, leaves the walk unfinished, so the line
// is held until the walk ends and carries the whole count.
func TestOverlapWindowContinuationDoesNotFlushTheIgnoredLine(t *testing.T) {
	t.Parallel()
	const total = 401 // three window pages of 200
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, unresolvedDependencyRow(fmt.Sprintf("WI-%05d", i), "EXT-1", "EXTERNAL_ISSUE_KEY", at.Add(-20*time.Minute+time.Duration(i)*time.Millisecond), created))
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: dependencyTablesOnly(t, at, rows)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.SetWindowPagesPerCallForTest(1)
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursor(t, at)}
	for tick := 1; tick <= 8; tick++ {
		if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
			t.Fatalf("tick %d: err=%v available=%v, want no batch", tick, err, available)
		}
	}
	lines := ignoredLines(t, buf.String())
	if len(lines) != 1 || ignoredCountSum(lines) != total {
		t.Fatalf("ignored lines over the overlap walk = %d (sum %d), want exactly 1 carrying %d", len(lines), ignoredCountSum(lines), total)
	}
}

// TestRebuildDoesNotMixAnAbandonedPassCountIntoTheNewPass: a from-scratch
// call (a rebuild's reset checkpoint) abandons the pending pass. Its count is
// reported on its own line, pass_outcome abandoned, never carried into the new
// pass's count and never silently dropped.
func TestRebuildDoesNotMixAnAbandonedPassCountIntoTheNewPass(t *testing.T) {
	t.Parallel()
	// A blank cursor is a rebuild's reset checkpoint; a cursor with no
	// position space is a legacy one, which re-reads from the start.
	for name, resetCursor := range map[string]func(*testing.T, time.Time) string{
		"blank":  func(*testing.T, time.Time) string { return "" },
		"legacy": func(t *testing.T, at time.Time) string { return testCursor(t, at.Add(-time.Hour), "") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			abandonedPassIsReportedSeparately(t, resetCursor)
		})
	}
}

func abandonedPassIsReportedSeparately(t *testing.T, resetCursor func(*testing.T, time.Time) string) {
	const pageRows, maxSkips = 200, 50
	yielded := (maxSkips + 1) * pageRows
	total := yielded + 300
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, unresolvedDependencyRow(fmt.Sprintf("WI-%05d", i), "EXT-1", "EXTERNAL_ISSUE_KEY", at.Add(time.Duration(i)*time.Millisecond), created))
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: dependencyTablesOnly(t, at, rows)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursor(t, at.Add(-time.Hour))}
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 1: err=%v available=%v, want a yield", err, available)
	}
	if n := len(ignoredLines(t, buf.String())); n != 0 {
		t.Fatalf("yield flushed %d lines", n)
	}

	// The rebuild: a blank cursor. The abandoned pass is reported first.
	checkpoint.Cursor = resetCursor(t, at)
	if _, _, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil {
		t.Fatalf("rebuild tick: %v", err)
	}
	var abandoned []map[string]any
	for _, entry := range ignoredLines(t, buf.String()) {
		if entry["pass_outcome"] == "abandoned" {
			abandoned = append(abandoned, entry)
		} else if int(entry["ignored_count"].(float64)) > yielded {
			t.Fatalf("a line mixes the abandoned pass into the new one: %v", entry)
		}
	}
	if len(abandoned) != 1 || int(abandoned[0]["ignored_count"].(float64)) != yielded {
		t.Fatalf("abandoned lines = %v, want one with ignored_count %d", abandoned, yielded)
	}
}
