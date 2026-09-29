package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

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

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func attemptEvents(h *captureHandler) (outcomes, classes []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message != postgresStartupAttemptEvent {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "outcome":
				outcomes = append(outcomes, a.Value.String())
			case "failure_class":
				classes = append(classes, a.Value.String())
			}
			return true
		})
	}
	return outcomes, classes
}

// Review P1: the terminal attempt is a FAILED attempt and must be logged, so
// the number of events equals the number of attempts the server saw.
func TestChaos7168_EveryFailedAttemptIsLoggedIncludingTheTerminalOne(t *testing.T) {
	stubPostgresOpen(t, func(context.Context, runtimepostgres.Config) (*sql.DB, error) {
		return nil, runtimepostgres.ErrUnavailable
	})
	h := &captureHandler{}
	_, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{}, 3, time.Millisecond, slog.New(h))
	outcomes, classes := attemptEvents(h)
	if err == nil || !reflect.DeepEqual(outcomes, []string{"retrying", "retrying", "exhausted"}) || !reflect.DeepEqual(classes, []string{"postgres_unavailable", "postgres_unavailable", "postgres_unavailable"}) {
		t.Fatalf("want 3 events retrying,retrying,exhausted; got err=%v outcomes=%v classes=%v", err, outcomes, classes)
	}
}

// fakeHostedPostgres answers every connection with a fatal ErrorResponse.
func fakeHostedPostgres(t *testing.T, sqlState string) (dsn string, connections *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	connections = &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer conn.Close()
				backend := pgproto3.NewBackend(conn, conn)
				if _, err := backend.ReceiveStartupMessage(); err != nil {
					return
				}
				backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: sqlState, Message: "fake"})
				_ = backend.Flush()
			}()
		}
	}()
	return "postgres://u:p@" + listener.Addr().String() + "/db?sslmode=disable", connections
}

// Review P1: a credential rejection through the REAL Open must not be retried
// (the server must see exactly one connection) and must be logged once.
func TestChaos7168_RealAuthRejectionIsNotRetriedAndIsLogged(t *testing.T) {
	dsn, connections := fakeHostedPostgres(t, "28P01")
	h := &captureHandler{}
	_, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{DSN: dsn, PingTimeout: 2 * time.Second}, 3, time.Millisecond, slog.New(h))
	outcomes, classes := attemptEvents(h)
	if !errors.Is(err, runtimepostgres.ErrRejected) || connections.Load() != 1 || !reflect.DeepEqual(outcomes, []string{"not_retryable"}) || !reflect.DeepEqual(classes, []string{"postgres_rejected"}) {
		t.Fatalf("want 1 connection, ErrRejected, one not_retryable event; got err=%v conns=%d outcomes=%v classes=%v", err, connections.Load(), outcomes, classes)
	}
}

// A server that answers 57P03 (starting up) is the case this change exists
// for: retried to the bound through the real Open, three connections.
func TestChaos7168_RealServerAnswerOf57P03IsRetriedToTheBound(t *testing.T) {
	dsn, connections := fakeHostedPostgres(t, "57P03")
	_, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{DSN: dsn, PingTimeout: 2 * time.Second}, 3, time.Millisecond, nil)
	if !errors.Is(err, runtimepostgres.ErrUnavailable) || connections.Load() != 3 {
		t.Fatalf("want 3 connections and ErrUnavailable, got err=%v conns=%d", err, connections.Load())
	}
}

// Any other server answer (here 3D000 missing database) is terminal: one connection.
func TestChaos7168_RealServerAnswerOf3D000IsTerminal(t *testing.T) {
	dsn, connections := fakeHostedPostgres(t, "3D000")
	_, err := openPostgresWithRetry(context.Background(), runtimepostgres.Config{DSN: dsn, PingTimeout: 2 * time.Second}, 3, time.Millisecond, nil)
	if !errors.Is(err, runtimepostgres.ErrRejected) || connections.Load() != 1 {
		t.Fatalf("want 1 connection and ErrRejected, got err=%v conns=%d", err, connections.Load())
	}
}

// Unreachable server through the REAL Open: three attempts, three WARN events,
// and an exhaustion error that carries the attempt count and class.
func TestChaos7168_UnreachableServerLogsThreeEventsAndErrorCarriesAttemptAndClass(t *testing.T) {
	h := &captureHandler{}
	cfg := runtimepostgres.Config{DSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable", PingTimeout: 500 * time.Millisecond}
	_, err := openPostgresWithRetry(context.Background(), cfg, 3, time.Millisecond, slog.New(h))
	outcomes, classes := attemptEvents(h)
	if err == nil || !errors.Is(err, runtimepostgres.ErrUnavailable) || !strings.Contains(err.Error(), "attempt 3/3 (postgres_unavailable)") ||
		!reflect.DeepEqual(outcomes, []string{"retrying", "retrying", "exhausted"}) || len(classes) != 3 {
		t.Fatalf("got err=%v outcomes=%v classes=%v", err, outcomes, classes)
	}
}
