package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type projectionSourceStub struct {
	batch     ProjectionBatch
	available bool
	err       error
}

func (s projectionSourceStub) NextProjectionBatch(context.Context, ProjectionCheckpoint) (ProjectionBatch, bool, error) {
	return s.batch, s.available, s.err
}

type projectionBackendStub struct {
	receipt ProjectionReceipt
	err     error
	applied int
}

func (s *projectionBackendStub) ApplyProjectionBatch(context.Context, ProjectionBatch) (ProjectionReceipt, error) {
	s.applied++
	return s.receipt, s.err
}

func (*projectionBackendStub) ProjectionWatermark(context.Context, string, string) (ProjectionWatermark, error) {
	return ProjectionWatermark{}, nil
}

func (*projectionBackendStub) PurgeOrganization(context.Context, string) error { return nil }

type checkpointStoreStub struct {
	checkpoint ProjectionCheckpoint
	saved      []ProjectionCheckpoint
	expected   []ProjectionCheckpoint
	compareErr error
}

func (s *checkpointStoreStub) LoadProjectionCheckpoint(context.Context, string, string) (ProjectionCheckpoint, error) {
	return s.checkpoint, nil
}

func (s *checkpointStoreStub) CompareAndSwapProjectionCheckpoint(_ context.Context, expected, checkpoint ProjectionCheckpoint) error {
	s.expected = append(s.expected, expected)
	if s.compareErr != nil {
		return s.compareErr
	}
	s.saved = append(s.saved, checkpoint)
	return nil
}

func TestProjectionWorkerAdvancesCheckpointOnlyAfterBackendAcceptance(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.Cursor = "cursor_1"
	batch.NextCursor = "cursor_2"
	backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_2"}}
	original := ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: "cursor_1"}
	checkpoints := &checkpointStoreStub{checkpoint: original}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{Now: func() time.Time { return time.Unix(60, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	run, err := worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !run.Applied || run.NextCursor != "cursor_2" {
		t.Fatalf("run = %#v", run)
	}
	if len(checkpoints.expected) != 1 || checkpoints.expected[0] != original {
		t.Fatalf("expected checkpoints = %#v", checkpoints.expected)
	}
	if len(checkpoints.saved) != 1 || checkpoints.saved[0].Cursor != "cursor_2" || checkpoints.saved[0].BackendWatermark != "backend_2" {
		t.Fatalf("saved checkpoints = %#v", checkpoints.saved)
	}
}

func TestProjectionWorkerDoesNotAdvanceCheckpointWhenBackendFails(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	backend := &projectionBackendStub{err: errors.New("backend unavailable")}
	checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: batch.Cursor}}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	_, err = worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if err == nil || !strings.Contains(err.Error(), "apply projection batch") {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(checkpoints.saved) != 0 || len(checkpoints.expected) != 0 {
		t.Fatalf("checkpoint writes = expected %#v saved %#v", checkpoints.expected, checkpoints.saved)
	}
}

func TestProjectionWorkerRejectsOutOfOrderBatch(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.Cursor = "unexpected"
	backend := &projectionBackendStub{}
	checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: "expected"}}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	_, err = worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("RunOnce() error = %v, want ErrProjectionConflict", err)
	}
	if backend.applied != 0 || len(checkpoints.saved) != 0 {
		t.Fatalf("backend applied = %d, saved = %#v", backend.applied, checkpoints.saved)
	}
}

func TestProjectionWorkerSurfacesConcurrentCheckpointConflict(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.Cursor = "cursor_1"
	batch.NextCursor = "cursor_2"
	backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_2"}}
	checkpoints := &checkpointStoreStub{
		checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: "cursor_1"},
		compareErr: ErrProjectionConflict,
	}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	_, err = worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("RunOnce() error = %v, want ErrProjectionConflict", err)
	}
	if backend.applied != 1 || len(checkpoints.saved) != 0 || len(checkpoints.expected) != 1 {
		t.Fatalf("backend applied = %d, expected = %#v, saved = %#v", backend.applied, checkpoints.expected, checkpoints.saved)
	}
}
