package chfixture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
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
	return FalkorReadyWithin(falkorServeBudget)
}

// FalkorReadyWithin is FalkorReady with an explicit budget.
func FalkorReadyWithin(budget time.Duration) wait.Strategy {
	return falkorServing{budget: budget}
}

// ErrPublishedPortUnreachable marks a failed wait where the server answered PING inside the container for a
// sustained time while the host-mapped port never answered. A fresh container gets a new port mapping, so this is
// the one readiness failure a caller may retry; every other failure is a hard fail.
var ErrPublishedPortUnreachable = errors.New("server answers inside the container but the published port never did")

// PortUnreachableError is the ErrPublishedPortUnreachable failure with the facts the fixture's log line reports.
type PortUnreachableError struct {
	Port    string
	Tries   int
	Elapsed time.Duration
	Err     error
}

func (e *PortUnreachableError) Error() string        { return e.Err.Error() }
func (e *PortUnreachableError) Unwrap() error        { return e.Err }
func (e *PortUnreachableError) Is(target error) bool { return target == ErrPublishedPortUnreachable }

const falkorUnreachableAfter = 10 * time.Second

type falkorServing struct{ budget time.Duration }

func (f falkorServing) WaitUntilReady(ctx context.Context, target wait.StrategyTarget) error {
	host, err := target.Host(ctx)
	if err != nil {
		return fmt.Errorf("falkordb ready: host: %w", err)
	}
	port, err := target.MappedPort(ctx, "6379/tcp")
	if err != nil {
		return fmt.Errorf("falkordb ready: mapped port: %w", err)
	}
	inside := func(ctx context.Context) error {
		code, out, err := target.Exec(ctx, []string{"redis-cli", "ping"})
		if err != nil {
			return fmt.Errorf("exec redis-cli ping: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(out, 4096))
		if code != 0 || !strings.Contains(string(body), "PONG") {
			return fmt.Errorf("redis-cli ping inside the container: exit %d, output %q", code, strings.TrimSpace(string(body)))
		}
		return nil
	}
	return awaitFalkorServingInside(ctx, net.JoinHostPort(host, port.Port()), f.budget, falkorServeTryLimit, falkorServePoll, falkorUnreachableAfter, inside, log.Printf)
}

func awaitFalkorServing(ctx context.Context, addr string, budget, tryLimit, poll time.Duration, logf func(string, ...any)) error {
	return awaitFalkorServingInside(ctx, addr, budget, tryLimit, poll, 0, nil, logf)
}

// awaitFalkorServingInside probes addr from the runner. When inside is set, every failed try also asks the server
// from inside the container, so a dead published port is told apart from a server that is not up; unreachableAfter
// is how long that must hold before the wait gives up early with ErrPublishedPortUnreachable.
func awaitFalkorServingInside(ctx context.Context, addr string, budget, tryLimit, poll, unreachableAfter time.Duration, inside func(context.Context) error, logf func(string, ...any)) error {
	began := time.Now()
	deadline := began.Add(budget)
	graph, err := probeGraphName()
	if err != nil {
		return err
	}
	defer deleteProbeGraph(newFalkorClient(addr, tryLimit), graph, tryLimit, logf)
	var last error
	var history []string
	var insideOKSince time.Time
	for try := 1; ; try++ {
		client := newFalkorClient(addr, tryLimit)
		last = probeFalkor(ctx, client, graph, tryLimit)
		_ = client.Close()
		if last == nil {
			logf("falkordb serving on %s after %dms, %d tries", addr, time.Since(began).Milliseconds(), try)
			return nil
		}
		line := fmt.Sprintf("try %d at %dms: %v", try, time.Since(began).Milliseconds(), last)
		if inside != nil {
			insideCtx, cancel := context.WithTimeout(ctx, tryLimit)
			insideErr := inside(insideCtx)
			cancel()
			if insideErr == nil {
				line += "; inside the container: answers PING"
				if insideOKSince.IsZero() {
					insideOKSince = time.Now()
				}
			} else {
				line += fmt.Sprintf("; inside the container: %v", insideErr)
				insideOKSince = time.Time{}
			}
		}
		history = append(history, line)
		logf("falkordb not serving on %s yet (%s)", addr, line)
		if ctx.Err() != nil {
			return fmt.Errorf("falkordb not serving on %s: %w (last probe error: %v)", addr, ctx.Err(), last)
		}
		if inside != nil && !insideOKSince.IsZero() && time.Since(insideOKSince) >= unreachableAfter {
			_, port, _ := net.SplitHostPort(addr)
			return &PortUnreachableError{Port: port, Tries: try, Elapsed: time.Since(began).Round(time.Millisecond), Err: fmt.Errorf("falkordb not serving on %s after %s: %w: %w\n%s", addr, time.Since(began).Round(time.Millisecond), ErrPublishedPortUnreachable, last, summarizeTries(history))}
		}
		if time.Now().Add(poll).After(deadline) {
			return fmt.Errorf("falkordb not serving on %s within %s: %w\n%s", addr, budget, last, summarizeTries(history))
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

func summarizeTries(history []string) string {
	const edge = 5
	if len(history) <= 2*edge {
		return strings.Join(history, "\n")
	}
	skipped := fmt.Sprintf("... %d tries omitted ...", len(history)-2*edge)
	return strings.Join(append(append(append([]string{}, history[:edge]...), skipped), history[len(history)-edge:]...), "\n")
}

func newFalkorClient(addr string, tryLimit time.Duration) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, Protocol: 2, DisableIdentity: true, DialTimeout: tryLimit, ReadTimeout: tryLimit, WriteTimeout: tryLimit, Dialer: loopbackDialer(tryLimit)})
}

// loopbackDialer dials every address the host name resolves to, IPv4 first, each for its share of the try, so a
// name that resolves to ::1 and 127.0.0.1 cannot park the whole try on one dead family.
func loopbackDialer(tryLimit time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(ips, func(i, j int) bool { return ips[i].IP.To4() != nil && ips[j].IP.To4() == nil })
		var errs []error
		for _, ip := range ips {
			dctx, cancel := context.WithTimeout(ctx, tryLimit/time.Duration(len(ips)))
			conn, err := (&net.Dialer{}).DialContext(dctx, "tcp", net.JoinHostPort(ip.String(), port))
			cancel()
			if err == nil {
				return conn, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
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
	defer func() { _ = client.Close() }()
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
