package projectionrun_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// CHAOS-12630: a graph that reloads an older copy of itself (FalkorDB restarted
// from a snapshot older than its last writes) keeps a watermark node with the
// older batch's cursor, while the checkpoint in Postgres keeps the newer one. The
// projection must resume from the graph's own watermark, so the batches the graph
// lost are walked again. These tests drive the coordinator against the fakes; the
// real-store proof is chaos12630_graph_reload_live_test.go.

// steppedSource emits one batch per cursor step, c0 -> c1 -> ... -> c<last>, and
// records every cursor it was asked to read from. The batch from cN is the same
// whenever it is read, so a re-walk reproduces it exactly.
type steppedSource struct {
	name string
	last int

	mu    sync.Mutex
	asked []string
}

func (s *steppedSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	s.mu.Lock()
	s.asked = append(s.asked, checkpoint.Cursor)
	s.mu.Unlock()
	step := 0
	if checkpoint.Cursor != "" {
		step = int(checkpoint.Cursor[1] - '0')
	}
	if step >= s.last {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	return validBatch(checkpoint.OrgID, s.name, checkpoint.Cursor, "c"+string(rune('0'+step+1))), true, nil
}

func (s *steppedSource) askedFrom() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

type reloadRig struct {
	backend     *fakeBackend
	checkpoints *fakeCheckpointStore
	source      *steppedSource
	coordinator *projectionrun.Coordinator
	logs        *bytes.Buffer
}

func newReloadRig(t *testing.T, lifecycle ...bool) reloadRig {
	t.Helper()
	logs := &bytes.Buffer{}
	rig := reloadRig{backend: newFakeBackend(), checkpoints: newFakeCheckpointStore(), source: &steppedSource{name: "source-a", last: 3}, logs: logs}
	config := projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: rig.source}},
		Backend: rig.backend, Checkpoints: rig.checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Logger:           slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		DrainBatchBudget: -1,
	}
	// lifecycle: a lifecycle row serving its epoch (servingLifecycleStore), so the
	// coordinator takes the lifecycle path.
	if len(lifecycle) > 0 && lifecycle[0] {
		config.Lifecycle = servingLifecycleStore{}
		config.EpochCheckpoints = func(int64) contextfabric.ProjectionCheckpointStore { return rig.checkpoints }
	}
	coordinator, err := projectionrun.NewCoordinator(config)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	rig.coordinator = coordinator
	return rig
}

// project ticks until the source has no more batches, so the checkpoint and the
// graph both stand at c3.
func (r reloadRig) project(t *testing.T) {
	t.Helper()
	for i := 0; i < 4; i++ {
		r.coordinator.Tick(context.Background())
	}
	checkpoint, _ := r.checkpoints.LoadProjectionCheckpoint(context.Background(), "org-1", "source-a")
	if checkpoint.Cursor != "c3" || checkpoint.BackendWatermark != "c3" {
		t.Fatalf("precondition: checkpoint %q / %q, want c3 / c3", checkpoint.Cursor, checkpoint.BackendWatermark)
	}
}

// reload makes the graph what it was after the batch to c1: its watermark names
// that batch, and the later batches' rows are gone, as after a restart from that
// snapshot. The checkpoint is not touched.
func (r reloadRig) reload() {
	r.backend.mu.Lock()
	kept := r.backend.applied[:0]
	for _, batch := range r.backend.applied {
		if batch.NextCursor == "c1" {
			kept = append(kept, batch)
		}
	}
	r.backend.applied = kept
	r.backend.mu.Unlock()
	r.backend.setWatermark("org-1", "source-a", contextfabric.ProjectionWatermark{
		OrgID: "org-1", Source: "source-a", Cursor: "c1", SourceVersion: "test.v1", BackendWatermark: "c1",
	})
}

func (r reloadRig) appliedTo() []string {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	var out []string
	for _, batch := range r.backend.applied {
		out = append(out, batch.NextCursor)
	}
	return out
}

func TestAGraphThatReloadedAnOlderCopyResumesFromItsOwnWatermark(t *testing.T) {
	t.Parallel()
	rig := newReloadRig(t)
	rig.project(t)
	rig.reload()
	for i := 0; i < 3; i++ {
		rig.coordinator.Tick(context.Background())
	}
	if got := rig.appliedTo(); strings.Join(got, ",") != "c1,c2,c3" {
		t.Fatalf("graph holds batches to %v after the reload, want c1,c2,c3: the lost batches were not walked again", got)
	}
	asked := rig.source.askedFrom()
	if !containsAfter(asked, 4, "c1") {
		t.Fatalf("source asked from %v, want a read from c1 after the reload", asked)
	}
	checkpoint, _ := rig.checkpoints.LoadProjectionCheckpoint(context.Background(), "org-1", "source-a")
	if checkpoint.Cursor != "c3" || checkpoint.BackendWatermark != "c3" {
		t.Fatalf("checkpoint %q / %q after the re-walk, want c3 / c3", checkpoint.Cursor, checkpoint.BackendWatermark)
	}
	line := ""
	for _, l := range strings.Split(rig.logs.String(), "\n") {
		if strings.Contains(l, "the projection resumes from the graph's own watermark") {
			line = l
		}
	}
	if line == "" || !strings.Contains(line, `"level":"WARN"`) || strings.Contains(line, "org-1") {
		t.Fatalf("want one WARN naming the realign by org hash, got %q", line)
	}
}

func TestTheLifecyclePathResumesFromTheGraphsOwnWatermark(t *testing.T) {
	t.Parallel()
	rig := newReloadRig(t, true)
	rig.project(t)
	rig.reload()
	for i := 0; i < 3; i++ {
		rig.coordinator.Tick(context.Background())
	}
	if got := rig.appliedTo(); strings.Join(got, ",") != "c1,c2,c3" {
		t.Fatalf("graph holds batches to %v after the reload on the lifecycle path, want c1,c2,c3", got)
	}
}

// setCheckpoint writes the stored checkpoint as a test needs it.
func (r reloadRig) setCheckpoint(cursor, backendWatermark string) {
	r.checkpoints.mu.Lock()
	defer r.checkpoints.mu.Unlock()
	key := r.checkpoints.key("org-1", "source-a")
	checkpoint := r.checkpoints.data[key]
	checkpoint.Cursor, checkpoint.BackendWatermark = cursor, backendWatermark
	if cursor == "" {
		checkpoint.SourceVersion = ""
	}
	r.checkpoints.data[key] = checkpoint
}

// A checkpoint that names no batch (never projected, or reset for a rebuild) is
// not moved to the graph's watermark: the walk starts from zero, as a rebuild owes.
func TestACheckpointThatNamesNoBatchIsNotRealigned(t *testing.T) {
	t.Parallel()
	rig := newReloadRig(t)
	rig.project(t)
	rig.backend.setWatermark("org-1", "source-a", contextfabric.ProjectionWatermark{
		OrgID: "org-1", Source: "source-a", Cursor: "c3", SourceVersion: "test.v1", BackendWatermark: "c3",
	})
	rig.setCheckpoint("", "")
	before := len(rig.source.askedFrom())
	rig.coordinator.Tick(context.Background())
	if asked := rig.source.askedFrom()[before:]; len(asked) == 0 || asked[0] != "" {
		t.Fatalf("source asked from %v after a reset checkpoint, want a walk from zero", asked)
	}
}

// A realign with nothing left to walk happens once: the checkpoint takes the
// graph's watermark with its cursor, so the next tick finds them in step.
func TestARealignWithNothingLeftToWalkHappensOnce(t *testing.T) {
	t.Parallel()
	rig := newReloadRig(t)
	rig.project(t)
	rig.backend.setWatermark("org-1", "source-a", contextfabric.ProjectionWatermark{
		OrgID: "org-1", Source: "source-a", Cursor: "c3", SourceVersion: "test.v1", BackendWatermark: "c3",
	})
	rig.setCheckpoint("c3", "stale")
	rig.coordinator.Tick(context.Background())
	rig.coordinator.Tick(context.Background())
	if got := strings.Count(rig.logs.String(), "the projection resumes from the graph's own watermark"); got != 1 {
		t.Fatalf("realigned %d times, want once", got)
	}
	checkpoint, _ := rig.checkpoints.LoadProjectionCheckpoint(context.Background(), "org-1", "source-a")
	if checkpoint.Cursor != "c3" || checkpoint.BackendWatermark != "c3" {
		t.Fatalf("checkpoint %q / %q, want c3 / c3", checkpoint.Cursor, checkpoint.BackendWatermark)
	}
}

func TestAGraphInStepWithItsCheckpointIsNotRealigned(t *testing.T) {
	t.Parallel()
	rig := newReloadRig(t)
	rig.project(t)
	before := len(rig.source.askedFrom())
	rig.coordinator.Tick(context.Background())
	asked := rig.source.askedFrom()[before:]
	for _, cursor := range asked {
		if cursor != "c3" {
			t.Fatalf("source asked from %v on a graph in step, want only c3", asked)
		}
	}
	if strings.Contains(rig.logs.String(), "the graph's watermark differs") {
		t.Fatal("a graph in step with its checkpoint was reported as differing")
	}
}

func TestAWatermarkThatCannotBeReadDecidesNothing(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(*fakeBackend){
		"a read error": func(b *fakeBackend) {
			b.setWatermarkErr("org-1", "source-a", errors.New("graph unavailable"))
		},
		"a watermark with no batch": func(b *fakeBackend) {
			b.setWatermark("org-1", "source-a", contextfabric.ProjectionWatermark{OrgID: "org-1", Source: "source-a", SourceVersion: "test.v1"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newReloadRig(t)
			rig.project(t)
			set(rig.backend)
			rig.coordinator.Tick(context.Background())
			checkpoint, _ := rig.checkpoints.LoadProjectionCheckpoint(context.Background(), "org-1", "source-a")
			if checkpoint.Cursor != "c3" || checkpoint.BackendWatermark != "c3" {
				t.Fatalf("checkpoint moved to %q / %q on %s", checkpoint.Cursor, checkpoint.BackendWatermark, name)
			}
		})
	}
}

func containsAfter(cursors []string, from int, want string) bool {
	for i, cursor := range cursors {
		if i >= from && cursor == want {
			return true
		}
	}
	return false
}
