package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructurepriors"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/containergate"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

type priorsFenceRig struct {
	dsn string
	db  *sql.DB
}

func newPriorsFenceRig(t *testing.T) *priorsFenceRig {
	t.Helper()
	containergate.Require(t)
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(ctx)) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)

	t.Setenv("ACR_ENVIRONMENT", "development")
	t.Setenv("ACR_POSTGRES_DSN", dsn)
	t.Setenv("ACR_POSTGRES_CONNECTION_KIND", "direct")
	t.Setenv("ACR_POSTGRES_STARTUP_ATTEMPTS", "1")
	t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", "")
	return &priorsFenceRig{dsn: dsn, db: db}
}

// seed gives org one promotable selection and versions 1 and 2 with 2 active,
// so each of the four priors commands below writes a row when it is allowed.
func (r *priorsFenceRig) seed(t *testing.T, org string) {
	t.Helper()
	ctx := context.Background()
	_, err := r.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context)
VALUES ('selection-' || $1, $1, now(), repeat('a', 64), 'prior-result', 'expected_kind', 'receipt', 'pull_request', true, 'human_panel', 'web_assertion', '[]', '{}')`, org)
	require.NoError(t, err)
	store, err := pgstructurepriors.NewStore(r.db)
	require.NoError(t, err)
	for range 2 {
		_, err = store.PublishVersion(ctx, org, nil, "seed-watermark", contextfabric.CurationRuleVersionV1)
		require.NoError(t, err)
	}
	one, two := int64(1), int64(2)
	require.NoError(t, store.FlipActiveVersion(ctx, org, nil, &one, "seed"))
	require.NoError(t, store.FlipActiveVersion(ctx, org, &one, &two, "seed"))
}

// priorRows counts the organization's rows in every table a priors command
// writes.
func (r *priorsFenceRig) priorRows(t *testing.T, org string) int {
	t.Helper()
	var n int
	require.NoError(t, r.db.QueryRowContext(context.Background(), `
SELECT (SELECT count(*) FROM acr.context_fabric_structure_priors WHERE org_id = $1)
     + (SELECT count(*) FROM acr.context_fabric_structure_prior_pointer WHERE org_id = $1)
     + (SELECT count(*) FROM acr.context_fabric_structure_prior_pointer_history WHERE org_id = $1)
     + (SELECT count(*) FROM acr.context_fabric_structure_prior_revocations WHERE org_id = $1)`, org).Scan(&n))
	return n
}

func (r *priorsFenceRig) markers(t *testing.T, org string) int {
	t.Helper()
	var n int
	require.NoError(t, r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM acr.context_fabric_org_purge_markers WHERE org_id = $1`, org).Scan(&n))
	return n
}

type priorsFenceGraph struct{}

func (priorsFenceGraph) CountOrganizationGraphs(context.Context, string) (int, error)  { return 0, nil }
func (priorsFenceGraph) DeleteOrganizationGraphs(context.Context, string) (int, error) { return 0, nil }

type priorsFenceSet struct{}

func (priorsFenceSet) RefreshOrgs(context.Context) error { return nil }
func (priorsFenceSet) EffectiveOrgs() []string           { return nil }

type priorsFenceUnknownOrg struct{}

func (priorsFenceUnknownOrg) OrganizationUnknown(context.Context, string) (bool, error) {
	return true, nil
}

// purge runs the production purge of org with the production org locker.
func (r *priorsFenceRig) purge(ctx context.Context, t *testing.T, org string) error {
	t.Helper()
	locker, err := projectionrun.NewPostgresOrgLocker(r.db)
	require.NoError(t, err)
	purger := &orgpurge.Purger{DB: r.db, Graph: priorsFenceGraph{}, Set: priorsFenceSet{}, Entitlements: priorsFenceUnknownOrg{}, Locker: locker, Logger: slog.New(slog.DiscardHandler)}
	_, err = purger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	return err
}

// holdOrgLock takes the organization lock the way a projector pass or a purge
// does, on its own connection, until the returned release is called.
func (r *priorsFenceRig) holdOrgLock(t *testing.T, org string) func() {
	t.Helper()
	locker, err := projectionrun.NewPostgresOrgLocker(r.db)
	require.NoError(t, err)
	unlock, err := locker.Lock(context.Background(), org)
	require.NoError(t, err)
	var once sync.Once
	release := func() { once.Do(func() { require.NoError(t, unlock()) }) }
	t.Cleanup(release)
	return release
}

type priorsFenceCommand struct {
	name string
	args func(org string) []string
}

func priorsFenceCommands() []priorsFenceCommand {
	return []priorsFenceCommand{
		{"curate", func(org string) []string { return []string{"curate", "--org", org, "--attest-no-frozen-corpus"} }},
		{"flip", func(org string) []string {
			return []string{"flip", "--org", org, "--version", "1", "--expect-current", "2", "--by", "operator"}
		}},
		{"rollback", func(org string) []string {
			return []string{"rollback", "--org", org, "--expect-current", "2", "--by", "operator"}
		}},
		{"revoke", func(org string) []string {
			return []string{"revoke", "--org", org, "--entry", "entry-1", "--by", "operator"}
		}},
	}
}

// captureDefaultLog sends slog.Default to a buffer for the rest of the test.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func requireCommandLog(t *testing.T, logs string, command, outcome string) {
	t.Helper()
	want := "command=" + command
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `msg="acr-projector priors command finished"`) && strings.Contains(line, want+" ") && strings.Contains(line, "outcome="+outcome) {
			return
		}
	}
	t.Fatalf("no Info line for priors %s with outcome %s in:\n%s", command, outcome, logs)
}

// Each priors command run for a purged organization exits with the typed
// purged error and a message an operator can act on, and writes no row.
func TestPriorsCommandsRefuseAPurgedOrganization(t *testing.T) {
	r := newPriorsFenceRig(t)
	logs := captureDefaultLog(t)
	for _, command := range priorsFenceCommands() {
		t.Run(command.name, func(t *testing.T) {
			org := uuid.NewString()
			r.seed(t, org)
			require.NoError(t, r.purge(context.Background(), t, org))
			require.Equal(t, 1, r.markers(t, org))
			require.Zero(t, r.priorRows(t, org))

			err := priorsCommand(command.args(org))
			require.Error(t, err, "priors %s must refuse a purged organization", command.name)
			require.ErrorIs(t, err, storage.ErrOrgPurged)
			require.Contains(t, err.Error(), "was purged; nothing is written for it until it returns to discovery")
			require.Zero(t, r.priorRows(t, org), "priors %s wrote a row for a purged organization", command.name)
			requireCommandLog(t, logs.String(), command.name, "purged")
		})
	}
	require.Contains(t, logs.String(), `level=WARN msg="acr-projector priors refused: organization was purged"`)
}

// Each priors command run while another session holds the organization lock
// (a projector pass, or a purge before it deletes) exits with the typed locked
// error and writes no row. Once the lock is released the same command writes.
func TestPriorsCommandsReportLockedWhileTheOrganizationLockIsHeld(t *testing.T) {
	r := newPriorsFenceRig(t)
	logs := captureDefaultLog(t)
	for _, command := range priorsFenceCommands() {
		t.Run(command.name, func(t *testing.T) {
			org := uuid.NewString()
			r.seed(t, org)
			before := r.priorRows(t, org)
			release := r.holdOrgLock(t, org)

			err := priorsCommand(command.args(org))
			require.Error(t, err, "priors %s must not write while the organization lock is held", command.name)
			require.ErrorIs(t, err, projectionrun.ErrOrgLocked)
			require.Contains(t, err.Error(), "is locked by a projector pass or a purge; nothing was written, run the command again")
			require.Equal(t, before, r.priorRows(t, org), "priors %s wrote a row while the organization lock was held", command.name)
			requireCommandLog(t, logs.String(), command.name, "locked")

			release()
			require.NoError(t, priorsCommand(command.args(org)))
			require.Greater(t, r.priorRows(t, org), before, "priors %s wrote nothing after the lock was released", command.name)
			requireCommandLog(t, logs.String(), command.name, "written")
		})
	}
	require.Contains(t, logs.String(), `level=WARN msg="acr-projector priors refused: organization is locked by a projector pass or a purge"`)
}

// The lock is keyed by the canonical UUID, so another spelling of the
// organization is locked out too.
func TestPriorsCommandsReportLockedForAnotherSpellingOfTheOrganization(t *testing.T) {
	r := newPriorsFenceRig(t)
	org := uuid.NewString()
	r.holdOrgLock(t, org)
	err := priorsCommand([]string{"revoke", "--org", strings.ToUpper(org), "--entry", "entry-1", "--by", "operator"})
	require.ErrorIs(t, err, projectionrun.ErrOrgLocked)
	require.Zero(t, r.priorRows(t, strings.ToUpper(org)))
}

// Once discovery lists the organization again and its marker is cleared, the
// priors commands write for it again.
func TestPriorsCommandsWriteAgainAfterThePurgeMarkerIsCleared(t *testing.T) {
	r := newPriorsFenceRig(t)
	for _, command := range priorsFenceCommands() {
		t.Run(command.name, func(t *testing.T) {
			org := uuid.NewString()
			r.seed(t, org)
			require.NoError(t, r.purge(context.Background(), t, org))
			require.ErrorIs(t, priorsCommand(command.args(org)), storage.ErrOrgPurged)

			_, err := r.db.ExecContext(context.Background(), `DELETE FROM acr.context_fabric_org_purge_markers WHERE org_id = $1`, org)
			require.NoError(t, err)
			r.seed(t, org)
			before := r.priorRows(t, org)
			require.NoError(t, priorsCommand(command.args(org)))
			require.Greater(t, r.priorRows(t, org), before)
		})
	}
}

// waitingOnAdvisoryLock reports whether some session waits on an advisory lock.
func (r *priorsFenceRig) waitingOnAdvisoryLock(t *testing.T) bool {
	t.Helper()
	var n int
	require.NoError(t, r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&n))
	return n > 0
}

// blockPointerLock holds the priors store's own pointer lock of org on a
// separate session, so a flip of org stops inside its write transaction,
// after the command took the organization lock and read the marker.
func (r *priorsFenceRig) blockPointerLock(t *testing.T, org string) (release func()) {
	t.Helper()
	conn, err := r.db.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), `SELECT pg_advisory_lock(hashtext($1))`, "structure_priors:"+org)
	require.NoError(t, err)
	var once sync.Once
	release = func() {
		once.Do(func() {
			_, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, "structure_priors:"+org)
			require.NoError(t, err)
			require.NoError(t, conn.Close())
		})
	}
	t.Cleanup(release)
	return release
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never observed: %s", what)
}

// A purge that starts while a priors write is in flight is refused by the
// organization lock the command still holds; it deletes nothing and sets no
// marker. After the write commits, the purge runs and removes the row the
// command wrote, and the next priors command for the org is refused.
func TestPriorsCommandHoldsTheOrganizationLockUntilItsWriteCommits(t *testing.T) {
	r := newPriorsFenceRig(t)
	org := uuid.NewString()
	r.seed(t, org)
	before := r.priorRows(t, org)

	release := r.blockPointerLock(t, org)
	done := make(chan error, 1)
	go func() {
		done <- priorsCommand([]string{"flip", "--org", org, "--version", "1", "--expect-current", "2", "--by", "operator"})
	}()
	waitFor(t, "the flip waiting inside its write transaction", func() bool { return r.waitingOnAdvisoryLock(t) })

	err := r.purge(context.Background(), t, org)
	require.ErrorIs(t, err, orgpurge.ErrLocked, "a purge must not run while a priors command holds the organization lock")
	require.Zero(t, r.markers(t, org))
	require.Equal(t, before, r.priorRows(t, org))

	release()
	require.NoError(t, <-done)
	require.Greater(t, r.priorRows(t, org), before)

	require.NoError(t, r.purge(context.Background(), t, org))
	require.Zero(t, r.priorRows(t, org), "the purge must remove the rows the priors command wrote")
	err = priorsCommand([]string{"revoke", "--org", org, "--entry", "entry-1", "--by", "operator"})
	require.True(t, errors.Is(err, storage.ErrOrgPurged), "after the purge: %v", err)
	require.Zero(t, r.priorRows(t, org))
}
