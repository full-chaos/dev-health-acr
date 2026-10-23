package chfixture

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type probeLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *probeLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestAwaitFalkorServingFailsLoudOnAPortThatAcceptsButNeverAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	var pl probeLog
	began := time.Now()
	err = awaitFalkorServing(context.Background(), ln.Addr().String(), 700*time.Millisecond, 150*time.Millisecond, 50*time.Millisecond, pl.logf)
	if err == nil {
		t.Fatal("an open port that never answers was reported as serving")
	}
	if !strings.Contains(err.Error(), "PING") {
		t.Fatalf("error does not carry the last probe error: %v", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("probe overran its budget: %s", took)
	}
	if len(pl.lines) < 2 {
		t.Fatalf("each failed try must be logged, got %d lines", len(pl.lines))
	}
}

func TestAwaitFalkorServingAcceptsAServerThatAnswersAfterADelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	start := time.Now()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeRESP(c, start.Add(400*time.Millisecond))
		}
	}()
	var pl probeLog
	if err := awaitFalkorServing(context.Background(), ln.Addr().String(), 5*time.Second, 150*time.Millisecond, 50*time.Millisecond, pl.logf); err != nil {
		t.Fatalf("a server that answers must be accepted: %v\n%s", err, strings.Join(pl.lines, "\n"))
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("returned before the server answered")
	}
}

func serveFakeRESP(c net.Conn, answerFrom time.Time) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if time.Now().Before(answerFrom) {
			continue
		}
		req := strings.ToUpper(string(buf[:n]))
		switch {
		case strings.Contains(req, "PING"):
			_, _ = c.Write([]byte("+PONG\r\n"))
		case strings.Contains(req, "GRAPH.QUERY"):
			_, _ = c.Write([]byte("*0\r\n"))
		case strings.Contains(req, "GRAPH.LIST"):
			_, _ = c.Write([]byte("*0\r\n"))
		case strings.Contains(req, "HELLO"), strings.Contains(req, "CLIENT"):
			_, _ = c.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}
