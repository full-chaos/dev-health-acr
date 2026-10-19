package devhealthsource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

type versionCountingBackend struct {
	contextfabric.ProjectionBackend
	applied int
}

func (b *versionCountingBackend) ApplyProjectionBatch(context.Context, contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.applied++
	return contextfabric.ProjectionReceipt{}, nil
}

type heldVersionCheckpoint struct {
	checkpoint contextfabric.ProjectionCheckpoint
}

func (h *heldVersionCheckpoint) LoadProjectionCheckpoint(context.Context, string, string) (contextfabric.ProjectionCheckpoint, error) {
	return h.checkpoint, nil
}

func (h *heldVersionCheckpoint) CompareAndSwapProjectionCheckpoint(_ context.Context, _, next contextfabric.ProjectionCheckpoint) error {
	h.checkpoint = next
	return nil
}

// An organization already projected under the previous source version holds
// stale PART_OF edges (v15) and outranked lower-tier LINKS_PULL_REQUEST edges
// (v16) no incremental read removes, and unread epic parents (v17): the worker must refuse its
// held checkpoint and demand a rebuild, and must not apply a batch first.
func TestACheckpointFromThePreviousSourceVersionDemandsARebuild(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: baseTables(at)})
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil || !ok {
		t.Fatalf("first batch: ok=%v err=%v", ok, err)
	}
	for _, c := range []struct {
		name, held string
		want       bool
	}{
		{"the previous version", "devhealthsource.clickhouse.v18", true},
		{"an earlier version", "devhealthsource.clickhouse.v17", true},
		{"an older version", "devhealthsource.clickhouse.v16", true},
		{"the current version", devhealthsource.ClickHouseSourceVersion, false},
	} {
		backend := &versionCountingBackend{}
		store := &heldVersionCheckpoint{checkpoint: contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: first.NextCursor, SourceVersion: c.held}}
		worker, err := contextfabric.NewProjectionWorker(source, backend, store, contextfabric.ProjectionWorkerOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, runErr := worker.RunOnce(context.Background(), "org-1", devhealthsource.SourceName)
		if got := errors.Is(runErr, contextfabric.ErrProjectionSourceVersionChanged); got != c.want {
			t.Errorf("a held checkpoint at %s: source-version-changed=%v (err %v), want %v; the producer is at %s", c.name, got, runErr, c.want, devhealthsource.ClickHouseSourceVersion)
		}
	}
}
