package falkorfixture_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
)

const stallFalkordbImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

// proxiedTarget is the real container with its mapped address swapped for a local proxy.
type proxiedTarget struct {
	testcontainers.Container
	host string
	port network.Port
}

func (p proxiedTarget) Host(context.Context) (string, error) { return p.host, nil }
func (p proxiedTarget) MappedPort(context.Context, string) (network.Port, error) {
	return p.port, nil
}

// stallingProxy forwards to upstream and, when the first GRAPH.QUERY passes through, first sends the server
// CLIENT PAUSE ALL on a side connection, so the server stops answering every client, inside the container too,
// right after it has answered PING: the state CI saw when a fresh server stopped answering after its first index
// command.
func stallingProxy(t *testing.T, upstream string) (string, *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var stalled atomic.Bool
	var once sync.Once
	var mu sync.Mutex
	var conns []net.Conn
	track := func(c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		conns = append(conns, c)
	}
	stall := func() {
		side, err := net.DialTimeout("tcp", upstream, 5*time.Second)
		if err != nil {
			t.Errorf("stall plant: dial: %v", err)
			return
		}
		track(side)
		_ = side.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(side, "*4\r\n$6\r\nCLIENT\r\n$5\r\nPAUSE\r\n$6\r\n600000\r\n$3\r\nALL\r\n"); err != nil {
			t.Errorf("stall plant: write: %v", err)
			return
		}
		reply, err := bufio.NewReader(side).ReadString('\n')
		if err != nil || !strings.HasPrefix(reply, "+OK") {
			t.Errorf("stall plant: CLIENT PAUSE reply %q: %v", reply, err)
			return
		}
		stalled.Store(true)
	}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.DialTimeout("tcp", upstream, 5*time.Second)
			if err != nil {
				_ = client.Close()
				continue
			}
			track(client)
			track(server)
			go func() { _, _ = io.Copy(client, server) }()
			go func() {
				buf := make([]byte, 32<<10)
				for {
					n, err := client.Read(buf)
					if n > 0 {
						if strings.Contains(strings.ToUpper(string(buf[:n])), "GRAPH.QUERY") {
							once.Do(stall)
						}
						if _, werr := server.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String(), &stalled
}

func TestFalkorReadyNamesARealServerThatStopsAnsweringAfterItServed(t *testing.T) {
	ctx := context.Background()
	c := falkorfixture.Start(ctx, t, stallFalkordbImage)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := c.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddr, stalled := stallingProxy(t, net.JoinHostPort(host, mapped.Port()))
	proxyHost, proxyPort, _ := net.SplitHostPort(proxyAddr)
	port, err := network.ParsePort(proxyPort + "/tcp")
	if err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	err = chfixture.FalkorReadyWithin(30*time.Second).WaitUntilReady(ctx, proxiedTarget{Container: c, host: proxyHost, port: port})
	took := time.Since(began)
	t.Logf("falkordb-stall: verdict after %s:\n%v", took.Round(time.Millisecond), err)

	if !stalled.Load() {
		t.Fatal("the plant never paused the server, so this run measured nothing")
	}
	if !errors.Is(err, chfixture.ErrServerWedged) {
		t.Fatalf("a server that answered PING and then stopped answering must be named as stalled, got: %v", err)
	}
	if took < 29*time.Second || took > time.Minute {
		t.Fatalf("a stalled server keeps the whole 30s budget and the diagnostics stay bounded: %s", took)
	}
	var wedged *chfixture.ServerWedgedError
	if !errors.As(err, &wedged) || !strings.HasPrefix(wedged.Graph, "acr_ready_probe_") || wedged.ServedTry < 1 {
		t.Fatalf("the error must carry the probe graph and the served try: %#v", wedged)
	}
	for _, want := range []string{
		"stopped answering after it served and was still silent when the 30s budget ended",
		`GRAPH.QUERY ` + wedged.Graph + ` "CREATE INDEX FOR (n:ReadyProbe) ON (n.k)"`,
		"container image: ", strings.TrimPrefix(stallFalkordbImage, "falkordb/"),
		"container state: status=running running=true paused=false",
		"/proc/1/status (exit 0):", "State:", "Threads:",
		"threads of pid 1 (tid state wchan comm) (exit 0):",
		"Ready to accept connections",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the stall failure must carry %q:\n%v", want, err)
		}
	}
}
