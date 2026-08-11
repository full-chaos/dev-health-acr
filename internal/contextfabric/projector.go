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
