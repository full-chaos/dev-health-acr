package falkorgraph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/redis/go-redis/v9"
)

type readAttempt struct {
	operation   string
	attempt     int
	maxAttempts int
	cause       string
	retried     bool
}

// readAttemptRecorder is a GraphTelemetry that keeps every read attempt record.
type readAttemptRecorder struct {
	NoopTelemetry
	mu       sync.Mutex
	records  []readAttempt
	onRecord func(readAttempt)
}

func (r *readAttemptRecorder) RecordGraphReadAttemptFailed(_ context.Context, operation string, attempt, maxAttempts int, cause string, retried bool) {
	record := readAttempt{operation: operation, attempt: attempt, maxAttempts: maxAttempts, cause: cause, retried: retried}
	r.mu.Lock()
	r.records = append(r.records, record)
	hook := r.onRecord
	r.mu.Unlock()
	if hook != nil {
		hook(record)
	}
}

func (r *readAttemptRecorder) all() []readAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]readAttempt(nil), r.records...)
}

// readRawRESPCommand reads one RESP array command and returns its raw bytes and
// upper-cased name.
func readRawRESPCommand(r *bufio.Reader) ([]byte, string, error) {
	var raw bytes.Buffer
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, "", err
	}
	raw.WriteString(header)
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, "", err
	}
	name := ""
	for i := 0; i < count; i++ {
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			return nil, "", err
		}
		raw.WriteString(sizeLine)
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(sizeLine, "$")))
		if err != nil {
			return nil, "", err
		}
		payload := make([]byte, size+2)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, "", err
		}
		raw.Write(payload)
		if i == 0 {
			name = strings.ToUpper(string(payload[:size]))
		}
	}
	return raw.Bytes(), name, nil
}

// connDropper decides, per command name, whether to close the connection
// instead of serving the command, and records when each command arrived.
type connDropper struct {
	mu    sync.Mutex
	drops map[string]int // -1 drops every one
	sent  map[string][]time.Time
}

func newConnDropper() *connDropper {
	return &connDropper{drops: map[string]int{}, sent: map[string][]time.Time{}}
}

// arm sets how many of the next commands named name are dropped and clears
// the arrival records.
func (d *connDropper) arm(name string, drops int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drops[name] = drops
	d.sent = map[string][]time.Time{}
}

func (d *connDropper) take(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent[name] = append(d.sent[name], time.Now())
	switch n := d.drops[name]; {
	case n < 0:
		return true
	case n > 0:
		d.drops[name] = n - 1
		return true
	}
	return false
}

func (d *connDropper) arrivals(name string) []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Time(nil), d.sent[name]...)
}

// droppingRESPServer answers GRAPH.LIST with an empty list and every other
// command with +OK, and closes the connection without a reply on a command the
// dropper takes.
func droppingRESPServer(t *testing.T, d *connDropper) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					_, name, err := readRawRESPCommand(r)
					if err != nil || d.take(name) {
						return
					}
					reply := "+OK\r\n"
					switch name {
					case "HELLO":
						reply = "-ERR unknown command 'HELLO'\r\n"
					case "GRAPH.LIST":
						reply = "*0\r\n"
					}
					if _, err := io.WriteString(c, reply); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func retrySDKAPI(t *testing.T, addr string, maxAttempts uint, telemetry GraphTelemetry) *sdkAPI {
	t.Helper()
	api, err := newSDKAPI(Config{
		Addr: addr, GraphPrefix: "acr-cf-retry", RequestTimeout: 3 * time.Second,
		MaxAttempts: maxAttempts, MaxResults: 25, PoolSize: 2, AllowInsecure: true, Telemetry: telemetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	return api.(*sdkAPI)
}

func closedTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestAGraphReadIsSentAtMostMaxAttemptsTimesOnADroppedConnection drives the
// real read client over a socket that the server closes without a reply.
func TestAGraphReadIsSentAtMostMaxAttemptsTimesOnADroppedConnection(t *testing.T) {
	ctx := context.Background()
	for _, maxAttempts := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("max_attempts=%d", maxAttempts), func(t *testing.T) {
			d := newConnDropper()
			sink := &readAttemptRecorder{}
			s := retrySDKAPI(t, droppingRESPServer(t, d), uint(maxAttempts), sink)

			d.arm("GRAPH.RO_QUERY", -1)
			_, err := s.query(ctx, "g", "MATCH (n) RETURN n", nil, true)
			if !errors.Is(err, contextfabric.ErrUnavailable) {
				t.Fatalf("persistent drop: error = %v, want ErrUnavailable", err)
			}
			if got := len(d.arrivals("GRAPH.RO_QUERY")); got != maxAttempts {
				t.Fatalf("persistent drop: GRAPH.RO_QUERY sent %d times, want %d", got, maxAttempts)
			}
			records := sink.all()
			if len(records) != maxAttempts {
				t.Fatalf("persistent drop: %d attempt records, want %d: %+v", len(records), maxAttempts, records)
			}
			for i, record := range records {
				want := readAttempt{operation: "query context graph", attempt: i + 1, maxAttempts: maxAttempts, cause: ReadFailureEOF, retried: i+1 < maxAttempts}
				if record != want {
					t.Fatalf("persistent drop: record %d = %+v, want %+v", i, record, want)
				}
			}

			d.arm("GRAPH.LIST", maxAttempts-1)
			if _, err := s.listGraphs(ctx); err != nil {
				t.Fatalf("%d drops: error = %v, want success on attempt %d", maxAttempts-1, err, maxAttempts)
			}
			if got := len(d.arrivals("GRAPH.LIST")); got != maxAttempts {
				t.Fatalf("%d drops: GRAPH.LIST sent %d times, want %d", maxAttempts-1, got, maxAttempts)
			}

			d.arm("GRAPH.LIST", maxAttempts)
			if _, err := s.listGraphs(ctx); !errors.Is(err, contextfabric.ErrUnavailable) {
				t.Fatalf("%d drops: error = %v, want ErrUnavailable", maxAttempts, err)
			}
		})
	}
}

// TestAGraphWriteIsSentOnceOnADroppedConnection: a write may commit before its
// connection drops, so it is never resent and reports no read attempt.
func TestAGraphWriteIsSentOnceOnADroppedConnection(t *testing.T) {
	d := newConnDropper()
	sink := &readAttemptRecorder{}
	s := retrySDKAPI(t, droppingRESPServer(t, d), 5, sink)
	d.arm("GRAPH.QUERY", -1)
	_, err := s.query(context.Background(), "g", "CREATE (:Probe)", nil, false)
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if got := len(d.arrivals("GRAPH.QUERY")); got != 1 {
		t.Fatalf("GRAPH.QUERY sent %d times, want 1", got)
	}
	if got := sink.all(); len(got) != 0 {
		t.Fatalf("a write produced read attempt records %+v", got)
	}
}

// TestTheReadBackoffIsCutByTheCallersDeadline: with a 300ms deadline the waits
// 50ms and 100ms fit and the 200ms wait does not, so the read returns the
// dependency error before the deadline and sends nothing after it.
func TestTheReadBackoffIsCutByTheCallersDeadline(t *testing.T) {
	d := newConnDropper()
	sink := &readAttemptRecorder{}
	s := retrySDKAPI(t, droppingRESPServer(t, d), 5, sink)
	d.arm("GRAPH.RO_QUERY", -1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_, err := s.query(ctx, "g", "MATCH (n) RETURN n", nil, true)
	returned := time.Now()
	sent := d.arrivals("GRAPH.RO_QUERY")
	t.Logf("sends=%d returned_before_deadline=%s err=%v records=%+v", len(sent), deadline.Sub(returned).Round(time.Millisecond), err, sink.all())
	if errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("error = %v, want the dependency error returned before the deadline, not the deadline", err)
	}
	if !returned.Before(deadline) {
		t.Fatalf("returned %s after the deadline", returned.Sub(deadline))
	}
	if len(sent) < 2 || len(sent) >= 5 {
		t.Fatalf("sent %d times, want at least one retry and fewer than MaxAttempts 5", len(sent))
	}
	for i, at := range sent {
		if !at.Before(deadline) {
			t.Fatalf("send %d arrived %s after the deadline", i+1, at.Sub(deadline))
		}
	}
	records := sink.all()
	if last := records[len(records)-1]; last.retried {
		t.Fatalf("last record %+v says retried, but no attempt followed", last)
	}
}

// TestACancelDuringTheReadBackoffEndsTheRead cancels the context while the
// first backoff runs: the read ends with the cancel and calls nothing more.
func TestACancelDuringTheReadBackoffEndsTheRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &readAttemptRecorder{onRecord: func(r readAttempt) {
		if r.retried {
			cancel()
		}
	}}
	s := &sdkAPI{config: Config{MaxAttempts: 5, Telemetry: sink}}
	calls := 0
	_, err := retryRead(ctx, s, "list graphs", func() (int, error) { calls++; return 0, io.EOF })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("read called %d times, want 1: no attempt after the cancel", calls)
	}
}

// TestTheReadBackoffDoublesFrom50ms pins the waits and that retryRead sleeps
// them: three attempts on a dropped connection take at least 50ms+100ms.
func TestTheReadBackoffDoublesFrom50ms(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 50 * time.Millisecond, 2: 100 * time.Millisecond, 3: 200 * time.Millisecond, 4: 400 * time.Millisecond} {
		if got := readRetryBackoff(attempt); got != want {
			t.Errorf("readRetryBackoff(%d) = %s, want %s", attempt, got, want)
		}
	}
	s := &sdkAPI{config: Config{MaxAttempts: 3}}
	calls := 0
	began := time.Now()
	_, err := retryRead(context.Background(), s, "list graphs", func() (int, error) { calls++; return 0, io.EOF })
	spent := time.Since(began)
	if calls != 3 || !errors.Is(err, io.EOF) {
		t.Fatalf("calls=%d err=%v, want 3 calls and the last error", calls, err)
	}
	if spent < 150*time.Millisecond {
		t.Fatalf("three attempts took %s, want at least 150ms of backoff", spent)
	}
}

// TestAReadIsNeverRetriedOnAContextError: a context error with the caller's own
// context still live (a lower layer's deadline) is not transient.
func TestAReadIsNeverRetriedOnAContextError(t *testing.T) {
	for _, contextErr := range []error{context.DeadlineExceeded, context.Canceled} {
		sink := &readAttemptRecorder{}
		s := &sdkAPI{config: Config{MaxAttempts: 5, Telemetry: sink}}
		calls := 0
		wrapped := fmt.Errorf("pool: %w", contextErr)
		_, err := retryRead(context.Background(), s, "list graphs", func() (int, error) { calls++; return 0, wrapped })
		if calls != 1 || !errors.Is(err, contextErr) {
			t.Fatalf("%v: calls=%d err=%v, want 1 call and the same error", contextErr, calls, err)
		}
		if got := sink.all(); len(got) != 0 {
			t.Fatalf("%v: attempt records %+v, want none", contextErr, got)
		}
	}
}

// TestARefusedReadDialIsRetriedWithBackoff dials a closed port: three attempts,
// two waits of 50ms and 100ms. Each attempt also runs the driver's own dial
// retry (5 dials 100ms apart, no command sent), so the upper bound is loose.
func TestARefusedReadDialIsRetriedWithBackoff(t *testing.T) {
	sink := &readAttemptRecorder{}
	s := retrySDKAPI(t, closedTCPAddr(t), 3, sink)
	began := time.Now()
	_, err := s.listGraphs(context.Background())
	spent := time.Since(began)
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	want := []readAttempt{
		{operation: "list graphs", attempt: 1, maxAttempts: 3, cause: ReadFailureConnectionRefused, retried: true},
		{operation: "list graphs", attempt: 2, maxAttempts: 3, cause: ReadFailureConnectionRefused, retried: true},
		{operation: "list graphs", attempt: 3, maxAttempts: 3, cause: ReadFailureConnectionRefused, retried: false},
	}
	got := sink.all()
	if len(got) != len(want) {
		t.Fatalf("records = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if spent < 150*time.Millisecond || spent > 5*time.Second {
		t.Fatalf("three refused attempts took %s, want at least the 50ms+100ms backoff and under 5s", spent)
	}
}

func syscallOpError(errno syscall.Errno) error {
	return &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: errno}}
}

func realEOF(t *testing.T) error {
	t.Helper()
	server, client := net.Pipe()
	_ = server.Close()
	_, err := client.Read(make([]byte, 1))
	_ = client.Close()
	return err
}

func realRefused(t *testing.T) error {
	t.Helper()
	_, err := net.Dial("tcp", closedTCPAddr(t))
	return err
}

func realReadTimeout(t *testing.T) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	_, err = conn.Read(make([]byte, 1))
	return err
}

func TestReadFailureCauseTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		cause string
		retry bool
	}{
		{"real EOF", realEOF(t), ReadFailureEOF, true},
		{"unexpected EOF", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), ReadFailureEOF, true},
		{"real refused dial", realRefused(t), ReadFailureConnectionRefused, true},
		{"connection reset", syscallOpError(syscall.ECONNRESET), ReadFailureConnectionReset, true},
		{"broken pipe", syscallOpError(syscall.EPIPE), ReadFailureBrokenPipe, true},
		{"connection aborted", syscallOpError(syscall.ECONNABORTED), ReadFailureConnectionAborted, true},
		{"loading reply", errors.New("LOADING Redis is loading the dataset in memory"), ReadFailureLoading, true},
		{"real read timeout", realReadTimeout(t), ReadFailureTimeout, false},
		{"os deadline", os.ErrDeadlineExceeded, ReadFailureTimeout, false},
		{"pool timeout", redis.ErrPoolTimeout, ReadFailurePoolTimeout, false},
		{"context deadline", context.DeadlineExceeded, "", false},
		{"wrapped context deadline", fmt.Errorf("dial: %w", context.DeadlineExceeded), "", false},
		{"context canceled", context.Canceled, "", false},
		{"wrong password", errors.New("WRONGPASS invalid username-password pair"), "", false},
		{"no auth", errors.New("NOAUTH Authentication required."), "", false},
		{"server query timeout", errors.New("Query timed out"), "", false},
		{"empty key", errors.New("Invalid graph operation on empty key"), "", false},
		{"max clients", errors.New("ERR max number of clients reached"), "", false},
		{"readonly replica", errors.New("READONLY You can't write against a read only replica."), "", false},
		{"closed client", net.ErrClosed, "", false},
		{"generic", errors.New("something else"), "", false},
	} {
		cause, retry := readFailureCause(tc.err)
		if cause != tc.cause || retry != tc.retry {
			t.Errorf("%s (%v): cause=%q retry=%v, want %q %v", tc.name, tc.err, cause, retry, tc.cause, tc.retry)
		}
	}
}

// TestTheGraphReadAttemptWarnNamesNoAddress drives the real SlogTelemetry
// through the real read client and reads the emitted lines back.
func TestTheGraphReadAttemptWarnNamesNoAddress(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
	d := newConnDropper()
	addr := droppingRESPServer(t, d)
	s := retrySDKAPI(t, addr, 3, SlogTelemetry{Logger: logger})
	d.arm("GRAPH.LIST", -1)
	if _, err := s.listGraphs(context.Background()); err == nil {
		t.Fatal("persistent drop returned no error")
	}
	text := buf.String()
	_, port, _ := net.SplitHostPort(addr)
	if strings.Contains(text, addr) || strings.Contains(text, port) || strings.Contains(text, "127.0.0.1") {
		t.Fatalf("the warn lines name the address: %s", text)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3: %s", len(lines), text)
	}
	for i, line := range lines {
		var got map[string]interface{}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]interface{}{
			"level": "WARN", "msg": "context_fabric: graph read attempt failed", "operation": "list graphs",
			"attempt": float64(i + 1), "max_attempts": float64(3), "cause": ReadFailureEOF, "retried": i < 2,
		}
		if len(got) != len(want) {
			t.Fatalf("line %d = %v, want exactly %v", i, got, want)
		}
		for key, value := range want {
			if got[key] != value {
				t.Fatalf("line %d %s = %v, want %v", i, key, got[key], value)
			}
		}
	}
}
