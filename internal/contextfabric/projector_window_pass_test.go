package contextfabric

import (
	"context"
	"testing"
	"time"
)

// windowReportingStub is a source that says where its trailing re-read
// stands, and records the checkpoints it was asked about.
type windowReportingStub struct {
	projectionSourceStub
	pass  ProjectionWindowPass
	asked *[]ProjectionCheckpoint
}

func (s windowReportingStub) ProjectionWindowPass(checkpoint ProjectionCheckpoint) ProjectionWindowPass {
	*s.asked = append(*s.asked, checkpoint)
	return s.pass
}

// The worker carries the source's pass on every run that did not fail: after
// "nothing available" and after an applied batch, including the first batch
// ever (whose claim rebuilds the checkpoint value), and always for the scope
// the store loaded -- the organization and the EPOCH of the checkpoint.
func TestProjectionWorkerCarriesTheWindowPassOfTheLoadedScope(t *testing.T) {
	t.Parallel()
	pass := ProjectionWindowPass{Open: true, StartedAt: time.Unix(100, 0).UTC(), Age: 16 * time.Minute, Bound: 15 * time.Minute, Overdue: true}
	batch := validProjectionBatch()
	for _, tc := range []struct {
		name          string
		available     bool
		storedVersion string
		cursor        string
	}{
		{"nothing available", false, batch.SourceVersion, batch.Cursor},
		{"a batch applied", true, batch.SourceVersion, batch.Cursor},
		{"the first batch ever applied (source version claimed first)", true, "", batch.Cursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var asked []ProjectionCheckpoint
			source := windowReportingStub{projectionSourceStub: projectionSourceStub{batch: batch, available: tc.available}, pass: pass, asked: &asked}
			backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_1"}}
			checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{
				OrgID: "org_1", Source: "dev-health-ops", Cursor: tc.cursor, SourceVersion: tc.storedVersion, Epoch: 7,
			}}
			worker, err := NewProjectionWorker(source, backend, checkpoints, ProjectionWorkerOptions{})
			if err != nil {
				t.Fatalf("NewProjectionWorker() error = %v", err)
			}
			run, err := worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
			if err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}
			if run.Applied != tc.available {
				t.Fatalf("precondition: run.Applied = %v, want %v", run.Applied, tc.available)
			}
			if run.WindowPass != pass {
				t.Errorf("run.WindowPass = %+v, want the source's pass %+v", run.WindowPass, pass)
			}
			if len(asked) != 1 || asked[0].OrgID != "org_1" || asked[0].Epoch != 7 {
				t.Errorf("the source was asked about %+v, want once, for org_1 at epoch 7", asked)
			}
		})
	}
}

// A source without the capability reports no pass, and a failed run none.
func TestProjectionWorkerReportsNoWindowPassWithoutTheCapabilityOrOnFailure(t *testing.T) {
	t.Parallel()
	batch := validProjectionBatch()
	stored := ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: batch.Cursor, SourceVersion: batch.SourceVersion}
	plain, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: false}, &projectionBackendStub{}, &checkpointStoreStub{checkpoint: stored}, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run, err := plain.RunOnce(context.Background(), "org_1", "dev-health-ops"); err != nil || run.WindowPass != (ProjectionWindowPass{}) {
		t.Fatalf("a source without the capability: run.WindowPass = %+v err = %v, want the zero value", run.WindowPass, err)
	}
	var asked []ProjectionCheckpoint
	failing := windowReportingStub{projectionSourceStub: projectionSourceStub{batch: batch, available: true}, pass: ProjectionWindowPass{Open: true, Overdue: true}, asked: &asked}
	worker, err := NewProjectionWorker(failing, &projectionBackendStub{err: ErrUnavailable}, &checkpointStoreStub{checkpoint: stored}, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run, err := worker.RunOnce(context.Background(), "org_1", "dev-health-ops"); err == nil || run.WindowPass != (ProjectionWindowPass{}) {
		t.Fatalf("a failed apply: run.WindowPass = %+v err = %v, want an error and the zero value", run.WindowPass, err)
	}
}

// catchUpReportingStub is a source that places a checkpoint's cursor on its
// clock, and records the checkpoints it was asked about.
type catchUpReportingStub struct {
	projectionSourceStub
	asked *[]ProjectionCheckpoint
}

func (s catchUpReportingStub) ProjectionCatchUp(checkpoint ProjectionCheckpoint) ProjectionCatchUp {
	*s.asked = append(*s.asked, checkpoint)
	return ProjectionCatchUp{CursorKnown: true, CursorAt: time.Unix(int64(len(checkpoint.Cursor)), 0).UTC(), PassOpen: true, PassEdge: time.Unix(999, 0).UTC()}
}

// The worker carries the source's catch-up on every run that did not fail,
// asked for the cursor THIS run left in the checkpoint (the batch's next
// cursor after an apply, the unchanged cursor otherwise) and for the scope
// the store loaded.
func TestProjectionWorkerCarriesTheCatchUpOfTheCursorItLeft(t *testing.T) {
	t.Parallel()
	batch := validProjectionBatch()
	batch.NextCursor = "cursor_2_longer"
	for _, tc := range []struct {
		name          string
		available     bool
		storedVersion string
		wantCursor    string
	}{
		{"nothing available", false, batch.SourceVersion, batch.Cursor},
		{"a batch applied", true, batch.SourceVersion, batch.NextCursor},
		{"the first batch ever applied (source version claimed first)", true, "", batch.NextCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var asked []ProjectionCheckpoint
			source := catchUpReportingStub{projectionSourceStub: projectionSourceStub{batch: batch, available: tc.available}, asked: &asked}
			backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_1"}}
			checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: batch.Cursor, SourceVersion: tc.storedVersion, Epoch: 7}}
			worker, err := NewProjectionWorker(source, backend, checkpoints, ProjectionWorkerOptions{})
			if err != nil {
				t.Fatalf("NewProjectionWorker() error = %v", err)
			}
			run, err := worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
			if err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}
			want := ProjectionCatchUp{CursorKnown: true, CursorAt: time.Unix(int64(len(tc.wantCursor)), 0).UTC(), PassOpen: true, PassEdge: time.Unix(999, 0).UTC()}
			if run.Applied != tc.available || run.CatchUp != want {
				t.Fatalf("run.Applied = %v, run.CatchUp = %+v; want %v and %+v", run.Applied, run.CatchUp, tc.available, want)
			}
			if len(asked) != 1 || asked[0].Cursor != tc.wantCursor || asked[0].OrgID != "org_1" || asked[0].Epoch != 7 {
				t.Errorf("the source was asked about %+v, want once: cursor %q, org_1, epoch 7", asked, tc.wantCursor)
			}
		})
	}
}

// A source without the capability reports no catch-up, and a failed run none.
func TestProjectionWorkerReportsNoCatchUpWithoutTheCapabilityOrOnFailure(t *testing.T) {
	t.Parallel()
	batch := validProjectionBatch()
	stored := ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: batch.Cursor, SourceVersion: batch.SourceVersion}
	plain, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "b"}}, &checkpointStoreStub{checkpoint: stored}, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run, err := plain.RunOnce(context.Background(), "org_1", "dev-health-ops"); err != nil || run.CatchUp != (ProjectionCatchUp{}) {
		t.Fatalf("a source without the capability: run.CatchUp = %+v err = %v, want the zero value", run.CatchUp, err)
	}
	var asked []ProjectionCheckpoint
	failing := catchUpReportingStub{projectionSourceStub: projectionSourceStub{batch: batch, available: true}, asked: &asked}
	worker, err := NewProjectionWorker(failing, &projectionBackendStub{err: ErrUnavailable}, &checkpointStoreStub{checkpoint: stored}, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run, err := worker.RunOnce(context.Background(), "org_1", "dev-health-ops"); err == nil || run.CatchUp != (ProjectionCatchUp{}) {
		t.Fatalf("a failed apply: run.CatchUp = %+v err = %v, want an error and the zero value", run.CatchUp, err)
	}
}
