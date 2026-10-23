package modelprovider

import (
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAPublicEndpointsOnlyCallRefusesEveryNonPublicLiteralTarget(t *testing.T) {
	for name, host := range map[string]string{
		"metadata":             "169.254.169.254",
		"metadata mapped":      "[::ffff:169.254.169.254]",
		"link-local v6":        "[fe80::1]",
		"unique local v6":      "[fc00::1]",
		"unique local v6 fd":   "[fd12:3456::1]",
		"loopback v6":          "[::1]",
		"loopback mapped":      "[::ffff:127.0.0.1]",
		"loopback compatible":  "[::127.0.0.1]",
		"rfc1918 10":           "10.0.0.5",
		"rfc1918 172":          "172.16.0.5",
		"rfc1918 192":          "192.168.0.5",
		"shared address space": "100.64.0.1",
		"unspecified":          "0.0.0.0",
	} {
		t.Run(name, func(t *testing.T) {
			logs := &lockedBuffer{}
			cfg := byoConfig("https://"+host+":9/v1", true)
			cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
			started := time.Now()
			if err := callOnce(t, cfg); err == nil {
				t.Fatal("a public-only call to a non-public literal target succeeded")
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("the call took %s: a refusal at the dial is immediate, a slow failure is a connect attempt", elapsed)
			}
			if !strings.Contains(strings.Join(logs.lines(), "\n"), refusalReason) {
				t.Fatalf("the call failed for another reason than the dial-time refusal; log: %q", strings.Join(logs.lines(), "\n"))
			}
		})
	}
}

func TestAPublicEndpointsOnlyCallNeverConnectsToALoopbackV6Listener(t *testing.T) {
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()
	if err := callOnce(t, byoConfig("https://"+listener.Addr().String()+"/v1", false)); err == nil {
		t.Fatal("the control call must fail its TLS handshake against a listener that closes: it reached it")
	}
	if len(accepted) == 0 {
		t.Fatal("the control call never reached the IPv6 loopback listener: the counter cannot tell a refusal from silence")
	}
	for len(accepted) > 0 {
		<-accepted
	}
	if err := callOnce(t, byoConfig("https://"+listener.Addr().String()+"/v1", true)); err == nil {
		t.Fatal("a public-only call to an IPv6 loopback base_url succeeded")
	}
	if len(accepted) != 0 {
		t.Fatalf("a public-only client connected to the IPv6 loopback listener %d times, want 0", len(accepted))
	}
}
