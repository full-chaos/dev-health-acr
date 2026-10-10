package devhealthsource_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// afterRecorder remembers the keyset "after" binding of every work_items read.
type afterRecorder struct {
	inner *fakeClient
	mu    sync.Mutex
	after []string
}

func (r *afterRecorder) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "FROM work_items AS w") {
		for _, binding := range bindings {
			if value, ok := binding.Value.(string); ok && binding.Name == "after" {
				r.mu.Lock()
				r.after = append(r.after, value)
				r.mu.Unlock()
			}
		}
	}
	return r.inner.Query(ctx, statement, bindings)
}

// readPast reports whether any work_items read started strictly after key.
func (r *afterRecorder) readPast(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, after := range r.after {
		if after > key {
			return true
		}
	}
	return false
}

const overlongRepoID = "2f6c1b9e-3a54-4c7d-9e21-8a0b5d7c4f13"

func overlongRows(at time.Time, ids ...string) [][]any {
	rows := make([][]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []any{id, overlongRepoID, "example-org/repo-000", id, "in_progress", "", at, at, uint8(0), zeroTime, "", "", "", []string{}})
	}
	return rows
}

func overlongSource(t *testing.T, at time.Time, log *bytes.Buffer, rows [][]any, recorder *afterRecorder) *devhealthsource.ClickHouseProjectionSource {
	t.Helper()
	tables := baseTables(at)
	for index, table := range tables {
		switch table.match {
		case "FROM work_items AS w":
			tables[index].rows = rows
			tables[index].cursorOf = workItemCursorOf
		default:
			tables[index].rows = nil
		}
	}
	recorder.inner = &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(recorder)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	return source.WithLogger(slog.New(slog.NewJSONHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func overlongLongID(prefix string) string { return overlongIDOfLength(prefix, 300) }

func overlongIDOfLength(prefix string, n int) string {
	return prefix + strings.Repeat("x", n-len(prefix))
}

// walk follows NextCursor until the source reports nothing more, returning the
// entity labels of every batch and the number of calls made.
func overlongWalk(t *testing.T, source *devhealthsource.ClickHouseProjectionSource, cursor string) (labels []string, calls int) {
	t.Helper()
	for ; calls < 20; calls++ {
		checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor}
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatalf("call %d: %v", calls, err)
		}
		if !available {
			return labels, calls
		}
		for _, e := range batch.Entities {
			labels = append(labels, e.Subject.Label)
		}
		cursor = batch.NextCursor
	}
	t.Fatalf("the walk did not finish in 20 calls")
	return nil, calls
}

func TestAnOverlongWorkItemIDLastOnThePageDoesNotFailTheBatch(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	var log bytes.Buffer
	var recorder afterRecorder
	longID := overlongLongID("Z")
	source := overlongSource(t, at, &log, overlongRows(at.Add(time.Second), "WIDGET-1", longID, "WIDGET-2"), &recorder)
	labels, _ := overlongWalk(t, source, testCursor(t, at.Add(-time.Hour), ""))
	if strings.Join(labels, ",") != "WIDGET-1,WIDGET-2" {
		t.Fatalf("entities = %v, want both normal work items", labels)
	}
	if !recorder.readPast(overlongRepoID + ":" + longID) {
		t.Fatalf("no read started past the over-long row: the cursor is stuck on it")
	}
	if !strings.Contains(log.String(), `"limit_bytes":512`) {
		t.Fatalf("no WARN names the cursor limit: %s", log.String())
	}
}

func TestAnOverlongWorkItemIDAloneAtTheFrontierIsPassedNotReRead(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	var log bytes.Buffer
	var recorder afterRecorder
	longID := overlongLongID("Z")
	source := overlongSource(t, at, &log, overlongRows(at.Add(time.Second), longID), &recorder)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: testCursor(t, at.Add(-time.Hour), "")}
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("first call: available=%v err=%v, want nothing to publish and no error", available, err)
	}
	consumed, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint)
	if err != nil || !ok {
		t.Fatalf("consumed progress: ok=%v err=%v, want a cursor recorded past the over-long row", ok, err)
	}
	next := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: consumed.NextCursor}
	if _, available, err := source.NextProjectionBatch(context.Background(), next); err != nil || available {
		t.Fatalf("next tick: available=%v err=%v, want nothing to publish and no error", available, err)
	}
	if !recorder.readPast(overlongRepoID + ":" + longID) {
		t.Fatalf("the next tick did not start past the over-long row: the cursor is stuck on it")
	}
}

func TestAnOverlongWorkItemIDMidPageKeepsEveryFittingRowAcrossPages(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 251)
	for i := 0; i < 250; i++ {
		ids = append(ids, fmt.Sprintf("WIDGET-%03d", i))
	}
	ids = append(ids, overlongLongID("WIDGET-198"))
	var log bytes.Buffer
	var recorder afterRecorder
	source := overlongSource(t, at, &log, overlongRows(at.Add(time.Second), ids...), &recorder)
	labels, _ := overlongWalk(t, source, testCursor(t, at.Add(-time.Hour), ""))
	seen := map[string]int{}
	for _, label := range labels {
		seen[label]++
	}
	for i := 0; i < 250; i++ {
		if got := seen[fmt.Sprintf("WIDGET-%03d", i)]; got != 1 {
			t.Fatalf("WIDGET-%03d projected %d times, want once", i, got)
		}
	}
	if len(labels) != 250 {
		t.Fatalf("projected %d entities, want the 250 fitting work items", len(labels))
	}
}

func TestAPageOfOnlyOverlongKeysIsWalkedPastToTheRowsBeyond(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 205)
	for i := 0; i < 200; i++ {
		ids = append(ids, overlongIDOfLength(fmt.Sprintf("A%03d", i), 262))
	}
	for i := 0; i < 5; i++ {
		ids = append(ids, fmt.Sprintf("WIDGET-%d", i))
	}
	var log bytes.Buffer
	var recorder afterRecorder
	source := overlongSource(t, at, &log, overlongRows(at.Add(time.Second), ids...), &recorder)
	labels, _ := overlongWalk(t, source, testCursor(t, at.Add(-time.Hour), ""))
	if len(labels) != 5 {
		t.Fatalf("entities = %v, want the 5 fitting work items", labels)
	}
	if !strings.Contains(log.String(), `"action":"deferred"`) || !strings.Contains(log.String(), `"max_key_bytes":296`) {
		t.Fatalf("no WARN names the cursor limit and the deferral: %.400s", log.String())
	}
}

func TestAnOverlongWorkItemIDLastInAFromScratchSnapshotDoesNotFailTheBatch(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	var log bytes.Buffer
	var recorder afterRecorder
	longID := overlongLongID("Z")
	source := overlongSource(t, at, &log, overlongRows(at.Add(time.Second), "WIDGET-1", longID), &recorder)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}
	batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v", available, err)
	}
	next := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: batch.NextCursor}
	if _, _, err := source.NextProjectionBatch(context.Background(), next); err != nil {
		t.Fatalf("next tick: %v", err)
	}
	if !recorder.readPast(overlongRepoID + ":" + longID) {
		t.Fatalf("the next tick did not start past the over-long row")
	}
}
