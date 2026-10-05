package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// replayPods opens n separate connection pools and stores on ONE database:
// n acr-api pods sharing one used-id record.
func replayPods(t *testing.T, ctx context.Context, n int) ([]*WebAssertionReplayStore, *sql.DB) {
	t.Helper()
	server := sharedPostgresFixture(t)
	dsn := server.dsnFor(server.createDatabase(t, ctx))
	stores := make([]*WebAssertionReplayStore, n)
	var first *sql.DB
	for i := range stores {
		db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		if first == nil {
			first = db
		}
		stores[i], err = NewWebAssertionReplayStore(db, nil)
		require.NoError(t, err)
	}
	return stores, first
}

func TestWebAssertionReplayStore_refusesSecondUseFromAnotherInstance(t *testing.T) {
	ctx := context.Background()
	pods, _ := replayPods(t, ctx, 2)
	exp := time.Now().Add(30 * time.Second)

	replay, err := pods[0].Observe(ctx, "https://web.example.test", "jti-1", exp, time.Now())
	require.NoError(t, err)
	require.False(t, replay, "first use is accepted")

	replay, err = pods[1].Observe(ctx, "https://web.example.test", "jti-1", exp, time.Now())
	require.NoError(t, err)
	require.True(t, replay, "the same id on the other pod is a replay")

	replay, err = pods[0].Observe(ctx, "https://web.example.test", "jti-1", exp, time.Now())
	require.NoError(t, err)
	require.True(t, replay, "and on the first pod again")
}

func TestWebAssertionReplayStore_sameIDFromDifferentIssuersDoesNotCollide(t *testing.T) {
	ctx := context.Background()
	pods, _ := replayPods(t, ctx, 2)
	exp := time.Now().Add(30 * time.Second)

	replay, err := pods[0].Observe(ctx, "https://issuer-a.example.test", "shared-jti", exp, time.Now())
	require.NoError(t, err)
	require.False(t, replay)
	replay, err = pods[1].Observe(ctx, "https://issuer-b.example.test", "shared-jti", exp, time.Now())
	require.NoError(t, err)
	require.False(t, replay)
}

func TestWebAssertionReplayStore_concurrentFirstUseAcceptsExactlyOne(t *testing.T) {
	ctx := context.Background()
	pods, _ := replayPods(t, ctx, 2)
	exp := time.Now().Add(30 * time.Second)

	for round := 0; round < 100; round++ {
		jti := "race-" + time.Now().Format("150405.000000000") + "-" + string(rune('a'+round%26)) + string(rune('A'+round/26))
		var accepted atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(store *WebAssertionReplayStore) {
				defer wg.Done()
				<-start
				replay, err := store.Observe(ctx, "https://web.example.test", jti, exp, time.Now())
				if err == nil && !replay {
					accepted.Add(1)
				}
			}(pods[i%2])
		}
		close(start)
		wg.Wait()
		require.EqualValues(t, 1, accepted.Load(), "round %d: exactly one concurrent first use is accepted", round)
	}
}

func TestWebAssertionReplayStore_sweepRemovesOnlyExpiredRowsInBoundedBatches(t *testing.T) {
	ctx := context.Background()
	pods, db := replayPods(t, ctx, 1)
	count := func() int {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.web_assertion_replays`).Scan(&n))
		return n
	}
	// Rows far past retention, rows inside the retention window, and live rows.
	_, err := db.ExecContext(ctx, `INSERT INTO acr.web_assertion_replays (issuer, jti, expires_at)
		SELECT 'i', 'old-' || g, now() - interval '1 hour' FROM generate_series(1, 250) g`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO acr.web_assertion_replays (issuer, jti, expires_at) VALUES
		('i', 'recent', now() - interval '10 seconds'), ('i', 'live', now() + interval '20 seconds')`)
	require.NoError(t, err)
	require.Equal(t, 252, count())

	pods[0].sweep(ctx)
	require.Equal(t, 152, count(), "one sweep deletes at most one batch")
	pods[0].sweep(ctx)
	pods[0].sweep(ctx)
	require.Equal(t, 2, count(), "only the expired rows are removed")
	var kept int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.web_assertion_replays WHERE jti IN ('recent','live')`).Scan(&kept))
	require.Equal(t, 2, kept, "a row inside the retention window and a live row stay")
}

func TestWebAssertionReplayStore_sweepRunsOnWritesAndTwoPodsSweepConcurrently(t *testing.T) {
	ctx := context.Background()
	pods, db := replayPods(t, ctx, 2)
	_, err := db.ExecContext(ctx, `INSERT INTO acr.web_assertion_replays (issuer, jti, expires_at)
		SELECT 'i', 'old-' || g, now() - interval '1 hour' FROM generate_series(1, 90) g`)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for p := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < webAssertionReplaySweepEach*2; i++ {
				_, err := pods[p].Observe(ctx, "https://web.example.test", "w-"+string(rune('a'+p))+"-"+time.Now().Format("150405.000000000"), time.Now().Add(20*time.Second), time.Now())
				require.NoError(t, err)
			}
		}()
	}
	wg.Wait()
	var old int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.web_assertion_replays WHERE jti LIKE 'old-%'`).Scan(&old))
	require.Zero(t, old, "writes sweep expired rows without a timer")
}

func TestWebAssertionReplayStore_failedSweepNeverChangesTheDecision(t *testing.T) {
	ctx := context.Background()
	server := sharedPostgresFixture(t)
	name := server.createDatabase(t, ctx)
	admin, err := pgx.ParseConfig(server.dsnFor(name))
	require.NoError(t, err)
	adminDB := stdlib.OpenDB(*admin)
	t.Cleanup(func() { _ = adminDB.Close() })
	for _, statement := range []string{
		`CREATE ROLE replay_no_delete LOGIN PASSWORD 'replay-test'`,
		`GRANT USAGE ON SCHEMA acr TO replay_no_delete`,
		`GRANT SELECT, INSERT ON acr.web_assertion_replays TO replay_no_delete`,
	} {
		_, err := adminDB.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	limited := *admin
	limited.User, limited.Password = "replay_no_delete", "replay-test"
	limitedDB := stdlib.OpenDB(limited)
	t.Cleanup(func() { _ = limitedDB.Close() })

	var logs bytes.Buffer
	store, err := NewWebAssertionReplayStore(limitedDB, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	for i := 0; i < webAssertionReplaySweepEach; i++ {
		replay, err := store.Observe(ctx, "i", "j-"+string(rune('a'+i)), time.Now().Add(20*time.Second), time.Now())
		require.NoError(t, err, "a sweep that the role cannot run must not fail the assertion")
		require.False(t, replay)
	}
	require.Contains(t, logs.String(), "web_assertion_replay_sweep")
	require.NotContains(t, logs.String(), "j-")
}

func TestWebAssertionReplayStore_refusesWhenStoreCannotAnswer(t *testing.T) {
	ctx := context.Background()
	pods, db := replayPods(t, ctx, 1)
	_, err := db.ExecContext(ctx, `DROP TABLE acr.web_assertion_replays`)
	require.NoError(t, err)
	_, err = pods[0].Observe(ctx, "i", "j", time.Now().Add(20*time.Second), time.Now())
	require.Error(t, err, "an absent table is an error, never an accepted assertion")

	closed, _ := replayPods(t, ctx, 1)
	require.NoError(t, closed[0].db.Close())
	_, err = closed[0].Observe(ctx, "i", "j", time.Now().Add(20*time.Second), time.Now())
	require.Error(t, err, "a closed pool is an error, never an accepted assertion")
}
