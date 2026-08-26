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
	// CompleteEnumeration (CHAOS-3898 S2a-2, design brief §3.3/item 5)
	// carries the applied batch's own ContextFabricProjectionBatch.
	// CompleteEnumeration flag -- previously computed by a source and then
	// discarded at this boundary ("the enumeration flags ProjectionRun
	// currently drops"). true only when Applied is also true and the
	// source claims this batch enumerated everything through the current
	// cursor; a build-completion classifier (projectionrun.Coordinator)
	// uses it as the "paged_final" signal. A source that pages without
	// ever claiming this on its last page is not penalized: exhaustion is
	// independently detectable via Applied=false with a non-empty
	// PreviousCursor (see BuildCompletionMode's doc comment) -- this field
	// is a FASTER, more precise signal when a source reports it, never the
	// only one.
	CompleteEnumeration bool
	// ItemsApplied (CHAOS-3898 S2a-2) is the sum of the backend receipt's
	// per-kind applied counts (entities+edges+contents+episodes+tombstones)
	// for this ONE tick's batch -- zero when Applied is false.
	ItemsApplied int
	// RowsApplied (CHAOS-4305) is the checkpoint's own durable
	// ProjectionCheckpoint.RowsApplied AFTER this call: the checkpoint's
	// pre-existing value plus ItemsApplied when a batch applied, or the
	// pre-existing value unchanged otherwise. It rides in the SAME CAS
	// statement that advances Cursor, so it can never diverge from it.
	// projectionrun.Coordinator's runBuildPair accumulates its per-tick row
	// count from THIS field, not from cf_build_source_progress's
	// separately-written rows_projected column -- closing the permanent
	// undercount CHAOS-4305 describes (a whole drain's RecordSourceProgress
	// writes, including the finalizing retry, all failing while the
	// checkpoint itself keeps advancing).
	RowsApplied int64
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
		advanced, progressed, err := w.persistConsumedProgress(ctx, checkpoint)
		if err != nil {
			return ProjectionRun{}, err
		}
		if progressed {
			return ProjectionRun{Source: sourceName, PreviousCursor: checkpoint.Cursor, NextCursor: advanced, RowsApplied: checkpoint.RowsApplied}, nil
		}
		return ProjectionRun{Source: sourceName, PreviousCursor: checkpoint.Cursor, RowsApplied: checkpoint.RowsApplied}, nil
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
			// RowsApplied carried through unchanged (CHAOS-4305): claiming a
			// SourceVersion applies zero rows by construction (cursor left
			// UNCHANGED, per this block's own doc comment above) -- a fresh
			// literal that omitted this would silently reset an
			// already-nonzero counter back to 0.
			RowsApplied: checkpoint.RowsApplied,
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
	itemsApplied := receipt.EntitiesApplied + receipt.EdgesApplied + receipt.ContentsApplied + receipt.EpisodesApplied + receipt.TombstonesApplied
	updated := ProjectionCheckpoint{
		OrgID: orgID, Source: sourceName, Cursor: batch.NextCursor, SourceVersion: batch.SourceVersion,
		BackendWatermark: receipt.BackendWatermark, UpdatedAt: w.now().UTC(),
		// RowsApplied (CHAOS-4305) accumulates in the SAME CAS statement
		// that advances Cursor, so it can never diverge from it -- the
		// atomicity fix. checkpoint.RowsApplied here is the pre-call value
		// (unchanged by the claim sub-path above, which never touches it).
		RowsApplied: checkpoint.RowsApplied + int64(itemsApplied),
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
		CompleteEnumeration: batch.CompleteEnumeration,
		ItemsApplied:        itemsApplied,
		RowsApplied:         updated.RowsApplied,
	}, nil
}

// persistConsumedProgress durably advances the checkpoint over source rows
// that were consumed but proved unpublishable -- see ProjectionProgress for
// why this cannot be done with a batch, and for the safety argument.
//
// Deliberately narrow: it runs only when the source reported no batch, only
// when the source implements the optional capability, and only for a cursor
// that actually moves. BackendWatermark and SourceVersion are carried through
// UNCHANGED -- nothing was applied, so nothing about the backend's reconciled
// state may change here. The same CAS the apply path uses keeps a concurrent
// projector from silently reordering this with a real batch.
func (w *ProjectionWorker) persistConsumedProgress(ctx context.Context, checkpoint ProjectionCheckpoint) (string, bool, error) {
	reporter, ok := w.source.(ProjectionProgress)
	if !ok {
		return "", false, nil
	}
	progress, ok, err := reporter.ConsumedWithoutPublishing(ctx, checkpoint)
	if err != nil {
		return "", false, fmt.Errorf("read consumed projection progress: %w", err)
	}
	if !ok || strings.TrimSpace(progress.NextCursor) == "" || progress.NextCursor == checkpoint.Cursor {
		return "", false, nil
	}
	// A source that cannot name the identity its progress was derived under
	// gets no progress at all. Silently advancing under an unknown producer
	// identity is the failure this guard exists to prevent.
	if strings.TrimSpace(progress.SourceVersion) == "" {
		return "", false, nil
	}
	// The same rule the batch path applies at the batch's own SourceVersion
	// (see the CHAOS-3779 H2-residual comment above): a stored version that
	// differs means the producer's identity semantics changed, so the durable
	// cursor must NOT move -- rows the new version would publish sit behind
	// it. Recovery is the existing rebuild path, not a quiet advance.
	if checkpoint.SourceVersion != "" && checkpoint.SourceVersion != progress.SourceVersion {
		return "", false, fmt.Errorf("%w: org %s source %s checkpoint source_version %q, progress source_version %q", ErrProjectionSourceVersionChanged, checkpoint.OrgID, checkpoint.Source, checkpoint.SourceVersion, progress.SourceVersion)
	}
	updated := checkpoint
	updated.Cursor = progress.NextCursor
	// Claim the version alongside the advance, exactly as the batch path
	// claims it before applying: after this, an empty stored version can no
	// longer wave a different producer identity through.
	updated.SourceVersion = progress.SourceVersion
	updated.UpdatedAt = w.now().UTC()
	if err := w.checkpoints.CompareAndSwapProjectionCheckpoint(ctx, checkpoint, updated); err != nil {
		if errors.Is(err, ErrProjectionConflict) {
			return "", false, err
		}
		return "", false, fmt.Errorf("advance projection checkpoint over unpublishable rows: %w", err)
	}
	return progress.NextCursor, true, nil
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
