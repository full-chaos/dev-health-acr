package chfixture

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	falkorServeBudget   = 2 * time.Minute
	falkorServeTryLimit = 3 * time.Second
	falkorServePoll     = 250 * time.Millisecond
)

// FalkorReady is the wait strategy for a FalkorDB container. A host-mapped port is open as soon as the container
// network is up, before the server answers, so a bare listening-port wait reports ready while the first command
// still hangs. This strategy returns only after the server answers PING and GRAPH.LIST on the mapped port, logs
// every failed try, and fails with the last error when the budget ends.
func FalkorReady() wait.Strategy {
	return falkorServing{}
}

type falkorServing struct{}

func (falkorServing) WaitUntilReady(ctx context.Context, target wait.StrategyTarget) error {
	host, err := target.Host(ctx)
	if err != nil {
		return fmt.Errorf("falkordb ready: host: %w", err)
	}
	port, err := target.MappedPort(ctx, "6379/tcp")
	if err != nil {
		return fmt.Errorf("falkordb ready: mapped port: %w", err)
	}
	return awaitFalkorServing(ctx, net.JoinHostPort(host, port.Port()), falkorServeBudget, falkorServeTryLimit, falkorServePoll, log.Printf)
}

func awaitFalkorServing(ctx context.Context, addr string, budget, tryLimit, poll time.Duration, logf func(string, ...any)) error {
	began := time.Now()
	deadline := began.Add(budget)
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, Protocol: 2, DisableIdentity: true, DialTimeout: tryLimit, ReadTimeout: tryLimit, WriteTimeout: tryLimit})
	defer func() { _ = client.Close() }()
	var last error
	for try := 1; ; try++ {
		last = probeFalkor(ctx, client, tryLimit)
		if last == nil {
			logf("falkordb serving on %s after %dms, %d tries", addr, time.Since(began).Milliseconds(), try)
			return nil
		}
		logf("falkordb not serving on %s yet (try %d, %dms): %v", addr, try, time.Since(began).Milliseconds(), last)
		if ctx.Err() != nil {
			return fmt.Errorf("falkordb not serving on %s: %w (last probe error: %v)", addr, ctx.Err(), last)
		}
		if time.Now().Add(poll).After(deadline) {
			return fmt.Errorf("falkordb not serving on %s within %s: %w", addr, budget, last)
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

func probeFalkor(ctx context.Context, client *redis.Client, tryLimit time.Duration) error {
	tryCtx, cancel := context.WithTimeout(ctx, tryLimit)
	defer cancel()
	if err := client.Ping(tryCtx).Err(); err != nil {
		return fmt.Errorf("PING: %w", err)
	}
	if err := client.Do(tryCtx, "GRAPH.LIST").Err(); err != nil {
		return fmt.Errorf("GRAPH.LIST: %w", err)
	}
	return nil
}
