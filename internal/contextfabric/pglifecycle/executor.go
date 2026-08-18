package pglifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// RetireExecutor drives one (org, epoch) EpochRetirement record through its
// own draining -> deleting -> deleted CAS to a terminal GRAPH.DELETE +
// checkpoint-set delete (design brief §3.5). It is the ONLY caller
// permitted to advance a retirement past 'draining', and the only caller
// that ever issues an epoch-scoped graph delete.
//
// This type is deliberately NOT wired into cmd/acr-projector's scheduler in
// this slice (S2a): it is a complete, independently testable unit --
// exercised directly by pglifecycle's own CAS acceptance tests -- so the
// production sweep loop (which needs the same coordinator single-flight
// discipline every other per-org operation in this repository already
// has -- see projectionrun.Coordinator) can be wired in the follow-up slice
// that also converts performRebuild/PurgeOrganization/the CHAOS-3882
// recovery path to lifecycle transitions, per that slice's own scoping.
type RetireExecutor struct {
	Store       contextfabric.GraphLifecycleStore
	Graph       contextfabric.EpochGraphDeleter
	Checkpoints contextfabric.EpochCheckpointDeleter
	Telemetry   contextfabric.GraphLifecycleTelemetry
	// Lease is the KeyResolver's own bounded cache lease (L); Deadline is
	// the enforced per-request binding deadline (D). Both are required and
	// must be positive -- design brief §3.5 pins that binding resolution
	// refuses an unenforced deadline precisely because the drain-bound
	// argument (GRAPH.DELETE issued no earlier than
	// drain_start + Lease + Deadline) depends on both being real, known
	// bounds.
	Lease    time.Duration
	Deadline time.Duration
	Now      func() time.Time
}

func (e *RetireExecutor) telemetry() contextfabric.GraphLifecycleTelemetry {
	if e.Telemetry != nil {
		return e.Telemetry
	}
	return contextfabric.NoopGraphLifecycleTelemetry{}
}

func (e *RetireExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// drainBound is Lease + Deadline -- the minimum time that must elapse
// between an EpochRetirement's DrainStart and its GRAPH.DELETE (design
// brief §3.5).
func (e *RetireExecutor) drainBound() (time.Duration, error) {
	if e.Lease <= 0 || e.Deadline <= 0 {
		return 0, fmt.Errorf("%w: retire executor requires a positive lease and a positive enforced request deadline", contextfabric.ErrLifecycleTransitionRefused)
	}
	return e.Lease + e.Deadline, nil
}

// DueRetirements lists every draining EpochRetirement whose drain bound
// (DrainStart + Lease + Deadline) has already elapsed as of now -- the
// executor's own work queue. Read-only; RunOne performs a second,
// authoritative check against the same bound before deleting anything.
func (e *RetireExecutor) DueRetirements(ctx context.Context) ([]contextfabric.EpochRetirement, error) {
	bound, err := e.drainBound()
	if err != nil {
		return nil, err
	}
	return e.Store.DrainingRetirements(ctx, e.now().Add(-bound))
}

// RunOne drives ONE EpochRetirement (org, epoch) to completion, or refuses
// it with a RetireGuardVerdict recorded via Telemetry either way -- a
// refusal is a normal, expected outcome (a race with rollback, a drain
// bound not yet elapsed if called directly rather than via DueRetirements),
// never a panic-worthy condition. Callers driving a sweep should treat a
// non-nil error as "log and continue to the next retirement", exactly like
// every other per-org operation in this repository (see
// projectionrun.Coordinator.Tick's own per-org failure isolation).
func (e *RetireExecutor) RunOne(ctx context.Context, orgID string, epoch int64) error {
	bound, err := e.drainBound()
	if err != nil {
		return err
	}

	// Re-read the CURRENT lifecycle row -- the "SAME row version the
	// retiring CAS bound, not a fresh point-in-time derivation" pin (design
	// brief §3.5) is satisfied by capturing ActiveEpoch ONCE here and using
	// that exact value for both the guard check and the delete call below,
	// rather than re-deriving it a second time after the CAS.
	lifecycle, found, err := e.Store.Get(ctx, orgID)
	if err != nil {
		return fmt.Errorf("pglifecycle: retire executor: read lifecycle row: %w", err)
	}
	activeEpoch := int64(0)
	if found {
		activeEpoch = lifecycle.ActiveEpoch
	}

	verdict, drainWait, guardErr := e.guard(ctx, orgID, epoch, activeEpoch, bound)
	e.telemetry().RecordEpochRetire(ctx, orgID, epoch, verdict, drainWait)
	if guardErr != nil {
		return guardErr
	}

	if _, err := e.Store.AdvanceRetirement(ctx, orgID, epoch, contextfabric.RetireRecordDraining, contextfabric.RetireRecordDeleting, e.now()); err != nil {
		return fmt.Errorf("pglifecycle: retire executor: advance to deleting: %w", err)
	}

	if err := e.Graph.DeleteEpochGraph(ctx, orgID, epoch, activeEpoch); err != nil {
		return fmt.Errorf("pglifecycle: retire executor: delete epoch graph: %w", err)
	}
	if err := e.Checkpoints.DeleteEpochCheckpoints(ctx, orgID, epoch); err != nil {
		return fmt.Errorf("pglifecycle: retire executor: delete epoch checkpoints: %w", err)
	}

	if _, err := e.Store.AdvanceRetirement(ctx, orgID, epoch, contextfabric.RetireRecordDeleting, contextfabric.RetireRecordDeleted, e.now()); err != nil {
		return fmt.Errorf("pglifecycle: retire executor: advance to deleted: %w", err)
	}
	return nil
}

// guard evaluates every RetireGuardVerdict condition, in the order design
// brief §3.5 states them, and returns the verdict alongside a non-nil error
// for every outcome other than RetireGuardOK.
func (e *RetireExecutor) guard(ctx context.Context, orgID string, epoch, activeEpoch int64, bound time.Duration) (contextfabric.RetireGuardVerdict, time.Duration, error) {
	if orgID == "" || epoch < 0 {
		return contextfabric.RetireGuardRefusedUnderivable, 0, fmt.Errorf("%w: epoch is underivable", contextfabric.ErrLifecycleTransitionRefused)
	}
	// Final key guard (isSweepTargetSafe shape, design brief §3.5): the
	// epoch to retire must not be the currently active/serving epoch. This
	// is checked BEFORE any state is advanced -- a race that just rolled
	// the org back onto this exact epoch must never proceed to delete it.
	if epoch == activeEpoch {
		return contextfabric.RetireGuardRefusedActiveKey, 0, fmt.Errorf("%w: epoch %d is the organization's active epoch", contextfabric.ErrLifecycleTransitionRefused, epoch)
	}
	retirements, err := e.Store.DrainingRetirements(ctx, e.now())
	if err != nil {
		return contextfabric.RetireGuardRefusedState, 0, fmt.Errorf("pglifecycle: retire executor: read retirement state: %w", err)
	}
	var record *contextfabric.EpochRetirement
	for i := range retirements {
		if retirements[i].OrgID == orgID && retirements[i].Epoch == epoch {
			record = &retirements[i]
			break
		}
	}
	if record == nil {
		return contextfabric.RetireGuardRefusedState, 0, fmt.Errorf("%w: no draining retirement record for (org, epoch)", contextfabric.ErrLifecycleTransitionRefused)
	}
	elapsed := e.now().Sub(record.DrainStart)
	if elapsed < bound {
		return contextfabric.RetireGuardRefusedDrainPending, bound - elapsed, fmt.Errorf("%w: drain bound not yet elapsed", contextfabric.ErrLifecycleTransitionRefused)
	}
	return contextfabric.RetireGuardOK, elapsed - bound, nil
}
