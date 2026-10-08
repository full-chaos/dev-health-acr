package projectionrun_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// TestLivenessCheckStaysLiveWhileColdOrgFirstProjectionDrains samples the
// readiness leg (LivenessCheck) continuously while a cold org drains a
// multi-batch first projection, including a tick whose context is cut
// mid-drain and a second tick that resumes.
func TestLivenessCheckStaysLiveWhileColdOrgFirstProjectionDrains(t *testing.T) {
	t.Parallel()
	const pages = 60

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	source := &fakeSource{name: "source-a", pages: pages}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-cold"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(), Logger: discardLogger(),
		DrainBatchBudget: pages,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	var samples, notLive atomic.Int64
	var firstErr atomic.Value
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			samples.Add(1)
			if err := coordinator.LivenessCheck(context.Background()); err != nil {
				notLive.Add(1)
				firstErr.CompareAndSwap(nil, err.Error())
			}
		}
	}()

	tickCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	coordinator.Tick(tickCtx)
	cancel()
	partial := backend.appliedCount()
	coordinator.Tick(context.Background())
	close(stop)
	wg.Wait()

	if partial == 0 || partial >= pages {
		t.Fatalf("fixture must cut the first tick mid-drain: applied %d of %d", partial, pages)
	}
	if got := backend.appliedCount(); got != pages {
		t.Fatalf("second tick must finish the backlog: applied %d of %d", got, pages)
	}
	if n := notLive.Load(); n != 0 {
		t.Fatalf("LivenessCheck was not live in %d of %d samples during first projection: %v", n, samples.Load(), firstErr.Load())
	}
}
