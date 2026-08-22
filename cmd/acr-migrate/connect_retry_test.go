package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
)

// CHAOS-4116 (2026-08-22 A/B incident, see the scoped-kill skill):
// openMigrationDB's own retry loop, tested against a FAKE opener --
// never a real connection, so these run in milliseconds with no
// network egress. TestRun_retriesOnATransientConnectFailure below is
// the consumer-level counterpart, exercising the REAL run() end to end
// against a real testcontainers postgres.
//
// MUTATION CHECK PERFORMED (development-time, not automated): removing
// the `attempt > 1` guard on the "connected on attempt" log line makes
// TestOpenMigrationDB_succeedsOnFirstAttempt's exact-output assertion
// fail (it would gain a spurious log line); changing `attempt < attempts`
// to `attempt <= attempts` in the sleep call makes
// TestOpenMigrationDB_sleepCallCount fail (one extra, wasted sleep after
// the final attempt). Both confirmed, then reverted.

func noSleep(time.Duration) {}

func TestOpenMigrationDB_succeedsOnFirstAttempt(t *testing.T) {
	calls := 0
	opener := func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return &sql.DB{}, nil
	}
	var output bytes.Buffer

	db, err := openMigrationDBWithOpener(context.Background(), runtimepostgres.Config{}, 3, time.Millisecond, noSleep, &output, opener)

	require.NoError(t, err)
	require.NotNil(t, db)
	require.Equal(t, 1, calls)
	require.Empty(t, output.String(), "a first-attempt success must not log anything -- byte-identical to before this ticket")
}

func TestOpenMigrationDB_retriesThenSucceeds(t *testing.T) {
	calls := 0
	opener := func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("PostgreSQL is unavailable")
		}
		return &sql.DB{}, nil
	}
	var output bytes.Buffer

	db, err := openMigrationDBWithOpener(context.Background(), runtimepostgres.Config{}, 3, time.Millisecond, noSleep, &output, opener)

	require.NoError(t, err)
	require.NotNil(t, db)
	require.Equal(t, 3, calls)
	require.Contains(t, output.String(), "connect attempt 1/3 failed")
	require.Contains(t, output.String(), "connect attempt 2/3 failed")
	require.Contains(t, output.String(), "connected to PostgreSQL on attempt 3/3")
}

func TestOpenMigrationDB_exhaustsAttemptsAndReturnsTheLastError(t *testing.T) {
	calls := 0
	sentinel := errors.New("PostgreSQL is unavailable: connection 42")
	opener := func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, sentinel
	}
	var output bytes.Buffer

	db, err := openMigrationDBWithOpener(context.Background(), runtimepostgres.Config{}, 3, time.Millisecond, noSleep, &output, opener)

	require.Nil(t, db)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 3, calls, "must attempt exactly the configured count, no more")
	require.Contains(t, output.String(), "connect attempt 3/3 failed")
}

// TestOpenMigrationDB_sleepCallCount pins that a backoff sleep happens
// BETWEEN attempts only -- never after the last one (which would just be
// wasted wall time on a run that is already failing for good).
func TestOpenMigrationDB_sleepCallCount(t *testing.T) {
	sleepCalls := 0
	sleep := func(time.Duration) { sleepCalls++ }
	opener := func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		return nil, errors.New("PostgreSQL is unavailable")
	}
	var output bytes.Buffer

	_, err := openMigrationDBWithOpener(context.Background(), runtimepostgres.Config{}, 3, time.Millisecond, sleep, &output, opener)

	require.Error(t, err)
	require.Equal(t, 2, sleepCalls, "3 attempts -> 2 gaps between them, never a sleep after the final failed attempt")
}

// TestRun_retriesOnATransientConnectFailure is the consumer-level proof:
// the REAL run() (real DSN parsing, real env var lookup, real migration
// application against a REAL testcontainers postgres) recovers from a
// simulated transient connect failure via migrateOpen, the exact
// injection point run() itself uses -- not a reimplementation of run()'s
// own logic.
func TestRun_retriesOnATransientConnectFailure(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	lookup := environment(map[string]string{
		migrationDSNEnvironment:                 dsn,
		migrationConnectRetriesEnvironment:      "3",
		migrationConnectRetryBackoffEnvironment: "1",
	})
	var stdout bytes.Buffer

	realOpen := migrateOpen
	failuresRemaining := 2
	migrateOpen = func(ctx context.Context, cfg runtimepostgres.Config) (*sql.DB, error) {
		if failuresRemaining > 0 {
			failuresRemaining--
			return nil, errors.New("simulated transient handshake failure")
		}
		return realOpen(ctx, cfg)
	}
	t.Cleanup(func() { migrateOpen = realOpen })

	// When
	err := run(ctx, []string{"up"}, lookup, &stdout)

	// Then
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "applied")
	require.Contains(t, stdout.String(), "connect attempt 1/3 failed: simulated transient handshake failure")
	require.Contains(t, stdout.String(), "connect attempt 2/3 failed: simulated transient handshake failure")
	require.Contains(t, stdout.String(), "connected to PostgreSQL on attempt 3/3")
	require.Equal(t, 0, failuresRemaining)
}

func TestPositiveIntEnv(t *testing.T) {
	env := func(vals map[string]string) lookupEnv {
		return func(key string) (string, bool) {
			v, ok := vals[key]
			return v, ok
		}
	}
	require.Equal(t, 5, positiveIntEnv(env(nil), "X", 5), "unset -> default")
	require.Equal(t, 5, positiveIntEnv(env(map[string]string{"X": ""}), "X", 5), "blank -> default")
	require.Equal(t, 5, positiveIntEnv(env(map[string]string{"X": "not-a-number"}), "X", 5), "non-numeric -> default")
	require.Equal(t, 5, positiveIntEnv(env(map[string]string{"X": "0"}), "X", 5), "zero -> default (not a valid attempt count)")
	require.Equal(t, 5, positiveIntEnv(env(map[string]string{"X": "-1"}), "X", 5), "negative -> default")
	require.Equal(t, 7, positiveIntEnv(env(map[string]string{"X": "7"}), "X", 5), "valid override honored")
}
