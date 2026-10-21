// Package outboundhttp holds the transport of every outbound HTTP client of acr: a connection pool that never dials
// through a proxy from the process environment (HTTP_PROXY, HTTPS_PROXY), so a credential on a request reaches only
// the host the request names. A client with a nil Transport uses http.DefaultTransport, which honours those variables;
// internal/cigates/outboundtransport fails a client of production code built without a transport.
package outboundhttp

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Transport is a new transport with http.DefaultTransport's settings and no proxy, for a client that changes a field
// of it (a dialer, a TLS config, a timeout).
func Transport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport := base.Clone()
		transport.Proxy = nil
		return transport
	}
	// A dependency replaced http.DefaultTransport with another RoundTripper: net/http's own default settings, no proxy.
	slog.Warn("outboundhttp: http.DefaultTransport was replaced by another RoundTripper; the outbound transport uses net/http's default settings with no proxy",
		"default_transport_type", fmt.Sprintf("%T", http.DefaultTransport))
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

var shared = sync.OnceValue(Transport)

// SharedTransport is one process-wide Transport(): the connection pool every client that customises nothing shares,
// as such a client shared http.DefaultTransport.
func SharedTransport() *http.Transport { return shared() }

// WithTransport is client unchanged when it has a Transport, else a copy with SharedTransport(): a supplied client
// with a nil Transport would dial through http.DefaultTransport and so through an environment proxy. A nil client
// stays nil; the caller's client is never modified.
func WithTransport(client *http.Client) *http.Client {
	if client == nil || client.Transport != nil {
		return client
	}
	copied := *client
	copied.Transport = SharedTransport()
	return &copied
}
