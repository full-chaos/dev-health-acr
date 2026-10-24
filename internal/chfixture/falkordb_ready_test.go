package chfixture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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
	var deleted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeRESP(c, start.Add(400*time.Millisecond), start.Add(700*time.Millisecond), &deleted)
		}
	}()
	var pl probeLog
	if err := awaitFalkorServing(context.Background(), ln.Addr().String(), 5*time.Second, 150*time.Millisecond, 50*time.Millisecond, pl.logf); err != nil {
		t.Fatalf("a server that answers must be accepted: %v\n%s", err, strings.Join(pl.lines, "\n"))
	}
	if time.Since(start) < 700*time.Millisecond {
		t.Fatal("returned before the index command was served")
	}
	if got := deleted.Load(); got != 1 {
		t.Fatalf("the throwaway graph must be deleted once on success, got %d", got)
	}
}

func TestAwaitFalkorServingFailsLoudWhenTheIndexCommandNeverSucceeds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var deleted atomic.Int32
	far := time.Now().Add(time.Hour)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeRESP(c, time.Time{}, far, &deleted)
		}
	}()
	var pl probeLog
	err = awaitFalkorServing(context.Background(), ln.Addr().String(), 700*time.Millisecond, 150*time.Millisecond, 50*time.Millisecond, pl.logf)
	if err == nil {
		t.Fatal("a server that answers PING and GRAPH.LIST but cannot create an index was reported as serving")
	}
	if !strings.Contains(err.Error(), "CREATE INDEX") {
		t.Fatalf("error does not name the index step: %v", err)
	}
	if got := deleted.Load(); got != 1 {
		t.Fatalf("the throwaway graph must be deleted once on the timeout path, got %d", got)
	}
}

func TestAwaitFalkorServingTreatsARepeatedIndexCreateAsServing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var deleted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeRESPIndexError(c, "-ERR Attribute 'k' is already indexed\r\n", &deleted)
		}
	}()
	var pl probeLog
	if err := awaitFalkorServing(context.Background(), ln.Addr().String(), 700*time.Millisecond, 150*time.Millisecond, 50*time.Millisecond, pl.logf); err != nil {
		t.Fatalf("an already-indexed reply means an earlier try executed and must count as serving: %v", err)
	}
	if got := deleted.Load(); got != 1 {
		t.Fatalf("the throwaway graph must be deleted once, got %d", got)
	}
}

func serveFakeRESPIndexError(c net.Conn, indexReply string, deleted *atomic.Int32) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		req := strings.ToUpper(string(buf[:n]))
		switch {
		case strings.Contains(req, "PING"):
			_, _ = c.Write([]byte("+PONG\r\n"))
		case strings.Contains(req, "GRAPH.LIST"):
			_, _ = c.Write([]byte("*0\r\n"))
		case strings.Contains(req, "GRAPH.QUERY"):
			_, _ = c.Write([]byte(indexReply))
		case strings.Contains(req, "GRAPH.DELETE"):
			deleted.Add(1)
			_, _ = c.Write([]byte("+Graph removed\r\n"))
		case strings.Contains(req, "HELLO"), strings.Contains(req, "CLIENT"):
			_, _ = c.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}

func TestAwaitFalkorServingUsesAFreshGraphNameForEveryStart(t *testing.T) {
	a, errA := probeGraphName()
	b, errB := probeGraphName()
	if errA != nil || errB != nil {
		t.Fatalf("names: %v %v", errA, errB)
	}
	if a == b || !strings.HasPrefix(a, "acr_ready_probe_") {
		t.Fatalf("probe graph names must be unique per start: %q %q", a, b)
	}
}

func serveFakeRESP(c net.Conn, answerFrom, indexFrom time.Time, deleted *atomic.Int32) {
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
		case strings.Contains(req, "GRAPH.LIST"):
			_, _ = c.Write([]byte("*0\r\n"))
		case strings.Contains(req, "GRAPH.QUERY"):
			if time.Now().Before(indexFrom) {
				_, _ = c.Write([]byte("-ERR Index not ready\r\n"))
			} else {
				_, _ = c.Write([]byte("*1\r\n$18\r\nIndices created: 1\r\n"))
			}
		case strings.Contains(req, "GRAPH.DELETE"):
			deleted.Add(1)
			_, _ = c.Write([]byte("+Graph removed\r\n"))
		case strings.Contains(req, "HELLO"), strings.Contains(req, "CLIENT"):
			_, _ = c.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}

func silentListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
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
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

func TestAwaitFalkorServingNamesAnUnreachablePublishedPortWhenTheServerAnswersInside(t *testing.T) {
	addr := silentListener(t)
	var pl probeLog
	began := time.Now()
	err := awaitFalkorServingInside(context.Background(), addr, 20*time.Second, 100*time.Millisecond, 20*time.Millisecond, 300*time.Millisecond, func(context.Context) error { return nil }, pl.logf)
	if !errors.Is(err, ErrPublishedPortUnreachable) {
		t.Fatalf("a server that answers inside while the published port never does must be named as such: %v", err)
	}
	if !strings.Contains(err.Error(), "PING") || !strings.Contains(err.Error(), "inside the container: answers PING") {
		t.Fatalf("the error must carry the PING error and the inside result: %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the verdict must come early, not after the whole budget: %s", took)
	}
}

func TestAwaitFalkorServingStaysAHardFailWhenTheServerIsNotUpInside(t *testing.T) {
	addr := silentListener(t)
	var pl probeLog
	insideErr := errors.New("redis-cli ping inside the container: exit 1")
	err := awaitFalkorServingInside(context.Background(), addr, 700*time.Millisecond, 100*time.Millisecond, 20*time.Millisecond, 200*time.Millisecond, func(context.Context) error { return insideErr }, pl.logf)
	if err == nil || errors.Is(err, ErrPublishedPortUnreachable) {
		t.Fatalf("a server that is not up inside must not be reported as a dead published port: %v", err)
	}
	if !strings.Contains(err.Error(), "PING") || !strings.Contains(err.Error(), insideErr.Error()) {
		t.Fatalf("both the PING error and the inside error must be reported: %v", err)
	}
}

func TestAwaitFalkorServingAcceptsALateServerEvenWhenItAnswersInsideFirst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var deleted atomic.Int32
	start := time.Now()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeRESP(c, start.Add(400*time.Millisecond), start, &deleted)
		}
	}()
	var pl probeLog
	if err := awaitFalkorServingInside(context.Background(), ln.Addr().String(), 5*time.Second, 100*time.Millisecond, 20*time.Millisecond, 3*time.Second, func(context.Context) error { return nil }, pl.logf); err != nil {
		t.Fatalf("a late-answering server must be accepted, not reported unreachable: %v", err)
	}
}

func TestAwaitFalkorServingKeepsOnlyTheEdgesOfALongTryHistory(t *testing.T) {
	var h []string
	for i := 1; i <= 30; i++ {
		h = append(h, fmt.Sprintf("try %d", i))
	}
	got := summarizeTries(h)
	if !strings.Contains(got, "try 1\n") || !strings.Contains(got, "try 30") || strings.Contains(got, "try 15") || !strings.Contains(got, "20 tries omitted") {
		t.Fatalf("history must keep the first and last 5 tries: %q", got)
	}
}

func TestAwaitFalkorServingNeverCallsAGraphCommandFailureAnUnreachablePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					req := strings.ToUpper(string(buf[:n]))
					switch {
					case strings.Contains(req, "PING"):
						_, _ = c.Write([]byte("+PONG\r\n"))
					case strings.Contains(req, "GRAPH.LIST"):
						_, _ = c.Write([]byte("-ERR graph module loading\r\n"))
					case strings.Contains(req, "HELLO"), strings.Contains(req, "CLIENT"):
						_, _ = c.Write([]byte("-ERR unknown command\r\n"))
					}
				}
			}()
		}
	}()
	var pl probeLog
	err = awaitFalkorServingInside(context.Background(), ln.Addr().String(), 1500*time.Millisecond, 100*time.Millisecond, 20*time.Millisecond, 300*time.Millisecond, func(context.Context) error { return nil }, pl.logf)
	if err == nil || errors.Is(err, ErrPublishedPortUnreachable) {
		t.Fatalf("a host that answers PING and fails GRAPH.LIST must stay a hard fail, not a dead published port: %v", err)
	}
	if !strings.Contains(err.Error(), "GRAPH.LIST") {
		t.Fatalf("the hard fail must carry the GRAPH.LIST error: %v", err)
	}
}
