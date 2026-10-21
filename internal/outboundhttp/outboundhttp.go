// Package outboundhttp holds the transport of every outbound HTTP client of acr: a connection pool that never dials
// through a proxy from the process environment (HTTP_PROXY, HTTPS_PROXY), so a credential on a request reaches only
// the host the request names. A client with a nil Transport uses http.DefaultTransport, which honours those variables;
// internal/cigates/outboundtransport fails a client of production code built without a transport.
package outboundhttp

import (
	"net/http"
	"sync"
)

// Transport is a new transport with http.DefaultTransport's settings and no proxy, for a client that changes a field
// of it (a dialer, a TLS config, a timeout).
func Transport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}

var shared = sync.OnceValue(Transport)

// SharedTransport is one process-wide Transport(): the connection pool every client that customises nothing shares,
// as such a client shared http.DefaultTransport.
func SharedTransport() *http.Transport { return shared() }
