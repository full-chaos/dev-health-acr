package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// summaryBackend applies every batch and remembers the last watermark.
type summaryBackend struct {
	mu         sync.Mutex
	applied    int
	watermarks map[string]contextfabric.ProjectionWatermark
}

func (b *summaryBackend) ApplyProjectionBatch(_ context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applied++
	b.watermarks[batch.Source] = contextfabric.ProjectionWatermark{
		OrgID: batch.OrgID, Source: batch.Source, SourceVersion: batch.SourceVersion, ProjectedAt: time.Now().UTC(), BackendWatermark: batch.NextCursor,
	}
	return contextfabric.ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Now().UTC(), BackendWatermark: batch.NextCursor, EntitiesApplied: len(batch.Entities)}, nil
}

func (b *summaryBackend) ProjectionWatermark(_ context.Context, _, source string) (contextfabric.ProjectionWatermark, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watermarks[source], nil
}

func (b *summaryBackend) PurgeOrganization(context.Context, string) error { return nil }

type summaryCheckpoints struct {
	mu   sync.Mutex
	data map[string]contextfabric.ProjectionCheckpoint
}

func (s *summaryCheckpoints) LoadProjectionCheckpoint(_ context.Context, org, source string) (contextfabric.ProjectionCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if checkpoint, ok := s.data[source]; ok {
		return checkpoint, nil
	}
	return contextfabric.ProjectionCheckpoint{OrgID: org, Source: source}, nil
}

func (s *summaryCheckpoints) CompareAndSwapProjectionCheckpoint(_ context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[expected.Source].Cursor != expected.Cursor {
		return contextfabric.ErrProjectionConflict
	}
	s.data[expected.Source] = updated
	return nil
}

type summaryRebuildMarker struct{}

func (summaryRebuildMarker) BeginRebuild(context.Context, string) error { return nil }
func (summaryRebuildMarker) IsRebuildInProgress(context.Context, string) (bool, error) {
	return false, nil
}
func (summaryRebuildMarker) CompleteRebuild(context.Context, string) error { return nil }

// The production source under the production coordinator. The source's window
// holds seven pages and it walks one page per tick, so its re-read is open at
// the end of every tick and, with a one-minute overlap, is older than its
// bound from the sixth tick. Each tick the source answers "nothing available".
// The tick summary must not count the organization ok while the open pass is
// overdue.
func TestTickSummaryIsNotOKWhileTheWindowPassIsOverdue(t *testing.T) {
	const orgID, overlap = "org-1", time.Minute
	start := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	rows := make([][]any, 0, 1400)
	for i := 0; i < 1400; i++ {
		at := start.Add(-100*time.Second + time.Duration(i)*60*time.Millisecond)
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		rows = append(rows, []any{id, "acme/repo-" + id[24:], "github", at, at, ""})
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: []fakeTable{{match: "FROM repos", rows: rows, cursorOf: repoCursorOf}}})
	if err != nil {
		t.Fatal(err)
	}
	if source, err = source.WithOverlap(overlap); err != nil {
		t.Fatal(err)
	}
	source.SetWindowPagesPerCallForTest(1)
	now := start
	source.SetClockForTest(func() time.Time { return now })
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	source.WithLogger(logger)
	backend := &summaryBackend{watermarks: map[string]contextfabric.ProjectionWatermark{}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{orgID},
		Sources: []projectionrun.SourcePair{{Name: devhealthsource.SourceName, Source: source}},
		Backend: backend, Checkpoints: &summaryCheckpoints{data: map[string]contextfabric.ProjectionCheckpoint{}},
		RebuildMarkers: summaryRebuildMarker{}, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	number := func(line map[string]any, key string) float64 {
		value, ok := line[key].(float64)
		if !ok {
			t.Fatalf("the tick summary has no numeric %q: %v", key, line)
		}
		return value
	}
	overdueTicks, youngTicks, drained := 0, 0, 0
	for tick := 1; tick <= 7; tick++ {
		if tick > 1 {
			now = now.Add(15 * time.Second)
		}
		logs.Reset()
		coordinator.Tick(context.Background())
		if tick == 1 {
			drained = backend.applied
		}
		var summary, pass map[string]any
		for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var line map[string]any
			if json.Unmarshal([]byte(raw), &line) != nil {
				continue
			}
			if line["msg"] == "context_fabric: projection tick freshness summary" {
				summary = line
			}
			if _, open := line["pass_age_seconds"]; open {
				pass = line
			}
		}
		if summary == nil || pass == nil {
			t.Fatalf("tick %d: summary line present=%v, open-pass line present=%v; the tick must end with the pass open:\n%s", tick, summary != nil, pass != nil, logs.String())
		}
		age := number(pass, "pass_age_seconds")
		ok := number(summary, "orgs_ok")
		t.Logf("tick %d: pass_age_seconds=%v orgs_ok=%v orgs_window_behind=%v orgs_window_pass_open=%v", tick, age, ok, summary["orgs_window_behind"], summary["orgs_window_pass_open"])
		if age > overlap.Seconds() {
			overdueTicks++
			if ok != 0 {
				t.Errorf("tick %d: the window pass is %v s old (bound %v s) and the summary says orgs_ok=%v", tick, age, overlap.Seconds(), ok)
				continue
			}
			if behind, open := number(summary, "orgs_window_behind"), number(summary, "orgs_window_pass_open"); behind != 1 || open != 1 {
				t.Errorf("tick %d: orgs_window_behind=%v orgs_window_pass_open=%v, want 1 and 1", tick, behind, open)
			}
			continue
		}
		youngTicks++
		if ok != 1 {
			t.Errorf("tick %d: the window pass is %v s old, inside its bound, and the summary says orgs_ok=%v", tick, age, ok)
		}
		if _, reported := summary["orgs_window_pass_open"]; reported {
			if behind, open := number(summary, "orgs_window_behind"), number(summary, "orgs_window_pass_open"); behind != 0 || open != 1 {
				t.Errorf("tick %d: orgs_window_behind=%v orgs_window_pass_open=%v, want 0 and 1", tick, behind, open)
			}
		}
	}
	if overdueTicks == 0 || youngTicks == 0 {
		t.Fatalf("ticks with an overdue pass=%d, with a pass inside its bound=%d: both cases must be executed", overdueTicks, youngTicks)
	}
	if drained < 7 || backend.applied != drained {
		t.Fatalf("precondition: %d batches applied by the first drain, %d by the end; want the 1,400 rows drained on tick 1 and nothing emitted again", drained, backend.applied)
	}
}
