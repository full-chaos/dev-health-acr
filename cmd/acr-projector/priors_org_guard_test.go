package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var errNotWritten = errors.New("nothing written")

type recordingOrgLocker struct {
	steps *[]string
	err   error
}

func (l recordingOrgLocker) Lock(context.Context, string) (func() error, error) {
	*l.steps = append(*l.steps, "lock")
	if l.err != nil {
		return nil, l.err
	}
	return func() error { *l.steps = append(*l.steps, "unlock"); return nil }, nil
}

type recordingMarkers struct {
	steps  *[]string
	purged bool
}

func (m recordingMarkers) IsPurged(context.Context, string) (bool, error) {
	*m.steps = append(*m.steps, "marker")
	return m.purged, nil
}

func TestPriorsOrgGuardOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lockErr  error
		purged   bool
		writeErr error
		steps    []string
		outcome  string
		is       error
	}{
		{name: "written", steps: []string{"lock", "marker", "write", "unlock"}, outcome: priorsOutcomeWritten},
		{name: "locked", lockErr: projectionrun.ErrOrgLocked, steps: []string{"lock"}, outcome: priorsOutcomeLocked, is: projectionrun.ErrOrgLocked},
		{name: "purged marker", purged: true, steps: []string{"lock", "marker", "unlock"}, outcome: priorsOutcomePurged, is: storage.ErrOrgPurged},
		{name: "refused by the write fence", writeErr: storage.ErrOrgPurged, steps: []string{"lock", "marker", "write", "unlock"}, outcome: priorsOutcomePurged, is: storage.ErrOrgPurged},
		{name: "write failed", writeErr: contextfabric.ErrUnavailable, steps: []string{"lock", "marker", "write", "unlock"}, outcome: priorsOutcomeFailed, is: contextfabric.ErrUnavailable},
		{name: "unchanged", writeErr: errNotWritten, steps: []string{"lock", "marker", "write", "unlock"}, outcome: priorsOutcomeUnchanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			var logs bytes.Buffer
			guard := priorsOrgGuard{
				Locker:  recordingOrgLocker{steps: &steps, err: tc.lockErr},
				Markers: recordingMarkers{steps: &steps, purged: tc.purged},
				Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
			}
			err := guard.run(context.Background(), "flip", "org-1", func(context.Context) (bool, error) {
				steps = append(steps, "write")
				if errors.Is(tc.writeErr, errNotWritten) {
					return false, nil
				}
				return tc.writeErr == nil, tc.writeErr
			})
			require.Equal(t, tc.steps, steps)
			if tc.is == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.is)
			}
			require.Contains(t, logs.String(), "command=flip")
			require.Contains(t, logs.String(), "outcome="+tc.outcome)
			require.Equal(t, tc.outcome == priorsOutcomeFailed, strings.Contains(logs.String(), "failure_class=dependency_unavailable"), "a failed write logs its failure class:\n%s", logs.String())
		})
	}
}

// The guard takes the organization lock, then reads the marker, then writes,
// and releases the lock only in a deferred call, after the write returned.
func TestPriorsOrgGuardReleasesTheOrganizationLockOnlyAfterTheWrite(t *testing.T) {
	raw, err := os.ReadFile("priors.go")
	require.NoError(t, err)
	source := string(raw)
	start := strings.Index(source, "func (g priorsOrgGuard) run(")
	require.GreaterOrEqual(t, start, 0)
	body := source[start:]
	body = body[:strings.Index(body, "\n}\n")]
	lock := strings.Index(body, "g.Locker.Lock(")
	deferredUnlock := strings.Index(body, "defer func() {\n\t\tif unlockErr := unlock(); unlockErr != nil")
	marker := strings.Index(body, "g.Markers.IsPurged(")
	write := strings.Index(body, "write(ctx)")
	require.True(t, lock >= 0 && deferredUnlock >= 0 && marker >= 0 && write >= 0, "guard markers not found")
	require.Less(t, lock, deferredUnlock)
	require.Less(t, deferredUnlock, marker)
	require.Less(t, marker, write)
	require.Equal(t, 1, strings.Count(body, "unlock()"), "the organization lock is released only by the deferred call")
	for _, command := range []string{"priorsCurate", "priorsFlip", "priorsRollback", "priorsRevoke"} {
		fn := source[strings.Index(source, "func "+command+"("):]
		fn = fn[:strings.Index(fn, "\n}\n")]
		require.Contains(t, fn, "guard.run(ctx, ", "%s must write under the organization guard", command)
		require.NotContains(t, fn, "openPriorsDB(", "%s must open through the opener that builds the guard", command)
	}
}

// opener opens the rig's database with the given locker and marker reader,
// each replaced by the production one when nil.
func (r *priorsFenceRig) opener(withLocker projectionrun.OrgLocker, withMarkers priorsPurgeMarkers) priorsOpener {
	return func(ctx context.Context) (*sql.DB, priorsOrgGuard, error) {
		db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: r.dsn})
		if err != nil {
			return nil, priorsOrgGuard{}, err
		}
		locker, markers := withLocker, withMarkers
		if locker == nil {
			if locker, err = projectionrun.NewPostgresOrgLocker(db); err != nil {
				return nil, priorsOrgGuard{}, err
			}
		}
		if markers == nil {
			if markers, err = pgprojection.NewOrgPurgeMarkerStore(db); err != nil {
				return nil, priorsOrgGuard{}, err
			}
		}
		return db, priorsOrgGuard{Locker: locker, Markers: markers, Logger: slog.New(slog.DiscardHandler)}, nil
	}
}

type neverPurged struct{}

func (neverPurged) IsPurged(context.Context, string) (bool, error) { return false, nil }

// Control: without the organization lock, the same command writes while a
// projector pass or a purge holds the lock, which is what
// TestPriorsCommandsReportLockedWhileTheOrganizationLockIsHeld catches.
func TestPriorsControlWithoutTheOrganizationLockAWriteLandsWhileTheLockIsHeld(t *testing.T) {
	r := newPriorsFenceRig(t)
	org := uuid.NewString()
	r.seed(t, org)
	before := r.priorRows(t, org)
	r.holdOrgLock(t, org)
	require.NoError(t, runPriors("revoke", []string{"--org", org, "--entry", "entry-1", "--by", "operator"}, r.opener(projectionrun.NoopOrgLocker{}, nil)))
	require.Greater(t, r.priorRows(t, org), before)
}

// Control: without the organization lock, a purge runs while a priors write
// is in flight, which is what TestPriorsCommandHoldsTheOrganizationLockUntilItsWriteCommits
// catches. Curate has already read the selections and waits to insert its
// version; the write fence refuses that insert once the purge committed.
func TestPriorsControlWithoutTheOrganizationLockAPurgeRunsDuringAPriorsWrite(t *testing.T) {
	r := newPriorsFenceRig(t)
	org := uuid.NewString()
	r.seed(t, org)

	release := r.blockPointerLock(t, org)
	done := make(chan error, 1)
	go func() {
		done <- runPriors("curate", []string{"--org", org, "--attest-no-frozen-corpus"}, r.opener(projectionrun.NoopOrgLocker{}, nil))
	}()
	waitFor(t, "the curate waiting inside its write transaction", func() bool { return r.waitingOnAdvisoryLock(t) })
	require.NoError(t, r.purge(context.Background(), t, org))
	require.Equal(t, 1, r.markers(t, org))
	release()
	require.ErrorIs(t, <-done, storage.ErrOrgPurged)
	require.Zero(t, r.priorRows(t, org))
}

// Without the marker read, revoke is still refused by the write fence and is
// reported as purged, but flip on a purged organization reports a missing
// version: the marker read is what gives every command the purged answer.
func TestPriorsWithoutTheMarkerReadOnlyTheWriteFenceRefuses(t *testing.T) {
	r := newPriorsFenceRig(t)
	org := uuid.NewString()
	r.seed(t, org)
	require.NoError(t, r.purge(context.Background(), t, org))
	open := r.opener(nil, neverPurged{})

	err := runPriors("revoke", []string{"--org", org, "--entry", "entry-1", "--by", "operator"}, open)
	require.ErrorIs(t, err, storage.ErrOrgPurged)
	require.Contains(t, err.Error(), "was purged; nothing is written for it until it returns to discovery")

	err = runPriors("flip", []string{"--org", org, "--version", "1", "--expect-current", "2", "--by", "operator"}, open)
	require.Error(t, err)
	require.False(t, errors.Is(err, storage.ErrOrgPurged), "flip without the marker read: %v", err)
	require.ErrorIs(t, err, contextfabric.ErrPriorVersionNotFound)
	require.Zero(t, r.priorRows(t, org))
}
