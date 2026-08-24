package projectionrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/stretchr/testify/require"
)

// fakeRefusalLifecycleStore is a minimal contextfabric.GraphLifecycleStore
// fake used ONLY to deterministically drive beginLifecycleBuild's
// post-CAS-refusal re-read branch (CHAOS-4208 round-2, codex finding 4): a
// real Postgres store (lifecycle_integration_test.go's own philosophy)
// can't cheaply inject "BeginBuild is CAS-refused AND the follow-up Get
// independently fails" -- these are two logically separate outcomes on the
// same call, which a fake can control directly without racing real CAS
// timing. White-box (package projectionrun, not projectionrun_test) so
// beginLifecycleBuild -- unexported, and the only thing under test here --
// can be called directly, with a bare *Coordinator struct literal instead
// of the full NewCoordinator fixture (Backend/Checkpoints/RebuildMarkers)
// this narrow unit doesn't touch.
type fakeRefusalLifecycleStore struct {
	beginBuildErr error
	getErr        error
	getRow        contextfabric.OrgGraphLifecycle
	getFound      bool
}

func (f *fakeRefusalLifecycleStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	if f.getErr != nil {
		return contextfabric.OrgGraphLifecycle{}, false, f.getErr
	}
	return f.getRow, f.getFound, nil
}

func (f *fakeRefusalLifecycleStore) BeginBuild(context.Context, string, []string, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, f.beginBuildErr
}

func (f *fakeRefusalLifecycleStore) RecordSourceProgress(context.Context, string, int64, string, contextfabric.BuildCompletionMode, int64, time.Time) error {
	return errors.New("fakeRefusalLifecycleStore: not implemented")
}

func (f *fakeRefusalLifecycleStore) SourceProgress(context.Context, string, int64) ([]contextfabric.BuildSourceProgress, error) {
	return nil, errors.New("fakeRefusalLifecycleStore: not implemented")
}

func (f *fakeRefusalLifecycleStore) Flip(context.Context, string, int64, time.Duration, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, errors.New("fakeRefusalLifecycleStore: not implemented")
}

func (f *fakeRefusalLifecycleStore) Rollback(context.Context, string, int64, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, errors.New("fakeRefusalLifecycleStore: not implemented")
}

func (f *fakeRefusalLifecycleStore) BeginRetire(context.Context, string, int64, time.Time, bool) (contextfabric.OrgGraphLifecycle, contextfabric.EpochRetirement, error) {
	return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, errors.New("fakeRefusalLifecycleStore: not implemented")
}

func (f *fakeRefusalLifecycleStore) DrainingRetirements(context.Context, time.Time) ([]contextfabric.EpochRetirement, error) {
	return nil, errors.New("fakeRefusalLifecycleStore: not implemented")
}

func (f *fakeRefusalLifecycleStore) AdvanceRetirement(context.Context, string, int64, contextfabric.RetireRecordState, contextfabric.RetireRecordState, time.Time) (contextfabric.EpochRetirement, error) {
	return contextfabric.EpochRetirement{}, errors.New("fakeRefusalLifecycleStore: not implemented")
}

var _ contextfabric.GraphLifecycleStore = (*fakeRefusalLifecycleStore)(nil)

func newDiscardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBeginLifecycleBuild_PropagatesAFailedReReadAfterCASRefusal pins
// CHAOS-4208 round-2 finding 4: a re-read failure after a CAS refusal must
// surface as an error, not silently default to "observed_status=serving"
// and report success -- that swallowing let an operator's Rebuild call
// return nil for a refusal whose actual cause was never determined, and
// let automatic divergence recovery clear its backoff as though the
// attempt had cleanly resolved to "not rebuildable right now".
func TestBeginLifecycleBuild_PropagatesAFailedReReadAfterCASRefusal(t *testing.T) {
	store := &fakeRefusalLifecycleStore{
		beginBuildErr: contextfabric.ErrLifecycleTransitionRefused,
		getErr:        errors.New("dependency unavailable"),
	}
	coordinator := &Coordinator{
		lifecycle:   store,
		sourceNames: []string{"source-a"},
		now:         time.Now,
		logger:      newDiscardTestLogger(),
	}

	opened, err := coordinator.beginLifecycleBuild(context.Background(), "org-1")
	require.False(t, opened)
	require.Error(t, err, "a re-read failure after a CAS refusal must not be reported as success")
}

// TestBeginLifecycleBuild_GraceRefusalWithSuccessfulReReadIsAHarmlessNoOp
// is the companion sanity case: when the re-read itself succeeds and
// reports a non-building status (e.g. grace), that IS the ordinary,
// already-covered no-op path (also pinned end-to-end against a real store
// in lifecycle_invalidation_test.go's grace-refusal test) -- listed here
// for contrast with the failure case above, in the same fake-driven unit.
func TestBeginLifecycleBuild_GraceRefusalWithSuccessfulReReadIsAHarmlessNoOp(t *testing.T) {
	store := &fakeRefusalLifecycleStore{
		beginBuildErr: contextfabric.ErrLifecycleTransitionRefused,
		getFound:      true,
		getRow:        contextfabric.OrgGraphLifecycle{ActiveEpoch: 1, Status: contextfabric.LifecycleStatusGrace},
	}
	coordinator := &Coordinator{
		lifecycle:   store,
		sourceNames: []string{"source-a"},
		now:         time.Now,
		logger:      newDiscardTestLogger(),
	}

	opened, err := coordinator.beginLifecycleBuild(context.Background(), "org-1")
	require.False(t, opened)
	require.NoError(t, err)
}
