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

// CompareAndSwapProjectionCheckpoint persists a successful save into
// s.checkpoint (CHAOS-3779 codex round-3 M1), so a SECOND worker/RunOnce
// call sharing this same stub instance -- simulating a later tick over a
// real, durable checkpoint store -- observes what an earlier tick
// actually saved, including a claim write that happened before an apply
// failure. Every prior single-tick test only ever calls RunOnce once per
// stub instance, so this never changes their behavior.
func (s *checkpointStoreStub) CompareAndSwapProjectionCheckpoint(_ context.Context, expected, checkpoint ProjectionCheckpoint) error {
	s.expected = append(s.expected, expected)
	if s.compareErr != nil {
		return s.compareErr
	}
	s.saved = append(s.saved, checkpoint)
	s.checkpoint = checkpoint
	return nil
}

func TestProjectionWorkerAdvancesCheckpointOnlyAfterBackendAcceptance(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.Cursor = "cursor_1"
	batch.NextCursor = "cursor_2"
	backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_2"}}
	// SourceVersion matches batch.SourceVersion: this is an ALREADY-projected
	// organization on an unchanged source version, not the M1/H2-residual
	// first-run-or-claim case (that shape has its own dedicated tests
	// below) -- keeping this fixture realistic avoids tripping the
	// claim-write path and keeps this test's single-CAS-call assertion
	// meaningful.
	original := ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: "cursor_1", SourceVersion: batch.SourceVersion}
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
	// SourceVersion matches batch.SourceVersion -- an already-projected
	// organization, so the M1 claim-write path (which itself persists a
	// checkpoint before the backend is ever called -- see the dedicated
	// M1 tests below) does not interfere with this test's "nothing is
	// EVER saved on backend failure" assertion.
	checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: batch.Cursor, SourceVersion: batch.SourceVersion}}
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
	// SourceVersion matches batch.SourceVersion -- an already-projected
	// organization, so this test's compareErr fires on the FINAL
	// checkpoint-advance CAS (the concurrency conflict this test is
	// actually about), not on the M1 claim-write CAS a mismatched/empty
	// SourceVersion would otherwise trigger first.
	checkpoints := &checkpointStoreStub{
		checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: "cursor_1", SourceVersion: batch.SourceVersion},
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

// TestProjectionWorkerRefusesIncrementalAdvanceOnSourceVersionMismatch is
// CHAOS-3779 codex round-2's H2 residual: a producer's identity semantics
// can change between deploys (this issue's own RelationshipID change,
// which now embeds relationship_type -- see devhealthsource/tables.go).
// Without this check, RunOnce would apply a re-projected batch straight
// through: the backend MERGEs a NEW edge under the new identity beside
// whatever the OLD identity already wrote, both stay visible forever (a
// tombstone only ever deletes an exact ID it is told to, and nothing
// retargets the stale one), and the checkpoint advances as if nothing
// happened -- a graph that silently doubles its edges after this deploys,
// for every organization already projected under the old identity scheme,
// the next time any of its rows are re-observed.
//
// A checkpoint whose stored SourceVersion differs from the current batch's
// must refuse the incremental advance outright, loudly, with a named
// error -- exactly like the existing out-of-order-cursor and
// concurrent-checkpoint-conflict refusals above, neither of which trusts
// the caller to have gotten pagination or concurrency right either. The
// existing Rebuild sequence (projectionrun.Coordinator.performRebuild ->
// resetAllCheckpoints) already resets a checkpoint to its zero value
// (Cursor="", SourceVersion="") as a side effect of resetting it for any
// reason, so recovery needs no new machinery: an operator (or an
// automated policy, out of this test's scope) reruns the existing
// `rebuild --org` path, and the very next tick's checkpoint carries no
// stored SourceVersion to conflict with.
func TestProjectionWorkerRefusesIncrementalAdvanceOnSourceVersionMismatch(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.Cursor = "cursor_1"
	batch.NextCursor = "cursor_2"
	batch.SourceVersion = "devhealthsource.clickhouse.v2"
	backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_2"}}
	checkpoints := &checkpointStoreStub{
		// A non-empty Cursor AND a non-empty, DIFFERENT SourceVersion is
		// exactly what a real checkpoint looks like for an organization
		// already projected under the pre-CHAOS-3779 identity scheme.
		checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops", Cursor: "cursor_1", SourceVersion: "devhealthsource.clickhouse.v1"},
	}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	_, err = worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if !errors.Is(err, ErrProjectionSourceVersionChanged) {
		t.Fatalf("RunOnce() error = %v, want ErrProjectionSourceVersionChanged", err)
	}
	// The critical assertion: the backend must NEVER see this batch. A
	// version mismatch that still called ApplyProjectionBatch would
	// already have MERGEd the duplicate edge before the caller ever sees
	// the error -- refusing after the fact is not the same as refusing
	// before the fact.
	if backend.applied != 0 {
		t.Fatalf("backend.applied = %d, want 0 -- a source_version mismatch must refuse BEFORE the backend ever sees the batch, not merely fail to advance the checkpoint after", backend.applied)
	}
	if len(checkpoints.saved) != 0 {
		t.Fatalf("checkpoints.saved = %#v, want none -- the checkpoint must not advance past a version mismatch", checkpoints.saved)
	}
}

// TestProjectionWorkerAllowsFirstEverProjectionWithNoStoredSourceVersion
// guards the version-mismatch check above against a false positive: an
// organization with no prior checkpoint (LoadProjectionCheckpoint returns
// a zero-value ProjectionCheckpoint, per pgprojection's sql.ErrNoRows
// handling) has an empty stored SourceVersion, which is not a "mismatch"
// against anything -- it is simply the first run. The same zero value
// recurs immediately after Rebuild resets a checkpoint, so this also
// covers "just rebuilt, about to run its first post-rebuild batch."
func TestProjectionWorkerAllowsFirstEverProjectionWithNoStoredSourceVersion(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.Cursor = ""
	batch.NextCursor = "cursor_1"
	batch.SourceVersion = "devhealthsource.clickhouse.v2"
	backend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Unix(50, 0).UTC(), BackendWatermark: "backend_1"}}
	checkpoints := &checkpointStoreStub{
		checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops"}, // zero value: never projected, or just rebuilt
	}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	run, err := worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil -- an empty stored SourceVersion must never be treated as a mismatch", err)
	}
	if !run.Applied || backend.applied != 1 {
		t.Fatalf("run = %#v, backend.applied = %d, want the batch applied normally", run, backend.applied)
	}
}

// TestProjectionWorkerClaimSurvivesAnApplyFailureOnAnEmptyCheckpoint is
// CHAOS-3779 codex round-3 finding M1's first regression test. Probed
// first (temporary reproduction, deleted before this commit): a v1 batch
// whose apply fails partway left the checkpoint at its original empty
// SourceVersion, because pre-fix RunOnce only ever wrote a checkpoint
// AFTER a successful apply -- so a later, different-version batch over
// that same still-empty checkpoint sailed straight through the H2-residual
// guard (which intentionally treats an empty SourceVersion as "no
// mismatch," a first-run allowance) and applied, duplicating whatever the
// first attempt had partially written.
//
// This proves the claim half of the fix in isolation: even though the
// backend fails and RunOnce surfaces that failure, the claim -- written
// BEFORE ApplyProjectionBatch is ever called, cursor left unchanged from
// what was loaded -- is left durably saved.
func TestProjectionWorkerClaimSurvivesAnApplyFailureOnAnEmptyCheckpoint(t *testing.T) {
	t.Parallel()

	batch := validProjectionBatch()
	batch.SourceVersion = "devhealthsource.clickhouse.v1"
	batch.Cursor = ""
	batch.NextCursor = "cursor_1"
	backend := &projectionBackendStub{err: errors.New("simulated partial write failure partway through the batch")}
	checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops"}}
	worker, err := NewProjectionWorker(projectionSourceStub{batch: batch, available: true}, backend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}

	_, err = worker.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if err == nil || !strings.Contains(err.Error(), "apply projection batch") {
		t.Fatalf("RunOnce() error = %v, want the simulated apply failure to surface", err)
	}
	if backend.applied != 1 {
		t.Fatalf("backend.applied = %d, want 1 -- the apply must still have been attempted (and failed) after the claim", backend.applied)
	}
	if len(checkpoints.saved) != 1 {
		t.Fatalf("checkpoints.saved = %#v, want exactly one entry -- the claim, saved BEFORE the apply failure", checkpoints.saved)
	}
	claim := checkpoints.saved[0]
	if claim.SourceVersion != "devhealthsource.clickhouse.v1" {
		t.Fatalf("claim.SourceVersion = %q, want the batch's source version to have been claimed", claim.SourceVersion)
	}
	if claim.Cursor != "" {
		t.Fatalf("claim.Cursor = %q, want unchanged (empty) -- the claim records zero progress, only the version", claim.Cursor)
	}
}

// TestProjectionWorkerRefusesALaterDifferentVersionAfterAClaimSurvivedFailure
// is M1's second regression test, closing the loop the first test opens:
// after tick 1's claim survives an apply failure, tick 2 -- a DIFFERENT
// SourceVersion arriving on the same, still-durably-claimed checkpoint --
// must now be refused by the ordinary H2-residual mismatch check, exactly
// as if the org had been fully, successfully projected under v1 already.
// This is the actual hazard closing: a real backend never sees the v2
// batch, so it never duplicates whatever v1 partially wrote.
func TestProjectionWorkerRefusesALaterDifferentVersionAfterAClaimSurvivedFailure(t *testing.T) {
	t.Parallel()

	checkpoints := &checkpointStoreStub{checkpoint: ProjectionCheckpoint{OrgID: "org_1", Source: "dev-health-ops"}}

	v1Batch := validProjectionBatch()
	v1Batch.SourceVersion = "devhealthsource.clickhouse.v1"
	v1Batch.Cursor = ""
	v1Batch.NextCursor = "cursor_1"
	failingBackend := &projectionBackendStub{err: errors.New("simulated partial write failure partway through the batch")}
	worker1, err := NewProjectionWorker(projectionSourceStub{batch: v1Batch, available: true}, failingBackend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}
	if _, err := worker1.RunOnce(context.Background(), "org_1", "dev-health-ops"); err == nil {
		t.Fatal("tick1 setup invalid: expected the simulated backend failure to surface as an error")
	}
	if len(checkpoints.saved) != 1 {
		t.Fatalf("tick1 setup invalid: want the claim saved, got %#v", checkpoints.saved)
	}

	v2Batch := validProjectionBatch()
	v2Batch.SourceVersion = "devhealthsource.clickhouse.v2"
	v2Batch.Cursor = "" // the checkpoint's Cursor is still "" -- the claim recorded zero progress
	v2Batch.NextCursor = "cursor_1"
	succeedingBackend := &projectionBackendStub{receipt: ProjectionReceipt{BatchID: v2Batch.BatchID}}
	worker2, err := NewProjectionWorker(projectionSourceStub{batch: v2Batch, available: true}, succeedingBackend, checkpoints, ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker() error = %v", err)
	}
	_, err = worker2.RunOnce(context.Background(), "org_1", "dev-health-ops")
	if !errors.Is(err, ErrProjectionSourceVersionChanged) {
		t.Fatalf("tick2 RunOnce() error = %v, want ErrProjectionSourceVersionChanged -- the surviving v1 claim must refuse the v2 batch", err)
	}
	if succeedingBackend.applied != 0 {
		t.Fatalf("tick2 backend.applied = %d, want 0 -- the v2 batch must never reach the backend, or it would duplicate whatever v1 partially wrote", succeedingBackend.applied)
	}
}
