package devhealthsource_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// A peek on the real paging engine (assemble.go pagedBatch) is read-only: it
// reports availability, and the next NextProjectionBatch at the same
// checkpoint serves the identical page (same BatchID and NextCursor) as on a
// source that was never peeked. It records no consumed-progress memo.
func TestClickHouseProjectionSourcePeekIsNonConsuming(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	workItemAt := at.Add(time.Second)
	newSource := func(t *testing.T) *devhealthsource.ClickHouseProjectionSource {
		tables := baseTables(at)
		for index, table := range tables {
			switch table.match {
			case "FROM work_items AS w":
				tables[index].rows = [][]any{{"WIDGET-1", "repo-000", "example-org/repo-000", "Investigate checkout flake", "in_progress", "", workItemAt, workItemAt, uint8(0), zeroTime, "", "", "", []string{}}}
				tables[index].cursorOf = workItemCursorOf
			default:
				tables[index].rows = nil
			}
		}
		source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
		if err != nil {
			t.Fatalf("new source: %v", err)
		}
		return source
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: testCursor(t, at.Add(-time.Hour), "")}
	ctx := context.Background()

	var _ contextfabric.ProjectionPeeker = newSource(t)
	control, controlAvailable, err := newSource(t).NextProjectionBatch(ctx, checkpoint)
	if err != nil || !controlAvailable {
		t.Fatalf("control batch: available=%v err=%v", controlAvailable, err)
	}

	source := newSource(t)
	for i := 0; i < 3; i++ {
		available, err := source.PeekProjectionBatch(ctx, checkpoint)
		if err != nil || !available {
			t.Fatalf("peek %d: available=%v err=%v", i, available, err)
		}
	}
	batch, available, err := source.NextProjectionBatch(ctx, checkpoint)
	if err != nil || !available {
		t.Fatalf("batch after peeks: available=%v err=%v", available, err)
	}
	if batch.BatchID != control.BatchID || batch.NextCursor != control.NextCursor || len(batch.Entities) != len(control.Entities) {
		t.Fatalf("peeked source served a different page: %s/%s vs control %s/%s", batch.BatchID, batch.NextCursor, control.BatchID, control.NextCursor)
	}

	// Past the last page a peek reports unavailable and leaves no memo.
	after := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: batch.NextCursor}
	available, err = source.PeekProjectionBatch(ctx, after)
	if err != nil || available {
		t.Fatalf("peek past the final page: available=%v err=%v, want false", available, err)
	}
	if _, ok, err := source.ConsumedWithoutPublishing(ctx, after); err != nil || ok {
		t.Fatalf("a peek must not record consumed progress: ok=%v err=%v", ok, err)
	}
}

// CHAOS-7199: the blank-cursor peek takes the fullSnapshot branch of the
// paging engine (nextBatch, Cursor == ""). Its stateful hooks are nilled by
// PeekProjectionBatch as on the paged path; this pins that branch. A peek
// from a blank cursor must (a) not drop a consumed-progress memo, which the
// snapshot success path would (forgetConsumed), (b) emit no quarantine
// telemetry, and (c) leave the next real call serving the same batch as a
// never-peeked source. NextProjectionBatch itself forgets the memo at a blank
// cursor; the peek must not.
func TestClickHouseProjectionSourcePeekFromBlankCursorIsNonConsuming(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created)}
	newSource := func(t *testing.T, buf *bytes.Buffer) *devhealthsource.ClickHouseProjectionSource {
		source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: dependencyTablesOnly(t, at, rows)})
		if err != nil {
			t.Fatalf("new source: %v", err)
		}
		return source.WithLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	ctx := context.Background()
	blank := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ""}

	var controlLog bytes.Buffer
	control, controlAvailable, err := newSource(t, &controlLog).NextProjectionBatch(ctx, blank)
	if err != nil || !controlAvailable {
		t.Fatalf("control snapshot: available=%v err=%v", controlAvailable, err)
	}

	var log bytes.Buffer
	source := newSource(t, &log)
	// Record a memo for a NON-blank checkpoint (an all-quarantined tail).
	tail := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: testCursor(t, at.Add(-time.Hour), "")}
	if _, available, err := source.NextProjectionBatch(ctx, tail); err != nil || available {
		t.Fatalf("tail tick: available=%v err=%v, want no batch", available, err)
	}
	logged := strings.Count(log.String(), quarantineLine)
	if logged == 0 {
		t.Fatal("setup: the tail tick quarantined nothing, so the telemetry half of this test cannot fail")
	}

	for i := 0; i < 2; i++ {
		available, err := source.PeekProjectionBatch(ctx, blank)
		if err != nil || !available {
			t.Fatalf("blank peek %d: available=%v err=%v", i, available, err)
		}
	}
	if got := strings.Count(log.String(), quarantineLine); got != logged {
		t.Fatalf("a blank-cursor peek emitted quarantine telemetry: %d lines, want %d", got, logged)
	}
	if _, ok, err := source.ConsumedWithoutPublishing(ctx, tail); err != nil || !ok {
		t.Fatalf("a blank-cursor peek dropped the consumed-progress memo: ok=%v err=%v", ok, err)
	}

	batch, available, err := source.NextProjectionBatch(ctx, blank)
	if err != nil || !available {
		t.Fatalf("batch after blank peeks: available=%v err=%v", available, err)
	}
	if batch.BatchID != control.BatchID || batch.NextCursor != control.NextCursor || len(batch.Entities) != len(control.Entities) || !batch.FullSnapshot {
		t.Fatalf("peeked source served a different snapshot: %s/%s vs control %s/%s", batch.BatchID, batch.NextCursor, control.BatchID, control.NextCursor)
	}
}
