package projectionrun_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

type fakePurgeMarker struct {
	mu      sync.Mutex
	purged  map[string]bool
	readErr error
}

func newFakePurgeMarker(orgs ...string) *fakePurgeMarker {
	m := &fakePurgeMarker{purged: map[string]bool{}}
	for _, org := range orgs {
		m.purged[org] = true
	}
	return m
}

func (m *fakePurgeMarker) set(org string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purged[org] = true
}

func (m *fakePurgeMarker) IsPurged(_ context.Context, org string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return false, m.readErr
	}
	return m.purged[org], nil
}

func (m *fakePurgeMarker) ClearPurged(_ context.Context, orgs []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cleared []string
	for _, org := range orgs {
		if m.purged[org] {
			delete(m.purged, org)
			cleared = append(cleared, org)
		}
	}
	return cleared, nil
}

// purgeBetweenSelectAndLock models a purge that lands after a tick selected
// the organization and before the tick takes the organization lock: the
// purge removes the organization's rows and sets its marker, then releases.
type purgeBetweenSelectAndLock struct {
	checkpoints *fakeCheckpointStore
	marker      *fakePurgeMarker
	once        sync.Once
}

func (l *purgeBetweenSelectAndLock) Lock(_ context.Context, orgID string) (func() error, error) {
	l.once.Do(func() {
		l.checkpoints.mu.Lock()
		for key, cp := range l.checkpoints.data {
			if cp.OrgID == orgID {
				delete(l.checkpoints.data, key)
			}
		}
		l.checkpoints.mu.Unlock()
		l.marker.set(orgID)
	})
	return func() error { return nil }, nil
}

func TestPurgeFence_ATickThatSelectedTheOrgBeforeThePurgeCreatesNothingAfterIt(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	marker := newFakePurgeMarker()
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-purged"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		PurgeMarkers: marker, Locker: &purgeBetweenSelectAndLock{checkpoints: checkpoints, marker: marker},
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	if backend.appliedCount() != 0 {
		t.Fatalf("a purged organization must not be projected, applied=%d", backend.appliedCount())
	}
	warned := logLines(t, buffer, "context_fabric: projection tick skipped organizations fenced by a purge marker")
	if len(warned) != 1 || warned[0]["orgs_purge_fenced"] != float64(1) || warned[0]["level"] != "WARN" {
		t.Fatalf("a fenced organization must produce one WARN with a per-tick count, got %v", warned)
	}
	if strings.Contains(buffer.String(), "org-purged") {
		t.Fatal("the fence logs must carry the organization hash, not the id")
	}
	cp, err := checkpoints.LoadProjectionCheckpoint(context.Background(), "org-purged", "source-a")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if cp.Cursor != "" {
		t.Fatalf("a purged organization must not get a checkpoint row, cursor=%q", cp.Cursor)
	}
	checkpoints.mu.Lock()
	defer checkpoints.mu.Unlock()
	if len(checkpoints.data) != 0 {
		t.Fatalf("a purged organization must not recreate checkpoint rows, rows=%d", len(checkpoints.data))
	}
}

func TestPurgeFence_AMarkerReadFailureSkipsTheOrganization(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	marker := newFakePurgeMarker()
	marker.readErr = errors.New("marker store down")
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		PurgeMarkers: marker, DrainBatchBudget: -1, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	if backend.appliedCount() != 0 {
		t.Fatalf("an unreadable purge marker must fail closed, applied=%d", backend.appliedCount())
	}
}

func TestPurgeFence_AnOrgThatReturnsToDiscoveryIsClearedAndProjected(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	marker := newFakePurgeMarker("org-back")
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-other"}}, {orgs: []string{"org-other", "org-back"}}}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		PurgeMarkers: marker, DrainBatchBudget: -1, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	coordinator.Tick(ctx)
	if appliedOrgs(backend)["org-back"] != 0 {
		t.Fatalf("org-back is not discoverable yet: %v", appliedOrgs(backend))
	}
	if purged, _ := marker.IsPurged(ctx, "org-back"); !purged {
		t.Fatal("the marker must survive while the organization stays out of discovery")
	}
	coordinator.Tick(ctx)
	if appliedOrgs(backend)["org-back"] == 0 {
		t.Fatalf("a returned organization must be projected, applied=%v", appliedOrgs(backend))
	}
	if purged, _ := marker.IsPurged(ctx, "org-back"); purged {
		t.Fatal("the marker must be cleared once the organization returns to discovery")
	}
}
