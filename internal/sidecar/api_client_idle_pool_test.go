package sidecar

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

// CHAOS-12206: the hosted API client keeps its idle connections to its one
// origin for reuse, and closes an idle connection before acr-api does.

// The client's idle timeout is below acr-api's server idle timeout, both the
// server default and the value the chart deploys, and the transport uses it.
// If the client kept a connection the server had closed, the next request on
// it would fail, and net/http retries only replayable requests: a POST would
// fail.
func TestTheClientClosesAnIdleConnectionBeforeTheHostedAPIDoes(t *testing.T) {
	t.Parallel()
	if hostedAPIServerIdleTimeout != config.DefaultIdleTimeout {
		t.Fatalf("hostedAPIServerIdleTimeout = %s, but acr-api's default server idle timeout is %s: the stated pair is stale", hostedAPIServerIdleTimeout, config.DefaultIdleTimeout)
	}
	values, err := os.ReadFile("../../deploy/helm/acr/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The top-level config block's idleTimeout feeds ACR_IDLE_TIMEOUT
	// (templates/configmap.yaml); acrMcp has its own, for its own server.
	block := regexp.MustCompile(`(?ms)^config:\n(.*?)^\S`).FindSubmatch(values)
	if block == nil {
		t.Fatal("deploy/helm/acr/values.yaml has no top-level config block")
	}
	match := regexp.MustCompile(`(?m)^  idleTimeout:\s*"([^"]+)"`).FindSubmatch(block[1])
	if match == nil {
		t.Fatal("deploy/helm/acr/values.yaml config has no idleTimeout")
	}
	deployed, err := time.ParseDuration(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	for name, server := range map[string]time.Duration{"server default": config.DefaultIdleTimeout, "chart config.idleTimeout": deployed} {
		if clientIdleConnTimeout >= server {
			t.Errorf("client idle timeout %s is not below the %s %s", clientIdleConnTimeout, name, server)
		}
	}
	transport, err := buildTransport(Config{APIBaseURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if transport.IdleConnTimeout != clientIdleConnTimeout {
		t.Errorf("transport IdleConnTimeout = %s, want %s", transport.IdleConnTimeout, clientIdleConnTimeout)
	}
	if transport.MaxIdleConnsPerHost != clientMaxIdleConns || transport.MaxIdleConns != clientMaxIdleConns {
		t.Errorf("transport idle pool = %d per host, %d total, want %d for its one origin", transport.MaxIdleConnsPerHost, transport.MaxIdleConns, clientMaxIdleConns)
	}
}

// Three waves of concurrent requests from one client to one plain-HTTP origin
// (acr-api in a cluster serves HTTP/1.1) open the first wave's connections
// and no more: each later request reuses an idle one. Before, the transport
// kept 2 idle connections per host, so every later wave dialed again.
func TestTheHostedClientReusesItsConnectionsAcrossWaves(t *testing.T) {
	t.Parallel()
	const wave = 40
	var opened atomic.Int64
	var arrived sync.WaitGroup
	release := make(chan struct{})
	var mu sync.Mutex
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived.Done()
		mu.Lock()
		gate := release
		mu.Unlock()
		<-gate
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := buildTransport(Config{APIBaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	for round := range 3 {
		// Every request of the wave is in flight at once before any is
		// answered, so the first wave needs exactly `wave` connections.
		arrived.Add(wave)
		mu.Lock()
		release = make(chan struct{})
		mu.Unlock()
		var done sync.WaitGroup
		for range wave {
			done.Add(1)
			go func() {
				defer done.Done()
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Errorf("round %d: %v", round, err)
					return
				}
				_ = resp.Body.Close()
			}()
		}
		arrived.Wait()
		mu.Lock()
		close(release)
		mu.Unlock()
		done.Wait()
	}
	if got := opened.Load(); got != wave {
		t.Fatalf("3 waves of %d concurrent requests opened %d connections, want %d: the idle pool did not keep the first wave's connections", wave, got, wave)
	}
}
