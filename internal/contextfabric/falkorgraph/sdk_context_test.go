package falkorgraph

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// delayingRESPServer answers every command after delay with a simple string, so a test measures how long the client
// waits on the socket, not what the reply means.
func delayingRESPServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
					time.Sleep(delay)
					if _, err := c.Write([]byte("+OK\r\n")); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func delayedSDKAPI(t *testing.T, delay time.Duration) *sdkAPI {
	t.Helper()
	api, err := newSDKAPI(Config{
		Addr: delayingRESPServer(t, delay), GraphPrefix: "acr-cf-ctx", RequestTimeout: 10 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 3, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return api.(*sdkAPI)
}

func TestReadsHonourTheCallersDeadlineOnTheNetworkIO(t *testing.T) {
	api := delayedSDKAPI(t, 800*time.Millisecond)
	for name, call := range map[string]func(context.Context) error{
		"indexes":    func(ctx context.Context) error { _, err := api.indexes(ctx, "g"); return err },
		"constraint": func(ctx context.Context) error { _, err := api.constraints(ctx, "g"); return err },
		"listGraphs": func(ctx context.Context) error { _, err := api.listGraphs(ctx); return err },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		began := time.Now()
		err := call(ctx)
		cancel()
		if spent := time.Since(began); spent > 650*time.Millisecond {
			t.Errorf("%s returned after %v with a 400ms caller deadline and an 800ms server: the read did not honour the deadline", name, spent)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s error = %v, want it to wrap context.DeadlineExceeded", name, err)
		}
	}
}

func TestReadsRefuseAnAlreadyCancelledContextAtOnce(t *testing.T) {
	api := delayedSDKAPI(t, 800*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	began := time.Now()
	_, err := api.indexes(ctx, "g")
	if spent := time.Since(began); spent > 200*time.Millisecond {
		t.Errorf("a read on a cancelled context returned after %v, want at once", spent)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
}

func TestWritesKeepRunningPastTheCallersDeadline(t *testing.T) {
	api := delayedSDKAPI(t, 800*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, _ = api.query(ctx, "g", "RETURN 1", nil, false)
	if spent := time.Since(began); spent < 750*time.Millisecond {
		t.Errorf("a write returned after %v with a 400ms caller deadline and an 800ms server: the write path must keep its present behaviour (a started write runs to the request timeout)", spent)
	}
}

func TestTheTwoClientsAreOptionedForTheirPathAndShareThePoolSize(t *testing.T) {
	api := delayedSDKAPI(t, time.Millisecond)
	read, write := api.read.Conn.(*redis.Client).Options(), api.db.Conn.(*redis.Client).Options()
	if !read.ContextTimeoutEnabled || write.ContextTimeoutEnabled {
		t.Fatalf("ContextTimeoutEnabled read=%v write=%v, want true and false", read.ContextTimeoutEnabled, write.ContextTimeoutEnabled)
	}
	if read.PoolSize != 3 || write.PoolSize != 3 {
		t.Fatalf("pool sizes read=%d write=%d, want the configured 3 each (one process holds at most 2 x PoolSize)", read.PoolSize, write.PoolSize)
	}
	if read.ReadTimeout != write.ReadTimeout || read.WriteTimeout != write.WriteTimeout || read.DialTimeout != write.DialTimeout || read.Addr != write.Addr {
		t.Fatalf("the two clients differ beyond the context flag: read %+v write %+v", read, write)
	}
}

// countingRESPServer parses RESP commands, answers HELLO with an error (so the
// client falls back to RESP2) and every other command with +OK at once, except
// GRAPH.QUERY and GRAPH.RO_QUERY: it counts each one and answers it only after delay.
func countingRESPServer(t *testing.T, delay time.Duration) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var graphQueries atomic.Int64
	readCommand := func(r *bufio.Reader) (string, error) {
		header, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
		if err != nil {
			return "", err
		}
		name := ""
		for i := 0; i < count; i++ {
			sizeLine, err := r.ReadString('\n')
			if err != nil {
				return "", err
			}
			size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(sizeLine, "$")))
			if err != nil {
				return "", err
			}
			payload := make([]byte, size+2)
			if _, err := io.ReadFull(r, payload); err != nil {
				return "", err
			}
			if i == 0 {
				name = strings.ToUpper(string(payload[:size]))
			}
		}
		return name, nil
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					name, err := readCommand(r)
					if err != nil {
						return
					}
					reply := "+OK\r\n"
					switch name {
					case "HELLO":
						reply = "-ERR unknown command 'HELLO'\r\n"
					case "GRAPH.QUERY", "GRAPH.RO_QUERY":
						graphQueries.Add(1)
						time.Sleep(delay)
					}
					if _, err := c.Write([]byte(reply)); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), &graphQueries
}

// TestTheWriteClientDoesNotResendAWriteWhoseReplyMissedTheRequestTimeout pins
// MaxRetries -1 on the write client: a write whose reply does not arrive within
// the RequestTimeout returns its error after one send. The projector replay is
// the only retry; every write statement stays replay-idempotent.
func TestTheWriteClientDoesNotResendAWriteWhoseReplyMissedTheRequestTimeout(t *testing.T) {
	addr, graphQueries := countingRESPServer(t, 1500*time.Millisecond)
	api, err := newSDKAPI(Config{
		Addr: addr, GraphPrefix: "acr-cf-ctx", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 3, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := api.(*sdkAPI)
	if got := s.db.Conn.(*redis.Client).Options().MaxRetries; got != 0 {
		t.Fatalf("write client MaxRetries = %d, want 0 (configured -1: no driver resend)", got)
	}
	_, err = s.query(context.Background(), "g", "CREATE (:Probe)", nil, false)
	if err == nil {
		t.Fatal("a write slower than the request timeout returned no error")
	}
	if got := graphQueries.Load(); got != 1 {
		t.Fatalf("one write call sent GRAPH.QUERY %d times, want 1", got)
	}
}

// TestTheReadClientKeepsTheDriverRetries pins that the read client still
// resends a read whose reply missed the RequestTimeout (driver default 3).
func TestTheReadClientKeepsTheDriverRetries(t *testing.T) {
	addr, graphQueries := countingRESPServer(t, 1500*time.Millisecond)
	api, err := newSDKAPI(Config{
		Addr: addr, GraphPrefix: "acr-cf-ctx", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 3, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := api.(*sdkAPI)
	options := s.read.Conn.(*redis.Client).Options()
	if options.MaxRetries != 3 {
		t.Fatalf("read client MaxRetries = %d, want the driver default 3", options.MaxRetries)
	}
	_, err = s.query(context.Background(), "g", "MATCH (n) RETURN n", nil, true)
	if err == nil {
		t.Fatal("a read slower than the request timeout returned no error")
	}
	if got, want := graphQueries.Load(), int64(options.MaxRetries+1); got != want {
		t.Fatalf("one read call sent GRAPH.RO_QUERY %d times, want %d", got, want)
	}
}

// TestAnOptionsCopyOfTheWriteClientReinitsToDriverRetries pins a trap: the
// write client is configured with MaxRetries -1, which go-redis stores as 0,
// so a copy of its Options() passed to redis.NewClient is read as "unset" and
// becomes the default 3 again unless -1 is set on the copy.
func TestAnOptionsCopyOfTheWriteClientReinitsToDriverRetries(t *testing.T) {
	api, err := newSDKAPI(Config{
		Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-ctx", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 3, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	options := *api.(*sdkAPI).db.Conn.(*redis.Client).Options()
	if options.MaxRetries != 0 {
		t.Fatalf("write client MaxRetries = %d, want 0 (configured -1)", options.MaxRetries)
	}
	reinit := redis.NewClient(&options)
	t.Cleanup(func() { _ = reinit.Close() })
	options.MaxRetries = -1
	fixed := redis.NewClient(&options)
	t.Cleanup(func() { _ = fixed.Close() })
	t.Logf("copy-reinit=%d fixed=%d", reinit.Options().MaxRetries, fixed.Options().MaxRetries)
	if reinit.Options().MaxRetries != 3 || fixed.Options().MaxRetries != 0 {
		t.Fatalf("copy-reinit=%d fixed=%d, want 3 and 0", reinit.Options().MaxRetries, fixed.Options().MaxRetries)
	}
}
