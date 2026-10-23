package outboundhttp

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"testing"
)

// The first hop is let through by address; every other address, the redirect target included, goes to the
// production hook itself.
func TestProductionHookRefusesARedirectFromAnAllowedHostToEachNonPublicTarget(t *testing.T) {
	for name, tc := range map[string]struct {
		listenHost string
		location   string
	}{
		"loopback v4": {"127.0.0.2", "http://127.0.0.2:%s/"},
		"loopback v6": {"[::1]", "http://[::1]:%s/"},
		"mapped v4":   {"127.0.0.2", "http://[::ffff:127.0.0.2]:%s/"},
		"metadata":    {"", "http://169.254.169.254:%s/"},
	} {
		t.Run(name, func(t *testing.T) {
			var targetAccepted func() int64 = func() int64 { return 0 }
			targetPort := "9"
			if tc.listenHost != "" {
				target, accepted := listenAt(t, tc.listenHost, okHandler())
				_, port, err := net.SplitHostPort(target.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				targetPort = port
				targetAccepted = accepted.Load
			}
			first, firstAccepted := listenAt(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, sprintfLocation(tc.location, targetPort), http.StatusFound)
			}))
			firstHopPort := portOf(t, first.Listener.Addr().String())
			allowFirstHop := func(network, address string, conn syscall.RawConn) error {
				host, port, err := net.SplitHostPort(address)
				if err == nil && port == firstHopPort && netip.MustParseAddr(host) == netip.MustParseAddr("127.0.0.1") {
					return nil
				}
				return RefuseNonPublicAddress(network, address, conn)
			}
			_, err := clientOver(publicTransportWith(allowFirstHop)).Get(first.URL)
			if !errors.Is(err, ErrNonPublicAddress) {
				t.Fatalf("redirect to a non-public target: err = %v, want ErrNonPublicAddress", err)
			}
			if firstAccepted.Load() == 0 {
				t.Fatal("the first hop was never reached: the test cannot tell a refused hop from a refused request")
			}
			if targetAccepted() != 0 {
				t.Fatalf("the redirect target accepted %d connections, want 0", targetAccepted())
			}
		})
	}
}

func sprintfLocation(format, port string) string {
	return strings.Replace(format, "%s", port, 1)
}

func portOf(t *testing.T, hostPort string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
