package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/redis/go-redis/v9"
)

const (
	liveSettleTries = 5
	liveSettlePause = 200 * time.Millisecond

	liveSettleMaxFirstRuns = 2000
	liveSettleFirstBound   = 30 * time.Second
)

// settleLiveFalkor runs the schema bootstrap on a throwaway graph through the adapter's own client before a live
// test body starts, so a connection the docker port drops in the container's first seconds is absorbed here. Only
// the unclassified dependency error is retried; a classified error (unauthorized, timeout, constraint) is final.
// The test body keeps the production default MaxAttempts 3: a drop after this step still fails a write (writes are
// sent once), and a read gets the same retry it gets in production.
func settleLiveFalkor(ctx context.Context, adapter *Adapter, tries int, pause time.Duration) error {
	org := "settle-14310-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	defer func() { _ = adapter.PurgeOrganization(context.Background(), org) }()
	return settleWith(ctx, tries, pause, func(ctx context.Context) error { return adapter.bootstrapSchema(ctx, key) })
}

// settleWith's errors name FalkorDB, the tries and the driver cause, never the
// address.
func settleWith(ctx context.Context, tries int, pause time.Duration, attempt func(context.Context) error) error {
	began := time.Now()
	var last error
	for try := 1; try <= tries; try++ {
		if last = attempt(ctx); last == nil {
			return nil
		}
		if !retryableSettleError(last) {
			return fmt.Errorf("FalkorDB live dependency failed on try %d (not retried): %w", try, last)
		}
		if try < tries {
			select {
			case <-ctx.Done():
				return fmt.Errorf("FalkorDB live dependency: %w", ctx.Err())
			case <-time.After(pause):
			}
		}
	}
	cause, _ := contextfabric.DependencyCause(last)
	return fmt.Errorf("FalkorDB live dependency unavailable after %d tries over %s (driver cause %q): %w", tries, time.Since(began).Round(time.Millisecond), cause, last)
}

func retryableSettleError(err error) bool {
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		return false
	}
	for _, sentinel := range knownSentinels {
		if errors.Is(err, sentinel) {
			return false
		}
	}
	return true
}

func startSettleFalkor(t *testing.T, ctx context.Context) (*Adapter, string) {
	t.Helper()
	container := falkorfixture.Start(ctx, t, codexRoundFalkordbImage)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := host + ":" + port.Port()
	adapter, err := New(Config{
		Addr: addr, GraphPrefix: "acr-cf-settle", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, addr
}

// CHAOS-14310 C1: the connections of a live FalkorDB are killed until the first bootstrap try has failed; the
// settle step absorbs it and the next try passes. The injected shape matches the CI error; the natural cause of the
// CI flake is not proven.
func TestLiveSettleAbsorbsConnectionsDroppedDuringTheFirstBootstrap(t *testing.T) {
	ctx := context.Background()
	adapter, addr := startSettleFalkor(t, ctx)
	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })

	firstFailed := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-firstFailed:
				return
			default:
				raw.Do(ctx, "CLIENT", "KILL", "TYPE", "normal", "SKIPME", "yes")
				time.Sleep(time.Millisecond)
			}
		}
	}()

	org := "settle-c1-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })
	calls := 0
	err := settleWith(ctx, liveSettleTries, 50*time.Millisecond, func(ctx context.Context) error {
		calls++
		err := adapter.bootstrapSchema(ctx, key)
		if calls == 1 {
			// bootstrapSchema is idempotent (create index/constraint treat "already exists" as success), so
			// re-run it under the kills until one run errors: the first try fails by count, not by timing.
			runs := 1
			began := time.Now()
			for err == nil && runs < liveSettleMaxFirstRuns && time.Since(began) < liveSettleFirstBound {
				err = adapter.bootstrapSchema(ctx, key)
				runs++
			}
			t.Logf("first bootstrap error after %d runs", runs)
			close(firstFailed)
			<-stopped
			if err == nil {
				t.Fatalf("premise: no bootstrap run failed under the connection kills within %d runs / %s", liveSettleMaxFirstRuns, liveSettleFirstBound)
			}
		}
		return err
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want a retry after the first failed try", calls)
	}
	names, err := adapter.api.listGraphs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := settleLiveFalkor(ctx, adapter, 1, 0); err != nil {
		t.Fatalf("settleLiveFalkor on a quiet server: %v", err)
	}
	after, err := adapter.api.listGraphs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range after {
		if strings.Contains(name, "settle-14310-") {
			t.Fatalf("throwaway settle graph %q left behind (graphs before: %v)", name, names)
		}
	}
}

// C2: a dependency that stays down fails inside the bound with a message that names FalkorDB, the tries and the
// driver cause, never the address, and carries the typed unavailable class.
func TestLiveSettleOnADependencyThatStaysDownNamesFalkorDB(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	adapter, err := New(Config{Addr: addr, GraphPrefix: "acr-cf-settle-down", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 2, AllowInsecure: true})
	if err != nil {
		t.Fatalf("New against a closed port: %v", err)
	}
	began := time.Now()
	err = settleLiveFalkor(context.Background(), adapter, 3, 10*time.Millisecond)
	if err == nil {
		t.Fatal("settle passed against a closed port")
	}
	if elapsed := time.Since(began); elapsed > 20*time.Second {
		t.Fatalf("settle took %s, want it bounded by its tries", elapsed)
	}
	for _, want := range []string{"FalkorDB live dependency unavailable after 3 tries", `driver cause "connection_refused"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err, want)
		}
	}
	_, port, _ := net.SplitHostPort(addr)
	for _, planted := range []string{addr, "127.0.0.1", ":" + port} {
		if strings.Contains(err.Error(), planted) {
			t.Fatalf("error = %q names the address (%q)", err, planted)
		}
	}
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("error = %v, want the unavailable class kept", err)
	}
}

// C3: only the unclassified dependency error is retried.
func TestSettleRetriesOnlyTheUnclassifiedDependencyError(t *testing.T) {
	unclassified := safeDependencyError("bootstrap relationship index", errors.New("EOF"))
	cases := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"unclassified dependency error", unclassified, 3},
		{"unauthorized", fmt.Errorf("create constraint: %w", ErrUnauthorized), 1},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), 1},
		{"constraint violation", fmt.Errorf("query: %w", ErrConstraintViolation), 1},
		{"unrelated error", errors.New("boom"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			err := settleWith(context.Background(), 3, time.Millisecond, func(context.Context) error { calls++; return c.err })
			if err == nil || !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want it to wrap %v", err, c.err)
			}
			if calls != c.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, c.wantCalls)
			}
		})
	}
	calls := 0
	if err := settleWith(context.Background(), 3, time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return unclassified
		}
		return nil
	}); err != nil || calls != 3 {
		t.Fatalf("err = %v calls = %d, want a pass on the third try", err, calls)
	}
}
