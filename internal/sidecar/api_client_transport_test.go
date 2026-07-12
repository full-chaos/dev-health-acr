package sidecar

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientClassifiesConnectionFailureAsTransportUnavailable is the
// CHAOS-2908 rereview regression lock: a client-side network/TLS/connection
// failure (here, a closed listener producing "connection refused") must
// resolve to the typed, sanitized ErrTransportUnavailable sentinel rather
// than a bare wrapped net/http error with no recognizable sentinel at all
// -- the latter is what makes internal/mcp's classify() fall through to
// its generic "internal" category instead of "unavailable". The error text
// must also never contain the configured host or port: a fixed, safe
// message is all that may ever surface here, exactly like every other
// *APIError this package constructs.
func TestClientClassifiesConnectionFailureAsTransportUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cfg := newFixtureConfig(t, server)
	// Close the listener before any request is sent, so every call fails
	// with a genuine transport-level connection failure (not a context
	// cancellation/deadline, which must remain classified separately).
	server.Close()

	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	if callErr == nil {
		t.Fatal("expected a connection failure against a closed listener")
	}
	if !errors.Is(callErr, ErrTransportUnavailable) {
		t.Fatalf("expected errors.Is to match ErrTransportUnavailable, got %v", callErr)
	}
	var apiErr *APIError
	if !errors.As(callErr, &apiErr) {
		t.Fatalf("expected an *APIError, got %v (%T)", callErr, callErr)
	}
	if strings.Contains(callErr.Error(), cfg.APIBaseURL.Host) {
		t.Fatalf("transport failure error leaked the configured host: %v", callErr)
	}
}

// TestClientPreservesContextCancellationOverTransportUnavailable locks that
// a context cancellation surfacing through http.Client.Do is still
// classified as context.Canceled (internal/mcp's classify() maps this to
// "cancelled", a distinct category from "unavailable"), not collapsed into
// the new generic transport-unavailable sentinel.
func TestClientPreservesContextCancellationOverTransportUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, callErr := client.Capabilities(ctx)
	if !errors.Is(callErr, context.Canceled) {
		t.Fatalf("expected errors.Is to match context.Canceled, got %v", callErr)
	}
	if errors.Is(callErr, ErrTransportUnavailable) {
		t.Fatal("a context cancellation must not be classified as ErrTransportUnavailable")
	}
}

// TestClientBodyReadUnexpectedEOFIsClassifiedTransportUnavailable is the
// CHAOS-2908 rereview regression lock for a partial response body: a
// connection that hangs up after promising more bytes (via Content-Length)
// than it ever sends must resolve to the typed, sanitized
// ErrTransportUnavailable sentinel, not a bare wrapped read error with no
// recognizable sentinel -- exactly like the pre-response connection-failure
// case above, but for a failure discovered while reading the body of an
// already-received response.
func TestClientBodyReadUnexpectedEOFIsClassifiedTransportUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support hijacking")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// Promise more bytes than are ever sent, then close normally: the
		// client's body reader detects the shortfall as io.ErrUnexpectedEOF,
		// simulating a connection that dropped partway through the body.
		fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n")
		fmt.Fprint(buf, `{"schema_version":"capabilities.v1"`)
		_ = buf.Flush()
	}))
	defer server.Close()

	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	if callErr == nil {
		t.Fatal("expected a body-read failure for a truncated response")
	}
	if !errors.Is(callErr, ErrTransportUnavailable) {
		t.Fatalf("expected errors.Is to match ErrTransportUnavailable, got %v", callErr)
	}
	if strings.Contains(callErr.Error(), `"schema_version"`) {
		t.Fatalf("transport failure error leaked partial response body content: %v", callErr)
	}
}

// TestClientBodyReadConnectionResetIsClassifiedTransportUnavailable covers
// the same body-read classification for a hard TCP reset (as opposed to a
// graceful close with a mismatched Content-Length above): forcing SO_LINGER
// to 0 before closing makes the kernel send RST instead of FIN. Whichever
// exact OS-level error text results, it must still resolve to
// ErrTransportUnavailable, never a bare wrapped error.
func TestClientBodyReadConnectionResetIsClassifiedTransportUnavailable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support hijacking")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n")
		fmt.Fprint(buf, `{"partial`)
		_ = buf.Flush()
		underlying := conn
		if tlsConn, ok := conn.(*tls.Conn); ok {
			underlying = tlsConn.NetConn()
		}
		if tcpConn, ok := underlying.(*net.TCPConn); ok {
			_ = tcpConn.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer server.Close()

	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	if callErr == nil {
		t.Fatal("expected a body-read failure for a reset connection")
	}
	if !errors.Is(callErr, ErrTransportUnavailable) {
		t.Fatalf("expected errors.Is to match ErrTransportUnavailable, got %v", callErr)
	}
}

// TestClientBodyReadContextDeadlineStaysTimeout locks that a context
// deadline expiring while the body is still being read (not merely before
// the request was ever sent, which
// TestClientPreservesContextCancellationOverTransportUnavailable above
// already covers) remains classified as context.DeadlineExceeded, not
// collapsed into the new ErrTransportUnavailable body-read path.
func TestClientBodyReadContextDeadlineStaysTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer server.Close()
	defer close(release)

	cfg := newFixtureConfig(t, server)
	cfg.Timeout = minTimeout // the shortest value the config validator accepts
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	if !errors.Is(callErr, context.DeadlineExceeded) {
		t.Fatalf("expected errors.Is to match context.DeadlineExceeded, got %v", callErr)
	}
	if errors.Is(callErr, ErrTransportUnavailable) {
		t.Fatal("a body-read timeout must not be classified as ErrTransportUnavailable")
	}
}
