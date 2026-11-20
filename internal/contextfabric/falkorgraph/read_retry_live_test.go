package falkorgraph

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// droppingProxy forwards every connection to upstream and closes both sides,
// before the command reaches the server, on a command the dropper takes.
func droppingProxy(t *testing.T, upstream string, d *connDropper) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func(client net.Conn) {
				server, err := net.Dial("tcp", upstream)
				if err != nil {
					_ = client.Close()
					return
				}
				defer func() { _ = client.Close(); _ = server.Close() }()
				go func() { _, _ = io.Copy(client, server); _ = client.Close() }()
				r := bufio.NewReader(client)
				for {
					raw, name, err := readRawRESPCommand(r)
					if err != nil || d.take(name) {
						return
					}
					if _, err := server.Write(raw); err != nil {
						return
					}
				}
			}(client)
		}
	}()
	return ln.Addr().String()
}

// TestLiveAServedGraphReadRetriesADroppedConnectionUpToMaxAttempts runs a
// served read (ProjectionWatermark, one GRAPH.RO_QUERY) on a real FalkorDB
// behind a proxy that drops the connection before the read reaches the
// server. The write control (ApplyProjectionBatch) is sent once.
func TestLiveAServedGraphReadRetriesADroppedConnectionUpToMaxAttempts(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx := context.Background()
	container := falkorfixture.Start(ctx, t, codexRoundFalkordbImage)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	d := newConnDropper()
	proxy := droppingProxy(t, host+":"+port.Port(), d)
	const source = "read-retry-source"
	for _, maxAttempts := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("max_attempts=%d", maxAttempts), func(t *testing.T) {
			adapter, err := New(Config{
				Addr: proxy, GraphPrefix: "acr-cf-read-retry", RequestTimeout: 15 * time.Second,
				MaxAttempts: uint(maxAttempts), MaxResults: 25, PoolSize: 10, AllowInsecure: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			org := fmt.Sprintf("live-read-retry-%d-%s", maxAttempts, time.Now().UTC().Format("20060102T150405.000000000"))
			t.Cleanup(func() {
				d.arm("GRAPH.QUERY", 0)
				d.arm("GRAPH.RO_QUERY", 0)
				_ = adapter.PurgeOrganization(context.Background(), org)
			})
			if _, err := adapter.ApplyProjectionBatch(ctx, chaos4298LiveBatch(org, "batch_read_retry_00000001", source, "c1", "c2")); err != nil {
				t.Fatalf("setup batch: %v", err)
			}
			read := func(drops int) (int, error) {
				d.arm("GRAPH.RO_QUERY", drops)
				_, err := adapter.ProjectionWatermark(ctx, org, source)
				return len(d.arrivals("GRAPH.RO_QUERY")), err
			}

			sent, err := read(-1)
			t.Logf("persistent drop: sent=%d err=%v", sent, err)
			if sent != maxAttempts || !errors.Is(err, contextfabric.ErrUnavailable) {
				t.Fatalf("persistent drop: sent=%d err=%v, want %d sends and ErrUnavailable", sent, err, maxAttempts)
			}
			sent, err = read(maxAttempts - 1)
			t.Logf("%d drops: sent=%d err=%v", maxAttempts-1, sent, err)
			if sent != maxAttempts || err != nil {
				t.Fatalf("%d drops: sent=%d err=%v, want %d sends and the watermark", maxAttempts-1, sent, err, maxAttempts)
			}
			sent, err = read(maxAttempts)
			t.Logf("%d drops: sent=%d err=%v", maxAttempts, sent, err)
			if sent != maxAttempts || !errors.Is(err, contextfabric.ErrUnavailable) {
				t.Fatalf("%d drops: sent=%d err=%v, want %d sends and ErrUnavailable", maxAttempts, sent, err, maxAttempts)
			}
			if maxAttempts > 1 {
				sent, err = read(1)
				t.Logf("one drop: sent=%d err=%v", sent, err)
				if sent != 2 || err != nil {
					t.Fatalf("one drop: sent=%d err=%v, want 2 sends and the watermark", sent, err)
				}
			}

			d.arm("GRAPH.RO_QUERY", 0)
			d.arm("GRAPH.QUERY", 1)
			_, err = adapter.ApplyProjectionBatch(ctx, chaos4298LiveBatch(org, "batch_read_retry_00000002", source, "c2", "c3"))
			writes := len(d.arrivals("GRAPH.QUERY"))
			t.Logf("write control, one drop: sent=%d err=%v", writes, err)
			if writes != 1 || !errors.Is(err, contextfabric.ErrUnavailable) {
				t.Fatalf("write control, one drop: sent=%d err=%v, want 1 send and ErrUnavailable", writes, err)
			}
		})
	}
}
