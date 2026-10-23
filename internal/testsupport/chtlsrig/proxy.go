package chtlsrig

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tlsHandshakeRecord = 0x16

// Stats counts what reached the proxy. Accepted is every TCP connection a
// client opened; ClientHellos is every connection whose first client byte was a
// TLS handshake record, so one warm pooled connection counts once however many
// statements it carries.
type Stats struct {
	Accepted     int64
	ClientHellos int64
}

// LatencyProxy forwards TCP to one backend and delays every chunk by half of
// the configured round trip in each direction. A new connection also waits one
// full round trip before the first byte moves, which stands in for the TCP
// handshake the loopback kernel completes instantly.
type LatencyProxy struct {
	listener net.Listener
	backend  string
	rtt      atomic.Int64
	accepted atomic.Int64
	hellos   atomic.Int64
	wg       sync.WaitGroup
}

// NewLatencyProxy starts a proxy on a loopback port.
func NewLatencyProxy(tb testing.TB, backend string, rtt time.Duration) *LatencyProxy {
	tb.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen latency proxy: %v", err)
	}
	proxy := &LatencyProxy{listener: listener, backend: backend}
	proxy.rtt.Store(int64(rtt))
	proxy.wg.Add(1)
	go proxy.acceptLoop()
	tb.Cleanup(proxy.Close)
	return proxy
}

func (p *LatencyProxy) Addr() string { return p.listener.Addr().String() }

func (p *LatencyProxy) SetRTT(rtt time.Duration) { p.rtt.Store(int64(rtt)) }

func (p *LatencyProxy) RTT() time.Duration { return time.Duration(p.rtt.Load()) }

func (p *LatencyProxy) Stats() Stats {
	return Stats{Accepted: p.accepted.Load(), ClientHellos: p.hellos.Load()}
}

func (p *LatencyProxy) ResetStats() {
	p.accepted.Store(0)
	p.hellos.Store(0)
}

func (p *LatencyProxy) Close() {
	_ = p.listener.Close()
	p.wg.Wait()
}

func (p *LatencyProxy) acceptLoop() {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.accepted.Add(1)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.serve(client)
		}()
	}
}

func (p *LatencyProxy) serve(client net.Conn) {
	defer func() { _ = client.Close() }()
	time.Sleep(p.RTT())
	backend, err := net.DialTimeout("tcp", p.backend, 10*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = backend.Close() }()
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		p.pump(backend, client, true)
		_ = backend.Close()
	}()
	go func() {
		defer pumps.Done()
		p.pump(client, backend, false)
		_ = client.Close()
	}()
	pumps.Wait()
}

type delayed struct {
	data []byte
	due  time.Time
}

// pump copies source to sink in order, holding each chunk until its own due
// time so pipelined chunks overlap their delays instead of queueing them.
func (p *LatencyProxy) pump(sink, source net.Conn, countHello bool) {
	queue := make(chan delayed, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range queue {
			time.Sleep(time.Until(chunk.due))
			if _, err := sink.Write(chunk.data); err != nil {
				for range queue {
				}
				return
			}
		}
	}()
	first := true
	buffer := make([]byte, 32*1024)
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			if first && countHello && buffer[0] == tlsHandshakeRecord {
				p.hellos.Add(1)
			}
			first = false
			queue <- delayed{data: append([]byte(nil), buffer[:n]...), due: time.Now().Add(p.RTT() / 2)}
		}
		if err != nil {
			if err != io.EOF {
				_ = source.Close()
			}
			break
		}
	}
	close(queue)
	<-done
}
