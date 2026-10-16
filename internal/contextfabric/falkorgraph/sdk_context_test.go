package falkorgraph

import (
	"context"
	"errors"
	"net"
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
