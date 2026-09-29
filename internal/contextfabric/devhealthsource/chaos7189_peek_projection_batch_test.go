package devhealthsource_test

import (
	"context"
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
