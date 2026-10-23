package chfixture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strings"
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
// every failed try, and fails with the last error when the budget ends. It also creates an index on a throwaway
// graph, the command the adapter's schema bootstrap issues first, and deletes that graph on every path.
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
	graph, err := probeGraphName()
	if err != nil {
		return err
	}
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, Protocol: 2, DisableIdentity: true, DialTimeout: tryLimit, ReadTimeout: tryLimit, WriteTimeout: tryLimit})
	defer func() { _ = client.Close() }()
	defer deleteProbeGraph(client, graph, tryLimit, logf)
	var last error
	for try := 1; ; try++ {
		last = probeFalkor(ctx, client, graph, tryLimit)
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

func probeGraphName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("falkordb ready: probe graph name: %w", err)
	}
	return "acr_ready_probe_" + hex.EncodeToString(b[:]), nil
}

func deleteProbeGraph(client *redis.Client, graph string, tryLimit time.Duration, logf func(string, ...any)) {
	ctx, cancel := context.WithTimeout(context.Background(), tryLimit)
	defer cancel()
	if err := client.Do(ctx, "GRAPH.DELETE", graph).Err(); err != nil && !strings.Contains(err.Error(), "empty key") {
		logf("falkordb probe graph %s not deleted: %v", graph, err)
	}
}

func probeFalkor(ctx context.Context, client *redis.Client, graph string, tryLimit time.Duration) error {
	tryCtx, cancel := context.WithTimeout(ctx, tryLimit)
	defer cancel()
	if err := client.Ping(tryCtx).Err(); err != nil {
		return fmt.Errorf("PING: %w", err)
	}
	if err := client.Do(tryCtx, "GRAPH.LIST").Err(); err != nil {
		return fmt.Errorf("GRAPH.LIST: %w", err)
	}
	if err := client.Do(tryCtx, "GRAPH.QUERY", graph, "CREATE INDEX FOR (n:ReadyProbe) ON (n.k)").Err(); err != nil && !alreadyIndexed(err) {
		return fmt.Errorf("GRAPH.QUERY CREATE INDEX: %w", err)
	}
	return nil
}

// alreadyIndexed reports the error a repeated CREATE INDEX gets when an earlier try executed but its reply was lost;
// the adapter's schema bootstrap treats the same error as success.
func alreadyIndexed(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already indexed") || strings.Contains(msg, "already exists")
}
