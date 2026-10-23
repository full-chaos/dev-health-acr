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

	first, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	opening := roundTrip(t, first, []byte{tlsHandshakeRecord, 3, 3})
	if opening < 2*rtt {
		t.Fatalf("first round trip on a new connection took %s, want at least a connection round trip plus a data round trip (%s)", opening, 2*rtt)
	}
	for i := 0; i < 5; i++ {
		warm := roundTrip(t, first, []byte("statement"))
		if warm < rtt || warm > 2*rtt {
			t.Fatalf("warm round trip %d took %s, want about %s", i, warm, rtt)
		}
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
