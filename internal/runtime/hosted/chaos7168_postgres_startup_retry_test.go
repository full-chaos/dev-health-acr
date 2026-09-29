package hosted

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
)

func stubPostgresOpen(t *testing.T, fn func(context.Context, runtimepostgres.Config) (*sql.DB, error)) {
	t.Helper()
	oldOpen, oldSleep := postgresOpenFn, postgresOpenSleep
	postgresOpenFn = fn
	postgresOpenSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { postgresOpenFn, postgresOpenSleep = oldOpen, oldSleep })
}

// CHAOS-7168: a flaky dialer that is unavailable for the first two attempts
// (the prod signature right after the migrate hook) must still open.
func TestChaos7168_OpenPostgresRetriesTransientUnavailable(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New("PostgreSQL is unavailable")
		}
		return &sql.DB{}, nil
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, nil); err != nil || calls != 3 {
		t.Fatalf("want success on attempt 3, got err=%v calls=%d", err, calls)
	}
}

func TestChaos7168_OpenPostgresIsBoundedAndSkipsNonTransient(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, errors.New("PostgreSQL is unavailable")
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, nil); err == nil || calls != postgresOpenAttempts {
		t.Fatalf("want bounded %d attempts and error, got err=%v calls=%d", postgresOpenAttempts, err, calls)
	}
	calls = 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, errors.New("invalid PostgreSQL configuration")
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, nil); err == nil || calls != 1 {
		t.Fatalf("config error must not retry, got err=%v calls=%d", err, calls)
	}
}
