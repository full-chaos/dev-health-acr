package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ProjectionWorkerOptions struct {
	PollInterval time.Duration
	Now          func() time.Time
}

type ProjectionWorker struct {
	source      ProjectionSource
	backend     ProjectionBackend
	checkpoints ProjectionCheckpointStore
	poll        time.Duration
	now         func() time.Time
}

type ProjectionRun struct {
	BatchID          string
	Source           string
	PreviousCursor   string
	NextCursor       string
	BackendWatermark string
	Applied          bool
	AppliedAt        time.Time
}

func NewProjectionWorker(source ProjectionSource, backend ProjectionBackend, checkpoints ProjectionCheckpointStore, options ProjectionWorkerOptions) (*ProjectionWorker, error) {
	if source == nil || backend == nil || checkpoints == nil {
		return nil, errors.New("projection worker requires source, backend, and checkpoint store")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 15 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &ProjectionWorker{source: source, backend: backend, checkpoints: checkpoints, poll: options.PollInterval, now: options.Now}, nil
}

// RunOnce processes at most one canonical projection batch. A checkpoint is
// advanced only after the selected backend has durably accepted the batch and
// only when the durable checkpoint still matches the cursor this worker read.
func (w *ProjectionWorker) RunOnce(ctx context.Context, orgID, sourceName string) (ProjectionRun, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(sourceName) == "" {
		return ProjectionRun{}, errors.New("projection worker requires organization and source")
	}
	checkpoint, err := w.checkpoints.LoadProjectionCheckpoint(ctx, orgID, sourceName)
	if err != nil {
		return ProjectionRun{}, fmt.Errorf("load projection checkpoint: %w", err)
	}
	batch, available, err := w.source.NextProjectionBatch(ctx, checkpoint)
	if err != nil {
		return ProjectionRun{}, fmt.Errorf("read projection batch: %w", err)
	}
	if !available {
		return ProjectionRun{Source: sourceName, PreviousCursor: checkpoint.Cursor}, nil
	}
	if err := batch.Validate(); err != nil {
		return ProjectionRun{}, fmt.Errorf("projection batch: %w", err)
	}
	if batch.OrgID != orgID || batch.Source != sourceName || batch.Cursor != checkpoint.Cursor {
		return ProjectionRun{}, fmt.Errorf("%w: batch scope or cursor does not match checkpoint", ErrProjectionConflict)
	}
	// CHAOS-3779 codex round-2 H2 residual: a checkpoint.SourceVersion of
	// "" means no prior checkpoint was ever durably saved for this
	// (org, source) pair -- a genuine first run, or the state a completed
	// Rebuild leaves behind (projectionrun.Coordinator.resetAllCheckpoints
	// resets the whole checkpoint, SourceVersion included) -- so it is
	// never itself a mismatch. Any OTHER stored value that differs from
	// the current batch's SourceVersion means the producer's identity
	// semantics changed since this organization was last projected, and
	// applying the batch would MERGE a new edge under the new identity
	// beside whatever the old identity already wrote in the backend,
	// silently doubling it. Refuse before the backend ever sees the
	// batch; recovery is the existing rebuild path.
	if checkpoint.SourceVersion != "" && checkpoint.SourceVersion != batch.SourceVersion {
		return ProjectionRun{}, fmt.Errorf("%w: org %s source %s checkpoint source_version %q, batch source_version %q", ErrProjectionSourceVersionChanged, orgID, sourceName, checkpoint.SourceVersion, batch.SourceVersion)
	}
	// CHAOS-3779 codex round-3 M1: an empty checkpoint.SourceVersion is
	// deliberately never treated as a mismatch above (a genuine first run,
	// or the state Rebuild leaves behind) -- but FalkorDB applies a
	// batch's items as separate writes, not atomically, so a first-ever
	// (or post-rebuild) attempt can partially write edges to the backend,
	// then fail before ever reaching the checkpoint update below. The
	// checkpoint stays empty, and a LATER attempt under a DIFFERENT
	// SourceVersion would see that same "empty means no mismatch"
	// allowance and sail straight through the guard, duplicating whatever
	// the first attempt partially wrote. Claim this SourceVersion
	// durably -- via the same CAS path, cursor left UNCHANGED (zero
	// progress recorded) -- BEFORE the backend ever sees the batch. A
	// partial failure now leaves the claim behind: the next attempt loads
	// a NON-empty SourceVersion, and if it differs, the mismatch check
	// above refuses it on its own next run. Single-flight per organization
	// (the coordinator's advisory lock) rules out a concurrent claim race.
	if checkpoint.SourceVersion == "" {
		claim := ProjectionCheckpoint{
			OrgID: orgID, Source: sourceName, Cursor: checkpoint.Cursor, SourceVersion: batch.SourceVersion,
			BackendWatermark: checkpoint.BackendWatermark, UpdatedAt: w.now().UTC(),
		}
		if err := w.checkpoints.CompareAndSwapProjectionCheckpoint(ctx, checkpoint, claim); err != nil {
			if errors.Is(err, ErrProjectionConflict) {
				return ProjectionRun{}, err
			}
			return ProjectionRun{}, fmt.Errorf("claim projection source version: %w", err)
		}
		checkpoint = claim
	}
	receipt, err := w.backend.ApplyProjectionBatch(ctx, batch)
	if err != nil {
		return ProjectionRun{}, fmt.Errorf("apply projection batch: %w", err)
	}
	if receipt.BatchID != batch.BatchID {
		return ProjectionRun{}, fmt.Errorf("%w: backend receipt does not match batch", ErrProjectionConflict)
	}
	updated := ProjectionCheckpoint{
		OrgID: orgID, Source: sourceName, Cursor: batch.NextCursor, SourceVersion: batch.SourceVersion,
		BackendWatermark: receipt.BackendWatermark, UpdatedAt: w.now().UTC(),
	}
	if err := w.checkpoints.CompareAndSwapProjectionCheckpoint(ctx, checkpoint, updated); err != nil {
		if errors.Is(err, ErrProjectionConflict) {
			return ProjectionRun{}, err
		}
		return ProjectionRun{}, fmt.Errorf("advance projection checkpoint: %w", err)
	}
	return ProjectionRun{
		BatchID: batch.BatchID, Source: sourceName, PreviousCursor: checkpoint.Cursor, NextCursor: batch.NextCursor,
		BackendWatermark: receipt.BackendWatermark, Applied: true, AppliedAt: receipt.AppliedAt,
	}, nil
}

// Run continuously projects one organization/source pair until cancellation.
// Hosting composition owns one worker per configured pair and its lifecycle.
func (w *ProjectionWorker) Run(ctx context.Context, orgID, sourceName string) error {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx, orgID, sourceName); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
