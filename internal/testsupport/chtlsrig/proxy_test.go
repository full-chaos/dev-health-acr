package chtlsrig

import (
	"io"
	"net"
	"testing"
	"time"
)

func echoBackend(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func roundTrip(t *testing.T, conn net.Conn, payload []byte) time.Duration {
	t.Helper()
	started := time.Now()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	return time.Since(started)
}

func TestLatencyProxyDelaysOneRoundTripAndCountsConnectionsAndHellos(t *testing.T) {
	const rtt = 60 * time.Millisecond
	proxy := NewLatencyProxy(t, echoBackend(t), rtt)

	// The proxy's connection delay starts at Accept, which can run before Dial
	// returns, so the clock must start before Dial to bound the opening delay.
	dialStarted := time.Now()
	first, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	roundTrip(t, first, []byte{tlsHandshakeRecord, 3, 3})
	opening := time.Since(dialStarted)
	if opening < 2*rtt {
		t.Fatalf("first round trip on a new connection took %s, want at least a connection round trip plus a data round trip (%s)", opening, 2*rtt)
	}
	// A warm round trip must never be faster than one round trip (a sleep never
	// returns early), and the proxy must add ONE round trip, not two: a double
	// delay would put every sample at 2*rtt or more. Scheduler noise on a
	// shared runner only ever adds time, so the upper bound is "some sample is
	// under 2*rtt", never "every sample is" (CHAOS-13016: single samples of
	// 135 ms and 184 ms against a 120 ms ceiling).
	const maxWarmSamples = 40
	sawOneRoundTrip := false
	for i := 0; i < maxWarmSamples && !sawOneRoundTrip; i++ {
		warm := roundTrip(t, first, []byte("statement"))
		if warm < rtt {
			t.Fatalf("warm round trip %d took %s, want at least %s: the proxy added less than one round trip", i, warm, rtt)
		}
		sawOneRoundTrip = warm < 2*rtt
	}
	if !sawOneRoundTrip {
		t.Fatalf("none of %d warm round trips took under %s: the proxy adds more than one round trip", maxWarmSamples, 2*rtt)
	}
	second, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	roundTrip(t, second, []byte("plain"))
	if got := proxy.Stats(); got.Accepted != 2 || got.ClientHellos != 1 {
		t.Fatalf("stats = %+v, want 2 accepted and 1 TLS client hello (only the connection that opened with a handshake record)", got)
	}
	proxy.ResetStats()
	if got := proxy.Stats(); got != (Stats{}) {
		t.Fatalf("stats after reset = %+v", got)
	}
}

func TestLatencyProxyAtZeroRTTAddsNoDelay(t *testing.T) {
	proxy := NewLatencyProxy(t, echoBackend(t), 0)
	conn, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if took := roundTrip(t, conn, []byte("x")); took > 40*time.Millisecond {
		t.Fatalf("round trip at zero latency took %s", took)
	}
}

func TestPercentileUsesTheNearestRank(t *testing.T) {
	samples := make([]time.Duration, 0, 20)
	for i := 20; i >= 1; i-- {
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	if got := Percentile(samples, 0.95); got != 19*time.Millisecond {
		t.Fatalf("p95 of 1..20 ms = %s, want 19ms", got)
	}
	if got := Percentile(samples, 0.5); got != 10*time.Millisecond {
		t.Fatalf("p50 of 1..20 ms = %s, want 10ms", got)
	}
}
