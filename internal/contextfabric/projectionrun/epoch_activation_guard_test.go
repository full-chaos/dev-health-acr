package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

// This file pins each clause of the epoch activation guard on its own, on the
// real Postgres lifecycle store and checkpoint store (the graph backend is
// the shared fake: which graph a reader resolves is proven live in
// epoch_activation_guard_live_test.go). Every clause has a test that fails
// when that clause alone is changed.

// recordingActivationTelemetry captures cf_epoch_activation_refused.
type recordingActivationTelemetry struct {
	contextfabric.NoopGraphLifecycleTelemetry
	mu       sync.Mutex
	refusals []contextfabric.EpochActivationRefusal
}

func (r *recordingActivationTelemetry) RecordEpochActivationRefused(_ context.Context, refusal contextfabric.EpochActivationRefusal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusals = append(r.refusals, refusal)
}

func (r *recordingActivationTelemetry) snapshot() []contextfabric.EpochActivationRefusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contextfabric.EpochActivationRefusal(nil), r.refusals...)
}

// unreadableCheckpoints fails ListProjectionCheckpoints while armed -- the
// candidate epoch's rows cannot be read -- and passes everything else
// through to the real view.
type unreadableCheckpoints struct {
	contextfabric.ProjectionCheckpointStore
	armed *atomic.Bool
}

func (u unreadableCheckpoints) ListProjectionCheckpoints(ctx context.Context, orgID string) ([]contextfabric.ProjectionCheckpoint, error) {
	if u.armed.Load() {
		return nil, errors.New("checkpoint store unavailable")
	}
	return u.ProjectionCheckpointStore.(contextfabric.ProjectionCheckpointLister).ListProjectionCheckpoints(ctx, orgID)
}

// cancellingCheckpoints cancels the caller's context the moment the guard
// asks for the epoch's source list -- a tick or rollback cut short mid-guard.
type cancellingCheckpoints struct {
	contextfabric.ProjectionCheckpointStore
	armed  *atomic.Bool
	cancel context.CancelFunc
}

func (c cancellingCheckpoints) ListProjectionCheckpoints(ctx context.Context, orgID string) ([]contextfabric.ProjectionCheckpoint, error) {
	if c.armed.Load() {
		c.cancel()
		return nil, ctx.Err()
	}
	return c.ProjectionCheckpointStore.(contextfabric.ProjectionCheckpointLister).ListProjectionCheckpoints(ctx, orgID)
}

// unlistableCheckpoints is a checkpoint view that cannot say which sources it
// holds: it deliberately does NOT implement ProjectionCheckpointLister.
type unlistableCheckpoints struct {
	inner contextfabric.ProjectionCheckpointStore
}

func (u unlistableCheckpoints) LoadProjectionCheckpoint(ctx context.Context, orgID, source string) (contextfabric.ProjectionCheckpoint, error) {
	return u.inner.LoadProjectionCheckpoint(ctx, orgID, source)
}

func (u unlistableCheckpoints) CompareAndSwapProjectionCheckpoint(ctx context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	return u.inner.CompareAndSwapProjectionCheckpoint(ctx, expected, updated)
}

// unversionedSource projects like lifecycleFakeSource but reports no current
// version at all (no ProjectionSourceVersion capability).
type unversionedSource struct{ inner *lifecycleFakeSource }

func (u unversionedSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	return u.inner.NextProjectionBatch(ctx, checkpoint)
}

// pairFailedNames is the tick summary's list of broken pairs.
func pairFailedNames(t *testing.T, summary map[string]any) []string {
	t.Helper()
	raw, ok := summary["pair_failed"].([]any)
	if !ok {
		t.Fatalf("the tick summary carries no pair_failed list: %v", summary)
	}
	names := make([]string, 0, len(raw))
	for _, name := range raw {
		names = append(names, name.(string))
	}
	return names
}

type guardRig struct {
	t           *testing.T
	ctx         context.Context
	lifecycle   *pglifecycle.Store
	checkpoints *pgprojection.CheckpointStore
	org         string
}

type guardCoordinatorOptions struct {
	budget      int
	telemetry   contextfabric.GraphLifecycleTelemetry
	logger      *slog.Logger
	checkpoints contextfabric.ProjectionCheckpointStore
	epochViews  func(int64) contextfabric.ProjectionCheckpointStore
}

func (r *guardRig) coordinator(options guardCoordinatorOptions, sources ...projectionrun.SourcePair) *projectionrun.Coordinator {
	r.t.Helper()
	logger := options.logger
	if logger == nil {
		logger = discardLogger()
	}
	var checkpoints contextfabric.ProjectionCheckpointStore = r.checkpoints
	if options.checkpoints != nil {
		checkpoints = options.checkpoints
	}
	epochViews := r.checkpoints.ForEpoch
	if options.epochViews != nil {
		epochViews = options.epochViews
	}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{r.org}, Sources: sources,
		Backend: newFakeBackend(), Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: r.lifecycle, EpochCheckpoints: epochViews, LifecycleTelemetry: options.telemetry,
		GraceWindow: time.Hour, MaxBackoff: time.Millisecond, DrainBatchBudget: options.budget, Logger: logger,
	})
	require.NoError(r.t, err)
	return coordinator
}

func (r *guardRig) row() contextfabric.OrgGraphLifecycle {
	r.t.Helper()
	row, found, err := r.lifecycle.Get(r.ctx, r.org)
	require.NoError(r.t, err)
	require.True(r.t, found)
	return row
}

func versioned(version string, pages int) *epochGuardTeamSource {
	return &epochGuardTeamSource{version: version, scope: []string{epochGuardRepo}, pages: pages}
}

func failingSource(version string) *epochGuardTeamSource {
	source := &epochGuardTeamSource{version: version}
	source.failing.Store(true)
	return source
}

// logRecords decodes every JSON log line whose msg starts with prefix.
func logRecords(t *testing.T, buffer *bytes.Buffer, prefix string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if msg, _ := record["msg"].(string); strings.HasPrefix(msg, prefix) {
			out = append(out, record)
		}
	}
	return out
}

func TestEpochActivationGuard_Clauses(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	rig := func(t *testing.T, org string) *guardRig {
		return &guardRig{t: t, ctx: ctx, lifecycle: lifecycle, checkpoints: checkpoints, org: org}
	}

	// A refused flip is loud on every attempt: a Warn naming every input,
	// one cf_epoch_activation_refused per refusing source, and the
	// organization counted rebuild_required on the tick summary.
	t.Run("flip_refusal_is_loud_and_recorded", func(t *testing.T) {
		r := rig(t, "org-guard-flip-loud")
		old := r.coordinator(guardCoordinatorOptions{budget: -1}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardOldVersion, 2)})
		require.NoError(t, old.Rebuild(ctx, r.org))
		old.Tick(ctx)

		var buffer bytes.Buffer
		telemetry := &recordingActivationTelemetry{}
		next := r.coordinator(guardCoordinatorOptions{
			budget: -1, telemetry: telemetry,
			logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		next.Tick(ctx)

		want := contextfabric.EpochActivationRefusal{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionFlip, ActiveEpoch: 0, CandidateEpoch: 1,
			Source: epochGuardSource, Reason: contextfabric.EpochActivationRefusedSourceVersion,
			RecordedSourceVersion: epochGuardOldVersion, CurrentSourceVersion: epochGuardNewVersion,
		}
		require.Equal(t, []contextfabric.EpochActivationRefusal{want}, telemetry.snapshot())
		warns := logRecords(t, &buffer, "context_fabric: graph epoch activation refused")
		require.Len(t, warns, 1)
		require.Equal(t, "WARN", warns[0]["level"])
		require.Equal(t, "flip", warns[0]["transition"])
		require.Equal(t, float64(1), warns[0]["candidate_epoch"])
		require.Equal(t, float64(0), warns[0]["active_epoch"])
		require.Equal(t, epochGuardSource, warns[0]["source"])
		require.Equal(t, "source_version_mismatch", warns[0]["reason"])
		require.Equal(t, epochGuardOldVersion, warns[0]["recorded_source_version"])
		require.Equal(t, epochGuardNewVersion, warns[0]["current_source_version"])
		summary := freshnessSummary(t, &buffer)
		require.Equal(t, float64(1), summaryNumber(t, summary, "orgs_rebuild_required"))
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)

		next.Tick(ctx)
		require.Len(t, telemetry.snapshot(), 2, "every flip attempt is refused again, and says so again")
	})

	// A refused rollback returns the refusal to the operator, leaves the
	// row exactly where it was, and creates no retirement record for the
	// epoch it did not abandon.
	t.Run("rollback_refusal_is_loud_and_leaves_state_untouched", func(t *testing.T) {
		r := rig(t, "org-guard-rollback-loud")
		old := r.coordinator(guardCoordinatorOptions{}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardOldVersion, 1)})
		old.Tick(ctx)
		telemetry := &recordingActivationTelemetry{}
		next := r.coordinator(guardCoordinatorOptions{telemetry: telemetry}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		require.NoError(t, next.Rebuild(ctx, r.org))
		before := tickUntilStatus(t, ctx, next, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)

		err := next.Rollback(ctx, r.org)
		require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)
		require.ErrorIs(t, err, contextfabric.ErrEpochSourceVersionStale)
		require.Equal(t, []contextfabric.EpochActivationRefusal{{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionRollback, ActiveEpoch: 1, CandidateEpoch: 0,
			Source: epochGuardSource, Reason: contextfabric.EpochActivationRefusedSourceVersion,
			RecordedSourceVersion: epochGuardOldVersion, CurrentSourceVersion: epochGuardNewVersion,
		}}, telemetry.snapshot())
		after := r.row()
		require.Equal(t, before.Status, after.Status)
		require.Equal(t, before.ActiveEpoch, after.ActiveEpoch)
		require.Equal(t, before.GraceEpoch, after.GraceEpoch)
		retirements, err := lifecycle.DrainingRetirements(ctx, time.Now().Add(24*time.Hour))
		require.NoError(t, err)
		for _, retirement := range retirements {
			require.NotEqual(t, r.org, retirement.OrgID, "a refused rollback must not retire the epoch it kept active")
		}
	})

	// Mismatch, not only "older": an epoch a NEWER binary recorded is just
	// as unservable by this one.
	t.Run("newer_recorded_version_is_refused", func(t *testing.T) {
		r := rig(t, "org-guard-newer")
		newer := r.coordinator(guardCoordinatorOptions{}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		newer.Tick(ctx)
		rolledBackBinary := r.coordinator(guardCoordinatorOptions{}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardOldVersion, 1)})
		require.NoError(t, rolledBackBinary.Rebuild(ctx, r.org))
		tickUntilStatus(t, ctx, rolledBackBinary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)
		require.ErrorIs(t, rolledBackBinary.Rollback(ctx, r.org), contextfabric.ErrEpochSourceVersionStale)
		require.Equal(t, int64(1), r.row().ActiveEpoch)
	})

	// Recorded "": the source never wrote into the candidate epoch, so
	// there is nothing stale to serve -- a source that is empty from its
	// first tick, and an epoch 0 the source never projected, both activate.
	t.Run("never_recorded_source_does_not_block", func(t *testing.T) {
		r := rig(t, "org-guard-empty")
		binary := r.coordinator(guardCoordinatorOptions{},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)},
			projectionrun.SourcePair{Name: "empty", Source: versioned("empty.v2", 0)},
		)
		require.NoError(t, binary.Rebuild(ctx, r.org))
		flipped := tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)
		require.Equal(t, int64(1), flipped.ActiveEpoch)
		emptyCheckpoint, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, r.org, 1, "empty")
		require.NoError(t, err)
		require.Empty(t, emptyCheckpoint.SourceVersion, "precondition: the empty source recorded no version")
		require.NoError(t, binary.Rollback(ctx, r.org), "epoch 0 holds nothing from either source")
		require.Equal(t, int64(0), r.row().ActiveEpoch)
	})

	// A build frozen by an earlier binary can hold data from a source this
	// binary no longer configures. Nothing can vouch for it: refused.
	t.Run("required_source_no_longer_configured_is_refused", func(t *testing.T) {
		r := rig(t, "org-guard-dropped")
		old := r.coordinator(guardCoordinatorOptions{},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)},
			projectionrun.SourcePair{Name: "dropped", Source: versioned("dropped.v1", 1)},
			projectionrun.SourcePair{Name: "sibling", Source: failingSource("sibling.v1")},
		)
		require.NoError(t, old.Rebuild(ctx, r.org))
		old.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status, "precondition: the failing sibling holds the flip")

		telemetry := &recordingActivationTelemetry{}
		next := r.coordinator(guardCoordinatorOptions{telemetry: telemetry},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)},
			projectionrun.SourcePair{Name: "sibling", Source: versioned("sibling.v1", 0)},
		)
		next.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		require.Equal(t, []contextfabric.EpochActivationRefusal{{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionFlip, ActiveEpoch: 0, CandidateEpoch: 1,
			Source: "dropped", Reason: contextfabric.EpochActivationRefusedSourceNotConfigured, RecordedSourceVersion: "dropped.v1",
		}}, telemetry.snapshot())
	})

	// A source that reports no current version cannot vouch for the data an
	// epoch recorded under it: refused, and announced at startup already.
	t.Run("unversioned_source_is_refused_loudly", func(t *testing.T) {
		r := rig(t, "org-guard-unversioned")
		var buffer bytes.Buffer
		telemetry := &recordingActivationTelemetry{}
		binary := r.coordinator(guardCoordinatorOptions{
			telemetry: telemetry, logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
		}, projectionrun.SourcePair{Name: "unversioned", Source: unversionedSource{&lifecycleFakeSource{name: "unversioned", pages: 1}}})
		startup := logRecords(t, &buffer, "context_fabric: source reports no current version")
		require.Len(t, startup, 1)
		require.Equal(t, "WARN", startup[0]["level"])
		require.Equal(t, "unversioned", startup[0]["source"])
		require.NoError(t, binary.Rebuild(ctx, r.org))
		binary.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		require.Equal(t, []contextfabric.EpochActivationRefusal{{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionFlip, ActiveEpoch: 0, CandidateEpoch: 1,
			Source: "unversioned", Reason: contextfabric.EpochActivationRefusedSourceVersionUnknown, RecordedSourceVersion: "test.v1",
		}}, telemetry.snapshot())
	})

	// A blank current version is no version: an epoch recorded at v16 must
	// not flip because the source now reports whitespace.
	t.Run("blank_current_version_is_refused", func(t *testing.T) {
		r := rig(t, "org-guard-blank-current")
		old := r.coordinator(guardCoordinatorOptions{budget: -1}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardOldVersion, 2)})
		require.NoError(t, old.Rebuild(ctx, r.org))
		old.Tick(ctx)
		telemetry := &recordingActivationTelemetry{}
		blank := r.coordinator(guardCoordinatorOptions{budget: -1, telemetry: telemetry}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(" \t", 1)})
		blank.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		refusals := telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Equal(t, contextfabric.EpochActivationRefusedSourceVersionUnknown, refusals[0].Reason)
		require.Equal(t, epochGuardOldVersion, refusals[0].RecordedSourceVersion)
	})

	// Versions are opaque identities, compared byte for byte (codex #749 r2):
	// a recorded version that differs from the binary's only by trailing
	// whitespace is a different producer identity, and RunOnce would refuse
	// its next batch too.
	for i, padded := range []string{epochGuardNewVersion + " ", " "} {
		t.Run(fmt.Sprintf("whitespace_padded_recorded_version_is_refused/%d", i), func(t *testing.T) {
			r := rig(t, fmt.Sprintf("org-guard-padded-version-%d", i))
			telemetry := &recordingActivationTelemetry{}
			binary := r.coordinator(guardCoordinatorOptions{telemetry: telemetry}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
			require.NoError(t, binary.Rebuild(ctx, r.org))
			tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)
			require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpoint(ctx,
				contextfabric.ProjectionCheckpoint{OrgID: r.org, Source: epochGuardSource},
				contextfabric.ProjectionCheckpoint{OrgID: r.org, Source: epochGuardSource, Cursor: "p", SourceVersion: padded, UpdatedAt: time.Now().UTC()}))

			err := binary.Rollback(ctx, r.org)
			require.ErrorIs(t, err, contextfabric.ErrEpochSourceVersionStale)
			require.Equal(t, int64(1), r.row().ActiveEpoch)
			require.Equal(t, []contextfabric.EpochActivationRefusal{{
				OrgID: r.org, Transition: contextfabric.LifecycleTransitionRollback, ActiveEpoch: 1, CandidateEpoch: 0,
				Source: epochGuardSource, Reason: contextfabric.EpochActivationRefusedSourceVersion,
				RecordedSourceVersion: padded, CurrentSourceVersion: epochGuardNewVersion,
			}}, telemetry.snapshot())
		})
	}

	// A row is judged under the exact name it was stored with (codex #749
	// r2): a padded name no configured source carries cannot be reloaded
	// under a trimmed name and read as "never recorded".
	for i, tc := range []struct{ name, version string }{
		{" retired_teams_projects ", epochGuardOldVersion},
		// Trims to the configured name and carries the current version: only
		// an exact-name lookup refuses it.
		{" " + epochGuardSource + " ", epochGuardNewVersion},
	} {
		t.Run(fmt.Sprintf("padded_source_name_row_is_refused/%d", i), func(t *testing.T) {
			r := rig(t, fmt.Sprintf("org-guard-padded-name-%d", i))
			telemetry := &recordingActivationTelemetry{}
			binary := r.coordinator(guardCoordinatorOptions{telemetry: telemetry}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
			require.NoError(t, binary.Rebuild(ctx, r.org))
			tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)
			require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpoint(ctx,
				contextfabric.ProjectionCheckpoint{OrgID: r.org, Source: tc.name},
				contextfabric.ProjectionCheckpoint{OrgID: r.org, Source: tc.name, Cursor: "p", SourceVersion: tc.version, UpdatedAt: time.Now().UTC()}))

			err := binary.Rollback(ctx, r.org)
			require.ErrorIs(t, err, contextfabric.ErrEpochSourceVersionStale)
			require.Equal(t, int64(1), r.row().ActiveEpoch)
			require.Equal(t, []contextfabric.EpochActivationRefusal{{
				OrgID: r.org, Transition: contextfabric.LifecycleTransitionRollback, ActiveEpoch: 1, CandidateEpoch: 0,
				Source: tc.name, Reason: contextfabric.EpochActivationRefusedSourceNotConfigured, RecordedSourceVersion: tc.version,
			}}, telemetry.snapshot())
		})
	}

	// A row that claims no version (a reset checkpoint, or one written with
	// no producer identity) recorded nothing, whether or not its source is
	// configured: it never blocks an activation.
	t.Run("rows_with_no_recorded_version_do_not_block", func(t *testing.T) {
		r := rig(t, "org-guard-empty-rows")
		telemetry := &recordingActivationTelemetry{}
		binary := r.coordinator(guardCoordinatorOptions{telemetry: telemetry}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		require.NoError(t, binary.Rebuild(ctx, r.org))
		tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)
		for _, source := range []string{epochGuardSource, "retired_teams_projects"} {
			require.NoError(t, checkpoints.CompareAndSwapProjectionCheckpoint(ctx,
				contextfabric.ProjectionCheckpoint{OrgID: r.org, Source: source},
				contextfabric.ProjectionCheckpoint{OrgID: r.org, Source: source, Cursor: "reset-marker", UpdatedAt: time.Now().UTC()}))
		}
		rows, err := checkpoints.ListProjectionCheckpoints(ctx, r.org)
		require.NoError(t, err)
		require.Len(t, rows, 2, "precondition: epoch 0 holds two rows with no recorded version")

		require.NoError(t, binary.Rollback(ctx, r.org))
		require.Equal(t, int64(0), r.row().ActiveEpoch)
		require.Empty(t, telemetry.snapshot())
	})

	// Unlistable is a wiring defect, not a stale version: a rollback onto a
	// view that cannot list is refused, and says which of the two it is.
	t.Run("unlistable_rollback_target_is_refused_not_stale", func(t *testing.T) {
		r := rig(t, "org-guard-unlistable-rollback")
		telemetry := &recordingActivationTelemetry{}
		binary := r.coordinator(guardCoordinatorOptions{
			telemetry: telemetry, checkpoints: unlistableCheckpoints{inner: checkpoints},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		binary.Tick(ctx)
		require.NoError(t, binary.Rebuild(ctx, r.org))
		tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)
		err := binary.Rollback(ctx, r.org)
		require.ErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)
		require.NotErrorIs(t, err, contextfabric.ErrEpochSourceVersionStale)
		require.Equal(t, int64(1), r.row().ActiveEpoch)
		require.Equal(t, []contextfabric.EpochActivationRefusal{{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionRollback, ActiveEpoch: 1, CandidateEpoch: 0,
			Reason: contextfabric.EpochActivationRefusedSourcesUnlistable,
		}}, telemetry.snapshot())
	})

	// A checkpoint view that cannot list its sources could be hiding one:
	// refused, and the tick summary names the broken read.
	t.Run("unlistable_checkpoint_view_is_refused", func(t *testing.T) {
		r := rig(t, "org-guard-unlistable")
		var buffer bytes.Buffer
		telemetry := &recordingActivationTelemetry{}
		binary := r.coordinator(guardCoordinatorOptions{
			telemetry: telemetry, logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
			epochViews: func(epoch int64) contextfabric.ProjectionCheckpointStore {
				return unlistableCheckpoints{inner: checkpoints.ForEpoch(epoch)}
			},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		require.NoError(t, binary.Rebuild(ctx, r.org))
		binary.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)
		require.Equal(t, []contextfabric.EpochActivationRefusal{{
			OrgID: r.org, Transition: contextfabric.LifecycleTransitionFlip, ActiveEpoch: 0, CandidateEpoch: 1,
			Reason: contextfabric.EpochActivationRefusedSourcesUnlistable,
		}}, telemetry.snapshot())
		summary := freshnessSummary(t, &buffer)
		require.Equal(t, float64(1), summaryNumber(t, summary, "orgs_pair_failed"))
		require.Contains(t, pairFailedNames(t, summary), "epoch_checkpoint_sources:checkpoint_load")
	})

	// An unreadable candidate checkpoint proves nothing, so nothing
	// activates -- and the error is not a permanent refusal, so the next
	// attempt re-reads.
	t.Run("unreadable_candidate_checkpoint_fails_closed", func(t *testing.T) {
		r := rig(t, "org-guard-unreadable-flip")
		old := r.coordinator(guardCoordinatorOptions{},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)},
			projectionrun.SourcePair{Name: "sibling", Source: failingSource("sibling.v1")},
		)
		require.NoError(t, old.Rebuild(ctx, r.org))
		old.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status)

		armed := &atomic.Bool{}
		armed.Store(true)
		var buffer bytes.Buffer
		telemetry := &recordingActivationTelemetry{}
		next := r.coordinator(guardCoordinatorOptions{
			telemetry: telemetry, logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
			epochViews: func(epoch int64) contextfabric.ProjectionCheckpointStore {
				return unreadableCheckpoints{ProjectionCheckpointStore: checkpoints.ForEpoch(epoch), armed: armed}
			},
		},
			projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)},
			projectionrun.SourcePair{Name: "sibling", Source: versioned("sibling.v1", 0)},
		)
		next.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusBuilding, r.row().Status, "an unreadable checkpoint must not let the build flip")
		refusals := telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Equal(t, contextfabric.EpochActivationRefusedCheckpointUnreadable, refusals[0].Reason)
		require.Empty(t, refusals[0].Source, "the epoch's rows as a whole could not be read")
		summary := freshnessSummary(t, &buffer)
		require.Equal(t, float64(1), summaryNumber(t, summary, "orgs_pair_failed"), "an unreadable checkpoint is a broken pair, never a quiet backoff")
		require.Equal(t, float64(0), summaryNumber(t, summary, "orgs_backoff"))
		require.Contains(t, pairFailedNames(t, summary), "epoch_checkpoint_sources:checkpoint_load")

		armed.Store(false)
		next.Tick(ctx)
		require.Equal(t, contextfabric.LifecycleStatusGrace, r.row().Status, "once the checkpoint reads again the same build flips")
	})

	// The candidate epoch's source list cannot be read: fail closed, then
	// succeed once it can.
	t.Run("unreadable_source_list_fails_closed", func(t *testing.T) {
		r := rig(t, "org-guard-unreadable-list")
		armed := &atomic.Bool{}
		telemetry := &recordingActivationTelemetry{}
		binary := r.coordinator(guardCoordinatorOptions{
			telemetry: telemetry, checkpoints: unreadableCheckpoints{ProjectionCheckpointStore: checkpoints, armed: armed},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		binary.Tick(ctx)
		require.NoError(t, binary.Rebuild(ctx, r.org))
		tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)

		armed.Store(true)
		err := binary.Rollback(ctx, r.org)
		require.Error(t, err)
		require.NotErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)
		require.Equal(t, int64(1), r.row().ActiveEpoch)
		refusals := telemetry.snapshot()
		require.Len(t, refusals, 1)
		require.Equal(t, contextfabric.EpochActivationRefusedCheckpointUnreadable, refusals[0].Reason)
		require.Empty(t, refusals[0].Source, "the rows as a whole could not be read, not one source")

		armed.Store(false)
		require.NoError(t, binary.Rollback(ctx, r.org))
		require.Equal(t, int64(0), r.row().ActiveEpoch)
	})

	// A guard whose own context ends mid-read reached no verdict: nothing
	// activates, and no refusal is claimed for a read that never happened.
	t.Run("cancelled_guard_claims_no_refusal", func(t *testing.T) {
		r := rig(t, "org-guard-cancelled")
		armed := &atomic.Bool{}
		rollbackCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		telemetry := &recordingActivationTelemetry{}
		binary := r.coordinator(guardCoordinatorOptions{
			telemetry: telemetry, checkpoints: cancellingCheckpoints{ProjectionCheckpointStore: checkpoints, armed: armed, cancel: cancel},
		}, projectionrun.SourcePair{Name: epochGuardSource, Source: versioned(epochGuardNewVersion, 1)})
		binary.Tick(ctx)
		require.NoError(t, binary.Rebuild(ctx, r.org))
		tickUntilStatus(t, ctx, binary, lifecycle, r.org, contextfabric.LifecycleStatusGrace, 5)

		armed.Store(true)
		err := binary.Rollback(rollbackCtx, r.org)
		require.ErrorIs(t, err, context.Canceled)
		require.NotErrorIs(t, err, contextfabric.ErrLifecycleTransitionRefused)
		require.Empty(t, telemetry.snapshot(), "a cancelled read is truncation, not a refusal")
		require.Equal(t, int64(1), r.row().ActiveEpoch)
	})
}
