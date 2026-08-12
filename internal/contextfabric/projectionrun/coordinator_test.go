package projectionrun_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func validBatch(orgID, source, cursor, nextCursor string) contextfabric.ProjectionBatch {
	now := time.Now().UTC()
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_%s_%s_%s", orgID, source, nextCursor),
		OrgID: orgID, Source: source, SourceVersion: "test.v1", Cursor: cursor, NextCursor: nextCursor, GeneratedAt: now,
		Entities: []contractsv1.ContextFabricEntityProjection{{
			Subject:        contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:test-" + nextCursor, Label: "test"},
			Authorization:  contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{"org/repo"}},
			EvidenceRefIDs: []string{"acr:v1:test:" + nextCursor}, ObservedAt: now, SourceVersion: "test.v1",
		}},
		Relationships: []contractsv1.ContextFabricRelationshipProjection{}, Contents: []contractsv1.ContextFabricContentProjection{},
		Episodes: []contractsv1.ContextFabricEpisodeProjection{}, Tombstones: []contractsv1.ContextFabricProjectionTombstone{},
	}
}

// fakeSource always produces one fresh, cursor-matching batch per call
// (unless told to fail), so a real contextfabric.ProjectionWorker.RunOnce
// against it always succeeds. It is shared across every organization the
// coordinator schedules (matching production, where one
// devhealthsource.ClickHouseProjectionSource instance serves every org), so
// its call counter must be atomic.
type fakeSource struct {
	name  string
	delay time.Duration
	err   error
	calls atomic.Int32
}

func (f *fakeSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return contextfabric.ProjectionBatch{}, false, ctx.Err()
		}
	}
	if f.err != nil {
		return contextfabric.ProjectionBatch{}, false, f.err
	}
	next := checkpoint.Cursor + "n"
	return validBatch(checkpoint.OrgID, f.name, checkpoint.Cursor, next), true, nil
}

// fakeBackend applies batches and tracks, per organization, the maximum
// number of concurrently in-flight ApplyProjectionBatch calls -- the
// single-flight-per-organization amendment requires this to never exceed 1.
type fakeBackend struct {
	mu          sync.Mutex
	inFlight    map[string]int
	maxInFlight map[string]int
	applied     []contextfabric.ProjectionBatch
	failOrgs    map[string]bool
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{inFlight: map[string]int{}, maxInFlight: map[string]int{}, failOrgs: map[string]bool{}}
}

func (b *fakeBackend) ApplyProjectionBatch(ctx context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.mu.Lock()
	b.inFlight[batch.OrgID]++
	if b.inFlight[batch.OrgID] > b.maxInFlight[batch.OrgID] {
		b.maxInFlight[batch.OrgID] = b.inFlight[batch.OrgID]
	}
	fail := b.failOrgs[batch.OrgID]
	b.mu.Unlock()

	time.Sleep(5 * time.Millisecond) // widen the race window on purpose

	b.mu.Lock()
	b.inFlight[batch.OrgID]--
	if !fail {
		b.applied = append(b.applied, batch)
	}
	b.mu.Unlock()
	if fail {
		return contextfabric.ProjectionReceipt{}, fmt.Errorf("%w: fake backend failure", contextfabric.ErrUnavailable)
	}
	return contextfabric.ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Now().UTC(), BackendWatermark: batch.NextCursor}, nil
}

func (b *fakeBackend) ProjectionWatermark(context.Context, string, string) (contextfabric.ProjectionWatermark, error) {
	return contextfabric.ProjectionWatermark{}, nil
}
func (b *fakeBackend) PurgeOrganization(context.Context, string) error { return nil }

func (b *fakeBackend) maxConcurrent(orgID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxInFlight[orgID]
}

func (b *fakeBackend) appliedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.applied)
}

type fakeCheckpointStore struct {
	mu   sync.Mutex
	data map[string]contextfabric.ProjectionCheckpoint
}

func newFakeCheckpointStore() *fakeCheckpointStore {
	return &fakeCheckpointStore{data: map[string]contextfabric.ProjectionCheckpoint{}}
}

func (s *fakeCheckpointStore) key(org, source string) string { return org + "\x00" + source }

func (s *fakeCheckpointStore) LoadProjectionCheckpoint(_ context.Context, org, source string) (contextfabric.ProjectionCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cp, ok := s.data[s.key(org, source)]; ok {
		return cp, nil
	}
	return contextfabric.ProjectionCheckpoint{OrgID: org, Source: source}, nil
}

func (s *fakeCheckpointStore) CompareAndSwapProjectionCheckpoint(_ context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.key(expected.OrgID, expected.Source)
	current := s.data[key].Cursor
	if current != expected.Cursor {
		return contextfabric.ErrProjectionConflict
	}
	s.data[key] = updated
	return nil
}

func TestCoordinatorEnforcesSingleFlightPerOrganizationAcrossOverlappingTicks(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: &fakeSource{name: "source-a", delay: 10 * time.Millisecond}},
			{Name: "source-b", Source: &fakeSource{name: "source-b", delay: 10 * time.Millisecond}},
		},
		Backend: backend, Checkpoints: checkpoints, Concurrency: 8, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	// Simulate a slow tick still running when the next poll fires: two
	// overlapping Tick calls for the same organization. Without the
	// amendment's guarantee this races two sources' ApplyProjectionBatch
	// calls for org-1 concurrently.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			coordinator.Tick(ctx)
		}()
	}
	wg.Wait()
	if max := backend.maxConcurrent("org-1"); max > 1 {
		t.Fatalf("single-flight per organization was violated: max concurrent ApplyProjectionBatch = %d", max)
	}
	if backend.appliedCount() == 0 {
		t.Fatal("expected at least one batch to have been applied")
	}
}

func TestCoordinatorIsolatesFailureToItsOwnPair(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.failOrgs["org-fails"] = true
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:      []string{"org-fails", "org-ok"},
		Sources:     []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:     backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	okCheckpoint, err := checkpoints.LoadProjectionCheckpoint(context.Background(), "org-ok", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if okCheckpoint.Cursor == "" {
		t.Fatal("org-ok's checkpoint must have advanced despite org-fails' failure")
	}
	failedCheckpoint, err := checkpoints.LoadProjectionCheckpoint(context.Background(), "org-fails", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if failedCheckpoint.Cursor != "" {
		t.Fatal("a failed backend write must never advance the checkpoint")
	}
}

func TestCoordinatorBacksOffAFailingPairThenRetriesLater(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.failOrgs["org-1"] = true
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	coordinator.Tick(ctx)
	coordinator.Tick(ctx)
	if source.calls.Load() != 1 {
		t.Fatalf("expected the second, immediately-following tick to be backed off; source calls = %d", source.calls.Load())
	}
}

func TestCoordinatorSkipsAnOrganizationLockedByAnotherReplica(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	locker := &denyingLocker{denied: map[string]bool{"org-locked-elsewhere": true}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:      []string{"org-locked-elsewhere", "org-free"},
		Sources:     []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:     backend, Checkpoints: checkpoints, Locker: locker, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	if backend.appliedCount() != 1 {
		t.Fatalf("expected exactly one applied batch (org-free only), got %d", backend.appliedCount())
	}
	locked, err := checkpoints.LoadProjectionCheckpoint(context.Background(), "org-locked-elsewhere", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if locked.Cursor != "" {
		t.Fatal("a locked-elsewhere organization must not have been projected")
	}
}

func TestCoordinatorRunStopsOnCancellation(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, PollInterval: time.Hour, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := coordinator.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
}

type denyingLocker struct{ denied map[string]bool }

func (l *denyingLocker) Lock(_ context.Context, orgID string) (func() error, error) {
	if l.denied[orgID] {
		return nil, projectionrun.ErrOrgLocked
	}
	return func() error { return nil }, nil
}

func TestPairBackoffKeyStaysDistinctPerSource(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.failOrgs["org-1"] = true
	checkpoints := newFakeCheckpointStore()
	sourceA, sourceB := &fakeSource{name: "source-a"}, &fakeSource{name: "source-b"}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: sourceA}, {Name: "source-b", Source: sourceB},
		},
		Backend: backend, Checkpoints: checkpoints, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	if sourceA.calls.Load() != 1 || sourceB.calls.Load() != 1 {
		t.Fatalf("expected both sources to run once regardless of the other's failure: a=%d b=%d", sourceA.calls.Load(), sourceB.calls.Load())
	}
}

func TestNewCoordinatorRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()
	if _, err := projectionrun.NewCoordinator(projectionrun.Config{}); err == nil {
		t.Fatal("expected an error for a config with no backend/checkpoints/sources")
	}
}
