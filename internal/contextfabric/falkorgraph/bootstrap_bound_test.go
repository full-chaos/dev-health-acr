package falkorgraph

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// silentOnCreateIndexServer is a RESP server that answers every command until
// the first GRAPH.QUERY carrying CREATE INDEX: from then on it reads but never
// answers, on every connection, and keeps the sockets open, as a FalkorDB
// that stopped answering after it served.
type silentOnCreateIndexServer struct {
	addr    string
	silence chan struct{}
	once    sync.Once

	mu       sync.Mutex
	inFlight string
	answered int
}

func newSilentOnCreateIndexServer(t *testing.T) *silentOnCreateIndexServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &silentOnCreateIndexServer{addr: ln.Addr().String(), silence: make(chan struct{})}
	stop := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		_ = ln.Close()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, stop)
		}
	}()
	return s
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(sizeLine, "$")))
		if err != nil {
			return nil, err
		}
		payload := make([]byte, size+2)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
		args = append(args, string(payload[:size]))
	}
	return args, nil
}

func (s *silentOnCreateIndexServer) serve(c net.Conn, stop <-chan struct{}) {
	defer c.Close()
	go func() {
		<-stop
		_ = c.Close()
	}()
	r := bufio.NewReader(c)
	for {
		args, err := readRESPCommand(r)
		if err != nil || len(args) == 0 {
			return
		}
		name := strings.ToUpper(args[0])
		if name == "GRAPH.QUERY" && len(args) > 2 && strings.Contains(args[2], "CREATE INDEX") {
			s.mu.Lock()
			s.inFlight = args[2]
			s.mu.Unlock()
			s.once.Do(func() { close(s.silence) })
		}
		select {
		case <-s.silence:
			continue
		default:
		}
		reply := "+OK\r\n"
		switch name {
		case "HELLO":
			reply = "-ERR unknown command 'HELLO'\r\n"
		case "PING":
			reply = "+PONG\r\n"
		}
		s.mu.Lock()
		s.answered++
		s.mu.Unlock()
		if _, err := c.Write([]byte(reply)); err != nil {
			return
		}
	}
}

// A FalkorDB that answers and then stops answering on the first CREATE INDEX
// of a fresh graph: the first write's bootstrap returns a typed unavailable
// error within the RequestTimeout (plus slack), even with a caller ctx that
// has no deadline, and the graph is not marked bootstrapped.
func TestAFirstWriteOnAServerThatGoesSilentOnCreateIndexFailsWithinTheRequestTimeout(t *testing.T) {
	const requestTimeout = time.Second
	const slack = 3 * time.Second
	server := newSilentOnCreateIndexServer(t)
	config := Config{
		Addr: server.addr, GraphPrefix: "acr-cf-silent", RequestTimeout: requestTimeout,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 2, AllowInsecure: true,
	}
	api, err := newSDKAPI(config)
	if err != nil {
		t.Fatal(err)
	}
	sdk := api.(*sdkAPI)
	t.Cleanup(func() {
		_ = sdk.db.Conn.Close()
		_ = sdk.read.Conn.Close()
	})
	adapter, err := newWithAPI(config, api)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.db.Conn.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("premise: the server answers PING before it goes silent: %v", err)
	}

	type outcome struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	began := time.Now()
	go func() {
		_, err := adapter.ApplyProjectionBatch(context.Background(), vectorProbeBatch("org-silent"))
		done <- outcome{err: err, elapsed: time.Since(began)}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(requestTimeout + slack):
		t.Fatalf("ApplyProjectionBatch still blocked %s after the server went silent on CREATE INDEX (RequestTimeout %s)", requestTimeout+slack, requestTimeout)
	}
	t.Logf("silent CREATE INDEX: returned after %s with %v", got.elapsed.Round(time.Millisecond), got.err)

	if got.err == nil {
		t.Fatal("ApplyProjectionBatch succeeded against a server that never answered CREATE INDEX")
	}
	if !errors.Is(got.err, contextfabric.ErrUnavailable) {
		t.Fatalf("error = %v, want contextfabric.ErrUnavailable", got.err)
	}
	if !strings.Contains(got.err.Error(), "bootstrap subject index") {
		t.Fatalf("error = %v, want it to name the bootstrap subject index step", got.err)
	}
	if got.elapsed < requestTimeout-100*time.Millisecond {
		t.Fatalf("returned after %s, before the RequestTimeout %s: the timeout did not end this call", got.elapsed, requestTimeout)
	}
	server.mu.Lock()
	inFlight, answered := server.inFlight, server.answered
	server.mu.Unlock()
	if !strings.Contains(inFlight, "CREATE INDEX FOR (n:"+labelSubject+")") {
		t.Fatalf("statement in flight = %q, want the Subject CREATE INDEX", inFlight)
	}
	if answered == 0 {
		t.Fatal("premise: the server answered nothing before it went silent")
	}
	key := writeKeyFor(t, adapter, "org-silent")
	if bootstrapCached(adapter, key) {
		t.Fatal("a bootstrap whose CREATE INDEX got no reply marked the graph as bootstrapped")
	}
}
