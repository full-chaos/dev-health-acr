package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
			return nil, runtimepostgres.ErrUnavailable
		}
		return &sql.DB{}, nil
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 5, 0, nil); err != nil || calls != 3 {
		t.Fatalf("want success on attempt 3, got err=%v calls=%d", err, calls)
	}
}

func TestChaos7168_OpenPostgresIsBoundedAndSkipsNonTransient(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, runtimepostgres.ErrUnavailable
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 5, 0, nil); err == nil || calls != 5 {
		t.Fatalf("want bounded %d attempts and error, got err=%v calls=%d", 5, err, calls)
	}
	calls = 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, errors.New("invalid PostgreSQL configuration")
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 5, 0, nil); err == nil || calls != 1 {
		t.Fatalf("config error must not retry, got err=%v calls=%d", err, calls)
	}
}

// A message-only match must NOT retry: a different error value carrying the
// same text is not the reachability sentinel.
func TestChaos7168_OpenPostgresClassifiesByErrorIdentityNotMessage(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, errors.New("PostgreSQL is unavailable")
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 5, 0, nil); err == nil || calls != 1 {
		t.Fatalf("message-only match must not retry, got err=%v calls=%d", err, calls)
	}
}

// A wrapped sentinel is still retried, and the configured count is honored.
func TestChaos7168_OpenPostgresHonorsConfiguredAttemptsAndWrappedSentinel(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, fmt.Errorf("dial: %w", runtimepostgres.ErrUnavailable)
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 3, 0, nil); err == nil || calls != 3 {
		t.Fatalf("want 3 attempts, got err=%v calls=%d", err, calls)
	}
}

// Zero attempts/backoff (a Config literal that never went through config.Load)
// must mean the default at the retry site, not a single attempt.
func TestChaos7168_ZeroValueMeansDefaultAttemptsAtTheRetrySite(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		calls++
		return nil, runtimepostgres.ErrUnavailable
	})
	if _, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 0, 0, nil); err == nil || calls != defaultPostgresStartupAttempts {
		t.Fatalf("zero attempts must retry %d times, got err=%v calls=%d", defaultPostgresStartupAttempts, err, calls)
	}
}

// The error the REAL runtimepostgres.Open returns for an unreachable server,
// wrapped in a %w chain, must still be classified as retryable.
func TestChaos7168_RealOpenErrorSurvivesWrappingAndIsRetried(t *testing.T) {
	calls := 0
	stubPostgresOpen(t, func(ctx context.Context, cfg runtimepostgres.Config) (*sql.DB, error) {
		calls++
		_, err := runtimepostgres.Open(ctx, cfg)
		return nil, fmt.Errorf("open postgres: %w", err)
	})
	cfg := runtimepostgres.Config{DSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1", PingTimeout: 500 * time.Millisecond}
	if _, err := openPostgresWithRetry(context.Background(), cfg, 3, time.Millisecond, nil); !errors.Is(err, runtimepostgres.ErrUnavailable) || calls != 3 {
		t.Fatalf("want 3 attempts and ErrUnavailable through the chain, got err=%v calls=%d", err, calls)
	}
}
