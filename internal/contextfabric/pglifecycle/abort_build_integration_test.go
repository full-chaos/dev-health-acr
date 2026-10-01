package pglifecycle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/stretchr/testify/require"
)

func retirementsOf(t *testing.T, ctx context.Context, store *pglifecycle.Store, orgID string) []contextfabric.EpochRetirement {
	t.Helper()
	all, err := store.DrainingRetirements(ctx, time.Now().Add(24*time.Hour))
	require.NoError(t, err)
	var out []contextfabric.EpochRetirement
	for _, retirement := range all {
		if retirement.OrgID == orgID {
			out = append(out, retirement)
		}
	}
	return out
}

func lifecycleRow(t *testing.T, ctx context.Context, store *pglifecycle.Store, orgID string) contextfabric.OrgGraphLifecycle {
	t.Helper()
	row, found, err := store.Get(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	return row
}

func TestAbortBuild(t *testing.T) {
	ctx := context.Background()
	db := newLifecycleTestDatabase(t, ctx)
	newStore := func(t *testing.T) (*pglifecycle.Store, *fakeLifecycleTelemetry) {
		store, err := pglifecycle.NewStore(db)
		require.NoError(t, err)
		telemetry := &fakeLifecycleTelemetry{}
		store.Telemetry = telemetry
		return store, telemetry
	}

	t.Run("first_build_returns_to_serving_epoch_zero_and_retires_the_target", func(t *testing.T) {
		store, telemetry := newStore(t)
		const org = "org-abort-first"
		built, err := store.BeginBuild(ctx, org, []string{"a", "b"}, time.Now())
		require.NoError(t, err)
		require.NoError(t, store.RecordSourceProgress(ctx, org, *built.TargetEpoch, "a", contextfabric.BuildCompletionPagedFinal, 3, time.Now()))

		at := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
		aborted, err := store.AbortBuild(ctx, org, *built.TargetEpoch, at)
		require.NoError(t, err)
		require.Equal(t, contextfabric.OrgGraphLifecycle{
			OrgID: org, ActiveEpoch: 0, LastAllocatedEpoch: 1, Status: contextfabric.LifecycleStatusServing, UpdatedAt: at,
		}, aborted)
		require.Equal(t, aborted, lifecycleRow(t, ctx, store, org), "the returned row is the stored row")

		require.Equal(t, []contextfabric.EpochRetirement{{
			OrgID: org, Epoch: 1, Reason: contextfabric.RetireReasonBuildAborted, DrainStart: at,
			State: contextfabric.RetireRecordDraining, CreatedAt: at, UpdatedAt: at,
		}}, retirementsOf(t, ctx, store, org))

		progress, err := store.SourceProgress(ctx, org, 1)
		require.NoError(t, err)
		require.Len(t, progress, 1, "the aborted epoch's progress rows stay as the record of what it reached")

		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Equal(t, []buildAbortSignal{{org, 0, 1}}, telemetry.buildAborts)
		require.Empty(t, telemetry.casConflicts)
	})

	t.Run("build_over_a_served_epoch_keeps_that_epoch_active", func(t *testing.T) {
		store, telemetry := newStore(t)
		const org = "org-abort-served"
		flipped := flipReady(t, ctx, store, org, []string{"a"}, time.Hour)
		_, _, err := store.BeginRetire(ctx, org, flipped.ActiveEpoch, time.Now(), true)
		require.NoError(t, err)
		built, err := store.BeginBuild(ctx, org, []string{"a"}, time.Now())
		require.NoError(t, err)
		require.Equal(t, int64(2), *built.TargetEpoch)

		aborted, err := store.AbortBuild(ctx, org, 2, time.Now())
		require.NoError(t, err)
		require.Equal(t, contextfabric.LifecycleStatusServing, aborted.Status)
		require.Equal(t, int64(1), aborted.ActiveEpoch, "the epoch that served before the build still serves")
		require.Equal(t, int64(2), aborted.LastAllocatedEpoch)
		require.Nil(t, aborted.TargetEpoch)
		require.Nil(t, aborted.GraceEpoch)
		require.Nil(t, aborted.RequiredSources)

		reasons := map[int64]contextfabric.RetireReason{}
		for _, retirement := range retirementsOf(t, ctx, store, org) {
			reasons[retirement.Epoch] = retirement.Reason
		}
		require.Equal(t, map[int64]contextfabric.RetireReason{
			0: contextfabric.RetireReasonGraceExpired, 2: contextfabric.RetireReasonBuildAborted,
		}, reasons)

		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Equal(t, []buildAbortSignal{{org, 1, 2}}, telemetry.buildAborts)
	})

	t.Run("next_build_allocates_a_new_epoch_and_flips", func(t *testing.T) {
		store, _ := newStore(t)
		const org = "org-abort-rebuild"
		_, err := store.BeginBuild(ctx, org, []string{"a"}, time.Now())
		require.NoError(t, err)
		_, err = store.AbortBuild(ctx, org, 1, time.Now())
		require.NoError(t, err)

		_, err = store.Flip(ctx, org, 1, time.Hour, time.Now())
		require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict, "the aborted epoch can never be flipped")

		rebuilt, err := store.BeginBuild(ctx, org, []string{"a"}, time.Now())
		require.NoError(t, err)
		require.Equal(t, int64(2), *rebuilt.TargetEpoch, "the aborted epoch number is never reused")
		require.NoError(t, store.RecordSourceProgress(ctx, org, 2, "a", contextfabric.BuildCompletionPagedFinal, 1, time.Now()))
		flipped, err := store.Flip(ctx, org, 2, time.Hour, time.Now())
		require.NoError(t, err)
		require.Equal(t, contextfabric.LifecycleStatusGrace, flipped.Status)
		require.Equal(t, int64(2), flipped.ActiveEpoch)
		require.Equal(t, int64(0), *flipped.GraceEpoch)
	})

	t.Run("wrong_target_epoch_conflicts_and_changes_nothing", func(t *testing.T) {
		store, telemetry := newStore(t)
		const org = "org-abort-wrong-epoch"
		_, err := store.BeginBuild(ctx, org, []string{"a"}, time.Now())
		require.NoError(t, err)
		before := lifecycleRow(t, ctx, store, org)

		_, err = store.AbortBuild(ctx, org, 2, time.Now())
		require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict)
		require.Equal(t, before, lifecycleRow(t, ctx, store, org))
		require.Empty(t, retirementsOf(t, ctx, store, org))

		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Empty(t, telemetry.buildAborts)
		require.Len(t, telemetry.casConflicts, 1)
		require.Equal(t, contextfabric.LifecycleTransitionAbortBuild, telemetry.casConflicts[0].losing)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, telemetry.casConflicts[0].observed)
	})

	t.Run("no_lifecycle_row_conflicts_and_creates_none", func(t *testing.T) {
		store, telemetry := newStore(t)
		const org = "org-abort-absent"
		_, err := store.AbortBuild(ctx, org, 1, time.Now())
		require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict)
		_, found, err := store.Get(ctx, org)
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, retirementsOf(t, ctx, store, org))

		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Empty(t, telemetry.buildAborts)
		require.Len(t, telemetry.casConflicts, 1)
		require.Equal(t, contextfabric.LifecycleStatusServing, telemetry.casConflicts[0].observed)
	})

	t.Run("grace_conflicts_and_keeps_the_flipped_epoch", func(t *testing.T) {
		store, telemetry := newStore(t)
		const org = "org-abort-grace"
		flipped := flipReady(t, ctx, store, org, []string{"a"}, time.Hour)
		before := lifecycleRow(t, ctx, store, org)

		_, err := store.AbortBuild(ctx, org, flipped.ActiveEpoch, time.Now())
		require.ErrorIs(t, err, contextfabric.ErrLifecycleConflict)
		require.Equal(t, before, lifecycleRow(t, ctx, store, org))
		require.Empty(t, retirementsOf(t, ctx, store, org), "the epoch that flipped must not be queued for retirement")

		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Empty(t, telemetry.buildAborts)
		require.Len(t, telemetry.casConflicts, 1)
		require.Equal(t, contextfabric.LifecycleStatusGrace, telemetry.casConflicts[0].observed)
	})

	t.Run("another_organization_with_the_same_target_epoch_is_untouched", func(t *testing.T) {
		store, telemetry := newStore(t)
		const org, other = "org-abort-scoped", "org-abort-scoped-other"
		for _, id := range []string{org, other} {
			_, err := store.BeginBuild(ctx, id, []string{"a"}, time.Now())
			require.NoError(t, err)
		}
		otherBefore := lifecycleRow(t, ctx, store, other)

		_, err := store.AbortBuild(ctx, org, 1, time.Now())
		require.NoError(t, err)
		require.Equal(t, contextfabric.LifecycleStatusServing, lifecycleRow(t, ctx, store, org).Status)
		require.Equal(t, otherBefore, lifecycleRow(t, ctx, store, other))
		require.Empty(t, retirementsOf(t, ctx, store, other))
		require.Len(t, retirementsOf(t, ctx, store, org), 1)

		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Equal(t, []buildAbortSignal{{org, 0, 1}}, telemetry.buildAborts)
	})

	t.Run("invalid_arguments_are_rejected_before_any_write", func(t *testing.T) {
		store, telemetry := newStore(t)
		for _, tc := range []struct {
			org   string
			epoch int64
		}{{"", 1}, {"   ", 1}, {"org-abort-invalid", 0}, {"org-abort-invalid", -1}} {
			_, err := store.AbortBuild(ctx, tc.org, tc.epoch, time.Now())
			require.Error(t, err)
			require.NotErrorIs(t, err, contextfabric.ErrLifecycleConflict)
		}
		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Empty(t, telemetry.casConflicts)
		require.Empty(t, telemetry.buildAborts)
	})

	// The lifecycle table's own CHECK is what makes "a build is open" and
	// "target_epoch is set" one fact: no row can carry a target epoch
	// outside 'building', so the CAS can never match a non-building row.
	t.Run("schema_forbids_a_target_epoch_outside_building", func(t *testing.T) {
		store, _ := newStore(t)
		const org = "org-abort-schema"
		_, err := store.BeginBuild(ctx, org, []string{"a"}, time.Now())
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_graph_lifecycle SET status = 'serving' WHERE org_id = $1`, org)
		require.ErrorContains(t, err, "ck_acr_cf_graph_lifecycle_target_epoch")
		_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_graph_lifecycle SET status = 'grace', grace_epoch = 7, grace_deadline = now() WHERE org_id = $1`, org)
		require.ErrorContains(t, err, "ck_acr_cf_graph_lifecycle_target_epoch")
		_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_graph_lifecycle SET target_epoch = NULL WHERE org_id = $1`, org)
		require.ErrorContains(t, err, "ck_acr_cf_graph_lifecycle_target_epoch")
	})

	// The reason vocabulary only widened: both earlier reasons are still
	// admitted (a binary without this change keeps writing them), the new
	// one is admitted, and anything else is still refused.
	t.Run("retirement_reason_check_only_widened", func(t *testing.T) {
		const org = "org-abort-reason-check"
		insert := func(epoch int64, reason string) error {
			_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_graph_epoch_retirements (org_id, epoch, reason, drain_start, state, created_at, updated_at)
VALUES ($1, $2, $3, now(), 'draining', now(), now())`, org, epoch, reason)
			return err
		}
		for epoch, reason := range []string{"grace_expired", "rollback_abandoned", "build_aborted"} {
			require.NoError(t, insert(int64(epoch), reason), reason)
		}
		for epoch, reason := range []string{"", "BUILD_ABORTED", "build_aborted ", "aborted"} {
			require.ErrorContains(t, insert(int64(10+epoch), reason), "context_fabric_graph_epoch_retirements_reason_check", reason)
		}
	})

	t.Run("aborted_epoch_is_deleted_by_the_retire_executor_after_the_drain_bound", func(t *testing.T) {
		store, _ := newStore(t)
		const org = "org-abort-retire"
		checkpoints, err := pgprojection.NewCheckpointStore(db)
		require.NoError(t, err)
		_, err = store.BeginBuild(ctx, org, []string{"a"}, time.Now())
		require.NoError(t, err)
		empty, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, org, 1, "a")
		require.NoError(t, err)
		written := empty
		written.Cursor, written.SourceVersion, written.UpdatedAt = "cursor-1", "source.v1", time.Now().UTC()
		require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpointForEpoch(ctx, empty, written))

		abortedAt := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
		_, err = store.AbortBuild(ctx, org, 1, abortedAt)
		require.NoError(t, err)

		graph := &fakeGraphDeleter{}
		now := abortedAt
		executor := &pglifecycle.RetireExecutor{
			Store: store, Graph: graph, Checkpoints: checkpoints,
			Lease: time.Minute, Deadline: time.Minute, Now: func() time.Time { return now },
		}
		dueFor := func() []contextfabric.EpochRetirement {
			due, err := executor.DueRetirements(ctx)
			require.NoError(t, err)
			var out []contextfabric.EpochRetirement
			for _, retirement := range due {
				if retirement.OrgID == org {
					out = append(out, retirement)
				}
			}
			return out
		}

		now = abortedAt.Add(2*time.Minute - time.Second)
		require.Empty(t, dueFor(), "the drain bound starts at the abort")
		require.Error(t, executor.RunOne(ctx, org, 1))
		require.Empty(t, graph.deletes)

		now = abortedAt.Add(2 * time.Minute)
		due := dueFor()
		require.Len(t, due, 1)
		require.Equal(t, int64(1), due[0].Epoch)
		require.NoError(t, executor.RunOne(ctx, org, 1))
		require.Len(t, graph.deletes, 1)
		require.Equal(t, int64(1), graph.deletes[0].epoch)
		require.Equal(t, int64(0), graph.deletes[0].activeEpoch)
		after, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, org, 1, "a")
		require.NoError(t, err)
		require.Empty(t, after.Cursor, "the aborted epoch's checkpoint set is deleted with its graph")
		require.Empty(t, dueFor())
		require.Equal(t, contextfabric.LifecycleStatusServing, lifecycleRow(t, ctx, store, org).Status)
	})

	t.Run("abort_and_flip_race_has_exactly_one_winner", func(t *testing.T) {
		store, telemetry := newStore(t)
		for i := 0; i < 8; i++ {
			org := "org-abort-race-" + string(rune('a'+i))
			_, err := store.BeginBuild(ctx, org, []string{"a"}, time.Now())
			require.NoError(t, err)
			require.NoError(t, store.RecordSourceProgress(ctx, org, 1, "a", contextfabric.BuildCompletionPagedFinal, 1, time.Now()))

			var wg sync.WaitGroup
			var abortErr, flipErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, abortErr = store.AbortBuild(ctx, org, 1, time.Now())
			}()
			go func() {
				defer wg.Done()
				_, flipErr = store.Flip(ctx, org, 1, time.Hour, time.Now())
			}()
			wg.Wait()

			require.NotEqual(t, abortErr == nil, flipErr == nil, "exactly one of abort_build/flip must win the race")
			final := lifecycleRow(t, ctx, store, org)
			retirements := retirementsOf(t, ctx, store, org)
			if abortErr == nil {
				require.ErrorIs(t, flipErr, contextfabric.ErrLifecycleConflict)
				require.Equal(t, contextfabric.LifecycleStatusServing, final.Status)
				require.Equal(t, int64(0), final.ActiveEpoch)
				require.Len(t, retirements, 1)
			} else {
				require.ErrorIs(t, abortErr, contextfabric.ErrLifecycleConflict)
				require.Equal(t, contextfabric.LifecycleStatusGrace, final.Status)
				require.Equal(t, int64(1), final.ActiveEpoch)
				require.Empty(t, retirements, "an epoch that flipped must never also be queued as aborted")
			}
		}
		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		require.Equal(t, 8, len(telemetry.buildAborts)+len(telemetry.flips))
	})
}
