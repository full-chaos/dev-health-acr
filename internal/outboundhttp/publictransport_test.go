package outboundhttp

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// listenAt serves handler on host:0 and counts every TCP connection it accepts, so a refused dial is told from a
// request that merely failed: a refusal accepts 0 connections.
func listenAt(t *testing.T, host string, handler http.Handler) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	accepted := &atomic.Int64{}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = &countingListener{Listener: listener, accepted: accepted}
	server.Start()
	t.Cleanup(server.Close)
	return server, accepted
}

type countingListener struct {
	net.Listener
	accepted *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
}

func clientOver(transport *http.Transport) *http.Client {
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func TestPublicTransportNeverConnectsToALoopbackListener(t *testing.T) {
	server, accepted := listenAt(t, "127.0.0.1", okHandler())

	control := clientOver(Transport())
	response, err := control.Get(server.URL)
	if err != nil {
		t.Fatalf("the unrestricted transport must reach the listener (the guard's control): %v", err)
	}
	_ = response.Body.Close()
	if accepted.Load() == 0 {
		t.Fatal("the control request was never accepted: the counting listener cannot tell a refusal from silence")
	}

	before := accepted.Load()
	_, err = clientOver(PublicTransport()).Get(server.URL)
	if !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("PublicTransport to a loopback listener: err = %v, want ErrNonPublicAddress", err)
	}
	if got := accepted.Load() - before; got != 0 {
		t.Fatalf("PublicTransport connected to the loopback listener %d times, want 0", got)
	}
	_, err = clientOver(SharedPublicTransport()).Get(server.URL)
	if !errors.Is(err, ErrNonPublicAddress) || accepted.Load() != before {
		t.Fatalf("SharedPublicTransport: err = %v accepts = %d, want a refusal and no connection", err, accepted.Load()-before)
	}
}

func TestPublicTransportRefusesAHostnameThatResolvesToLoopback(t *testing.T) {
	server, accepted := listenAt(t, "127.0.0.1", okHandler())
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := net.LookupHost("localhost")
	if err != nil || len(addresses) == 0 {
		t.Skipf("localhost does not resolve here: %v", err)
	}
	_, err = clientOver(PublicTransport()).Get("http://localhost:" + port)
	if err == nil || !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("a name that resolves to loopback: err = %v, want ErrNonPublicAddress", err)
	}
	if accepted.Load() != 0 {
		t.Fatalf("the listener accepted %d connections, want 0", accepted.Load())
	}
}

// A redirect hop is dialled through the same transport: a first hop that is allowed and redirects to a non-public
// address is refused at the dial of the second hop, which accepts 0 connections. The test dials 127.0.0.1 and
// refuses 127.0.0.2 (both loopback, so the test needs no public host).
func TestPublicTransportRefusesARedirectHopToANonPublicAddress(t *testing.T) {
	target, targetAccepted := listenAt(t, "127.0.0.2", okHandler())
	first, firstAccepted := listenAt(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	onlyTheFirstHop := func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrNonPublicAddress
		}
		if addr, err := netip.ParseAddr(host); err != nil || addr != netip.MustParseAddr("127.0.0.1") {
			return ErrNonPublicAddress
		}
		return nil
	}
	_, err := clientOver(publicTransportWith(onlyTheFirstHop)).Get(first.URL)
	if !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("redirect to a non-public hop: err = %v, want ErrNonPublicAddress", err)
	}
	if firstAccepted.Load() == 0 {
		t.Fatal("the first hop was never reached: the test cannot tell a refused hop from a refused request")
	}
	if targetAccepted.Load() != 0 {
		t.Fatalf("the redirect target accepted %d connections, want 0", targetAccepted.Load())
	}
}

func TestARefusalNamesNoHostAddressOrURL(t *testing.T) {
	if text := ErrNonPublicAddress.Error(); strings.ContainsAny(text, "0123456789:/") || strings.Contains(text, "http") {
		t.Fatalf("the refusal text %q must name no host, address or url", text)
	}
}

func TestPublicTransportUsesNoEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	if PublicTransport().Proxy != nil || SharedPublicTransport().Proxy != nil {
		t.Fatal("a public-only transport must not honour proxy settings")
	}
}
