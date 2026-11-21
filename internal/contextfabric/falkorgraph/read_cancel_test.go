package falkorgraph

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// cancelEndpoint is one read path of the read client, called through the
// adapter, with the graph command it sends.
type cancelEndpoint struct {
	name    string
	command string
	call    func(*Adapter, context.Context) error
}

var cancelEndpoints = []cancelEndpoint{
	{"watermark", "GRAPH.RO_QUERY", func(a *Adapter, ctx context.Context) error {
		_, err := a.ProjectionWatermark(ctx, "cancel-org", "cancel-source")
		return err
	}},
	{"constraints", "GRAPH.RO_QUERY", func(a *Adapter, ctx context.Context) error {
		_, err := a.api.constraints(ctx, "cancel-graph")
		return err
	}},
	{"indexes", "GRAPH.RO_QUERY", func(a *Adapter, ctx context.Context) error {
		_, err := a.api.indexes(ctx, "cancel-graph")
		return err
	}},
	{"list", "GRAPH.LIST", func(a *Adapter, ctx context.Context) error {
		_, err := a.CountOrganizationGraphs(ctx, "cancel-org")
		return err
	}},
}

const cancelHelloRESP3 = "%2\r\n+server\r\n+redis\r\n+proto\r\n:3\r\n"

// cancelServer is a fake RESP server. It counts the target graph command and
// answers it with respond; every other command is connection setup. handshake
// sees each setup command with the count so far.
func cancelServer(t *testing.T, target, helloReply string, logs io.Writer, handshake func(raw string, sends int64), respond func(net.Conn, int64)) (*Adapter, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var sends atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					raw, cmd, err := readRawRESPCommand(reader)
					if err != nil {
						return
					}
					if cmd == target {
						respond(conn, sends.Add(1))
						continue
					}
					if handshake != nil {
						handshake(string(raw), sends.Load())
					}
					reply := "+OK\r\n"
					if cmd == "HELLO" {
						reply = helloReply
						if reply == "" {
							reply = "-ERR unknown command 'HELLO'\r\n"
						}
					}
					if _, err := io.WriteString(conn, reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	a, err := New(Config{
		Addr: listener.Addr().String(), GraphPrefix: "cancel-falkor", AllowInsecure: true,
		RequestTimeout: time.Second, MaxAttempts: 3, MaxResults: 25, PoolSize: 2,
		Telemetry: SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))},
	})
	if err != nil {
		t.Fatal(err)
	}
	sdk := a.api.(projectReachConn).conn.(*sdkAPI)
	t.Cleanup(func() { _ = sdk.read.Conn.Close(); _ = sdk.db.Conn.Close() })
	return a, &sends
}

func cancelReplyOK(command string) string {
	if command == "GRAPH.LIST" {
		return "*0\r\n"
	}
	stat := "Query internal execution time: 0.01 milliseconds"
	return fmt.Sprintf("*3\r\n*0\r\n*0\r\n*1\r\n$%d\r\n%s\r\n", len(stat), stat)
}

// isLastSetupCommand matches the last CLIENT SETINFO of a new connection.
func isLastSetupCommand(raw string) bool {
	return strings.Contains(strings.ToUpper(raw), "\r\nLIB-VER\r\n")
}

// requireCancelClass asserts a cancelled read reads as the caller's cancel:
// not a graph dependency failure, no driver cause, no read attempt warning and
// no error line.
func requireCancelClass(t *testing.T, err error, logs string, wantWarnings int) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("err = %v reads as a graph dependency failure", err)
	}
	if cause, ok := contextfabric.DependencyCause(err); ok {
		t.Fatalf("err = %v carries the driver cause %q", err, cause)
	}
	if got := strings.Count(logs, "graph read attempt failed"); got != wantWarnings {
		t.Fatalf("read attempt warnings = %d, want %d:\n%s", got, wantWarnings, logs)
	}
	if strings.Contains(logs, `"level":"ERROR"`) {
		t.Fatalf("a cancelled read wrote an error line:\n%s", logs)
	}
}

// TestACancelDuringConnectionSetupSendsNoRead: a cancel that lands while the
// read client sets up a new connection stops the read before its command is
// written, on every read path and both protocols. On the first send nothing
// reaches the server; after a dropped first attempt the retry sends nothing
// more. A deadline in the same window is the control.
func TestACancelDuringConnectionSetupSendsNoRead(t *testing.T) {
	for _, endpoint := range cancelEndpoints {
		for _, protocol := range []string{"resp2", "resp3"} {
			for _, phase := range []string{"first_send", "retry_after_drop"} {
				for _, ending := range []string{"cancel", "deadline"} {
					t.Run(endpoint.name+"/"+protocol+"/"+phase+"/"+ending, func(t *testing.T) {
						var logs bytes.Buffer
						var reached, armed atomic.Bool
						ctx, cancel := context.WithCancel(context.Background())
						if ending == "deadline" {
							cancel()
							ctx, cancel = context.WithTimeout(context.Background(), 160*time.Millisecond)
						}
						defer cancel()
						hello := ""
						if protocol == "resp3" {
							hello = cancelHelloRESP3
						}
						a, sends := cancelServer(t, endpoint.command, hello, &logs, func(raw string, sends int64) {
							if armed.Load() && sends == 1 && isLastSetupCommand(raw) {
								reached.Store(true)
								if ending == "deadline" {
									<-ctx.Done()
								} else {
									cancel()
								}
							}
						}, func(c net.Conn, attempt int64) {
							if attempt == 1 {
								_ = c.Close()
								return
							}
							_, _ = io.WriteString(c, cancelReplyOK(endpoint.command))
						})
						if phase == "first_send" {
							// The first command drops its connection, so the
							// read under test sets up a new one.
							warm, stopWarm := context.WithTimeout(context.Background(), 40*time.Millisecond)
							_ = endpoint.call(a, warm)
							stopWarm()
							if sends.Load() != 1 {
								t.Fatalf("fixture setup sent %d commands, want 1", sends.Load())
							}
						}
						logs.Reset()
						armed.Store(true)
						before := sends.Load()
						err := endpoint.call(a, ctx)
						sent := sends.Load() - before
						if !reached.Load() {
							t.Fatal("the read never reached the last connection setup step, so this measured nothing")
						}
						want := int64(0)
						if phase == "retry_after_drop" {
							want = 1
						}
						if sent != want {
							t.Fatalf("sends = %d, want %d: a read was written after its %s", sent, want, ending)
						}
						if ending == "deadline" {
							if !errors.Is(err, context.DeadlineExceeded) {
								t.Fatalf("err = %v, want context.DeadlineExceeded", err)
							}
							return
						}
						warnings := 0
						if phase == "retry_after_drop" {
							warnings = 1 // the dropped first attempt, retried=true
						}
						requireCancelClass(t, err, logs.String(), warnings)
					})
				}
			}
		}
	}
}

// TestACancelledReadReturnsTheCancelOverAReply: a cancel that lands after the
// server holds the read returns context.Canceled, not the reply that arrives
// (rows, an absent graph, an auth refusal). A transient failure or a timeout
// in the same spot already returned the cancel and is the control.
func TestACancelledReadReturnsTheCancelOverAReply(t *testing.T) {
	for _, endpoint := range cancelEndpoints {
		for _, reply := range []string{"ok", "empty_key", "noauth", "eof", "loading", "timeout"} {
			if endpoint.command == "GRAPH.LIST" && reply == "empty_key" {
				continue
			}
			t.Run(endpoint.name+"/"+reply, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var logs bytes.Buffer
				a, sends := cancelServer(t, endpoint.command, "", &logs, nil, func(c net.Conn, _ int64) {
					cancel()
					writeCancelReply(c, endpoint.command, reply)
				})
				logs.Reset()
				err := endpoint.call(a, ctx)
				if sends.Load() != 1 {
					t.Fatalf("sends = %d, want 1", sends.Load())
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled over the %s reply", err, reply)
				}
				warnings := 0
				if reply == "eof" || reply == "loading" {
					warnings = 1 // a transient cause after the end, retried=false
				}
				requireCancelClass(t, err, logs.String(), warnings)
			})
		}
	}
}

// TestALiveReadReturnsItsReply is the live-context control: the same replies
// with no cancel come back as they are.
func TestALiveReadReturnsItsReply(t *testing.T) {
	for _, endpoint := range cancelEndpoints {
		for _, reply := range []string{"ok", "empty_key", "noauth"} {
			if endpoint.command == "GRAPH.LIST" && reply == "empty_key" {
				continue
			}
			t.Run(endpoint.name+"/"+reply, func(t *testing.T) {
				var logs bytes.Buffer
				a, sends := cancelServer(t, endpoint.command, "", &logs, nil, func(c net.Conn, _ int64) {
					writeCancelReply(c, endpoint.command, reply)
				})
				err := endpoint.call(a, context.Background())
				if sends.Load() != 1 {
					t.Fatalf("sends = %d, want 1", sends.Load())
				}
				switch reply {
				case "ok":
					if endpoint.name == "watermark" {
						if !errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound) {
							t.Fatalf("err = %v, want the watermark not found for no rows", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("err = %v, want none", err)
					}
				case "empty_key":
					if !errors.Is(err, ErrNotFound) && !errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound) {
						t.Fatalf("err = %v, want not found", err)
					}
				case "noauth":
					if !errors.Is(err, ErrUnauthorized) {
						t.Fatalf("err = %v, want ErrUnauthorized", err)
					}
				}
			})
		}
	}
}

func writeCancelReply(c net.Conn, command, reply string) {
	switch reply {
	case "ok":
		_, _ = io.WriteString(c, cancelReplyOK(command))
	case "empty_key":
		_, _ = io.WriteString(c, "-ERR Invalid graph operation on empty key\r\n")
	case "noauth":
		_, _ = io.WriteString(c, "-NOAUTH Authentication required.\r\n")
	case "loading":
		_, _ = io.WriteString(c, "-LOADING Redis is loading the dataset in memory\r\n")
	case "eof":
		_ = c.Close()
	case "timeout":
		// No reply: the read runs to RequestTimeout.
	}
}

// TestACancelDuringConnectionSetupStillSendsAWrite: the write client does not
// change. A write with a cancel during its connection setup is still sent once
// and runs to its reply, because a started write must never be cut.
func TestACancelDuringConnectionSetupStillSendsAWrite(t *testing.T) {
	for _, protocol := range []string{"resp2", "resp3"} {
		t.Run(protocol, func(t *testing.T) {
			var logs bytes.Buffer
			var reached, armed atomic.Bool
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hello := ""
			if protocol == "resp3" {
				hello = cancelHelloRESP3
			}
			a, sends := cancelServer(t, "GRAPH.QUERY", hello, &logs, func(raw string, sends int64) {
				if armed.Load() && sends == 1 && isLastSetupCommand(raw) {
					reached.Store(true)
					cancel()
				}
			}, func(c net.Conn, attempt int64) {
				if attempt == 1 {
					_ = c.Close()
					return
				}
				_, _ = io.WriteString(c, cancelReplyOK("GRAPH.QUERY"))
			})
			// The first write drops its connection, so the write under test
			// sets up a new one.
			_, _ = a.api.query(context.Background(), "cancel-graph", "CREATE (:Probe)", nil, false)
			if sends.Load() != 1 {
				t.Fatalf("fixture setup sent %d writes, want 1", sends.Load())
			}
			armed.Store(true)
			_, err := a.api.query(ctx, "cancel-graph", "CREATE (:Probe)", nil, false)
			if !reached.Load() {
				t.Fatal("the write never reached the last connection setup step, so this measured nothing")
			}
			if got := sends.Load() - 1; got != 1 {
				t.Fatalf("write sends = %d, want 1: the write client must not stop a write at the write boundary", got)
			}
			if err != nil {
				t.Fatalf("err = %v, want the write's own reply", err)
			}
		})
	}
}

// TestAReplyAfterTheEndStillProvesTheConnection: a read whose reply arrives
// after its context ended returns the context's error, and the reply still
// marks the connection as proven, so a later timeout is not read as a TLS
// handshake failure.
func TestAReplyAfterTheEndStillProvesTheConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sdkAPI{config: Config{MaxAttempts: 3}}
	calls := 0
	_, err := retryRead(ctx, s, "list graphs", func() (int, error) { calls++; cancel(); return 1, nil })
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d err=%v, want 1 call and context.Canceled", calls, err)
	}
	if !s.everConnected.Load() {
		t.Fatal("a reply after the end did not mark the connection as proven")
	}
}

// TestARetryIsRecordedOnlyWhenTheNextAttemptReachesTheWrite: the first attempt
// drops its connection; the retry sets up a new one, and the caller's cancel or
// deadline lands during that setup. The retry is then stopped before its
// command is written, so the first attempt's record says retried=false. With a
// live context the retry is sent and the record says retried=true.
func TestARetryIsRecordedOnlyWhenTheNextAttemptReachesTheWrite(t *testing.T) {
	for _, endpoint := range cancelEndpoints {
		for _, protocol := range []string{"resp2", "resp3"} {
			for _, end := range []string{"cancel", "deadline", "live"} {
				t.Run(endpoint.name+"/"+protocol+"/"+end, func(t *testing.T) {
					var logs bytes.Buffer
					var setup atomic.Bool
					var ctx context.Context
					var stop context.CancelFunc
					if end == "deadline" {
						ctx, stop = context.WithTimeout(context.Background(), 250*time.Millisecond)
					} else {
						ctx, stop = context.WithCancel(context.Background())
					}
					defer stop()
					hello := ""
					if protocol == "resp3" {
						hello = cancelHelloRESP3
					}
					a, sends := cancelServer(t, endpoint.command, hello, &logs, func(raw string, sent int64) {
						if sent != 1 || !isLastSetupCommand(raw) {
							return
						}
						setup.Store(true)
						switch end {
						case "cancel":
							stop()
						case "deadline":
							<-ctx.Done()
						}
					}, func(c net.Conn, sent int64) {
						if sent == 1 {
							_ = c.Close()
							return
						}
						_, _ = io.WriteString(c, cancelReplyOK(endpoint.command))
					})
					logs.Reset()
					err := endpoint.call(a, ctx)
					if !setup.Load() {
						t.Fatal("the retry never reached a new connection's setup, so this measured nothing")
					}
					count := sends.Load()
					log := logs.String()
					retried := strings.Contains(log, `"retried":true`)
					switch end {
					case "cancel":
						if count != 1 || !errors.Is(err, context.Canceled) {
							t.Fatalf("sends=%d err=%v, want 1 send and context.Canceled", count, err)
						}
					case "deadline":
						if count != 1 || !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("sends=%d err=%v, want 1 send and context.DeadlineExceeded", count, err)
						}
					case "live":
						if count != 2 || (err != nil && !errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound)) {
							t.Fatalf("sends=%d err=%v, want 2 sends and the reply (no rows)", count, err)
						}
					}
					if retried != (count == 2) {
						t.Fatalf("retried=%t with %d sends: the record must say retried only when the retry was written:\n%s", retried, count, log)
					}
					if got := strings.Count(log, "graph read attempt failed"); got != 1 {
						t.Fatalf("read attempt records = %d, want 1 (the dropped first attempt):\n%s", got, log)
					}
				})
			}
		}
	}
}

// TestACancelDuringResultDecodingReturnsTheCancel: a compact reply names
// labels, relationship types and property keys by number, and the decoder
// looks them up with more graph calls. A cancel or deadline that lands during
// one lookup sends no later lookup and returns the context's error, never rows
// with no error. A live read is the control.
func TestACancelDuringResultDecodingReturnsTheCancel(t *testing.T) {
	cases := []struct {
		shape  string
		stopAt string
	}{
		{"watermark", "labels"},
		{"watermark", "properties"},
		{"edge-page", "labels"},
		{"edge-page", "types"},
		{"edge-page", "properties"},
		{"watermark", "never"},
		{"edge-page", "never"},
	}
	for _, protocol := range []string{"resp2", "resp3"} {
		for _, tc := range cases {
			endings := []string{"cancel", "deadline"}
			if tc.stopAt == "never" {
				endings = []string{"live"}
			}
			for _, ending := range endings {
				t.Run(protocol+"/"+tc.shape+"/"+tc.stopAt+"/"+ending, func(t *testing.T) {
					runDecodeCancel(t, protocol, tc.shape, tc.stopAt, ending)
				})
			}
		}
	}
}

func runDecodeCancel(t *testing.T, protocol, shape, stopAt, ending string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var ctx context.Context
	var cancel context.CancelFunc
	var wantErr error
	if ending == "deadline" {
		ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
		wantErr = context.DeadlineExceeded
	} else {
		ctx, cancel = context.WithCancel(context.Background())
		wantErr = context.Canceled
	}
	defer cancel()

	keys := []string{propRelationshipID, propRelationType, propKind, propCanonicalID, "projected_at", "cursor"}
	node := func(id, label int, props ...string) string {
		return respArray(":8\r\n", respArray(respInt(id), respArray(respInt(label)), respArray(props...)))
	}
	prop := func(id int, value string) string {
		return respArray(respInt(id), respInt(2), respString(value))
	}
	nodeReply := respCompact([]string{"w"}, node(0, 0, prop(4, "2026-11-21T00:00:00Z"), prop(5, "cursor-1")))
	edge := respArray(respInt(7), respArray(respInt(0), respInt(0), respInt(1), respInt(2),
		respArray(prop(0, "edge-1"), prop(1, "links_pull_request"))))
	edgeReply := respCompact([]string{"r", "a", "b"}, edge, node(1, 1, prop(2, "work_item"), prop(3, "issue-1")), node(2, 1, prop(2, "pull_request"), prop(3, "pull-1")))

	var mu sync.Mutex
	var metadata, afterEnd []string
	var primary atomic.Int64
	// ended is set once the chosen lookup has seen the context end. A lookup
	// that arrives after it was sent after the end, whether or not the
	// context's own timer has run yet.
	var ended, reached atomic.Bool
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					raw, cmd, err := readRawRESPCommand(r)
					if err != nil {
						return
					}
					reply := "+OK\r\n"
					if cmd == "HELLO" {
						reply = "-ERR unknown command 'HELLO'\r\n"
						if protocol == "resp3" {
							reply = cancelHelloRESP3
						}
					}
					query := string(raw)
					procedure := ""
					switch {
					case strings.Contains(query, "RETURN w"):
						primary.Add(1)
						reply = nodeReply
					case strings.Contains(query, "RETURN r, a, b"):
						primary.Add(1)
						reply = edgeReply
					case strings.Contains(query, "db.labels"):
						procedure = "labels"
						reply = respSchema("label", labelWatermark, labelSubject)
					case strings.Contains(query, "db.relationshipTypes"):
						procedure = "types"
						reply = respSchema("relationshipType", labelRelation)
					case strings.Contains(query, "db.propertyKeys"):
						procedure = "properties"
						reply = respSchema("propertyKey", keys...)
					}
					if procedure != "" {
						mu.Lock()
						name := cmd + "/" + procedure
						metadata = append(metadata, name)
						if ended.Load() || ctx.Err() != nil {
							afterEnd = append(afterEnd, name)
						}
						mu.Unlock()
						if procedure == stopAt {
							reached.Store(true)
							if ending == "deadline" {
								<-ctx.Done()
							} else {
								cancel()
							}
							ended.Store(true)
						}
					}
					if _, err := io.WriteString(c, reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	var logs bytes.Buffer
	a, err := New(Config{Addr: listener.Addr().String(), GraphPrefix: "cancel-falkor", AllowInsecure: true, RequestTimeout: time.Second, MaxAttempts: 3, MaxResults: 25, PoolSize: 2,
		Telemetry: SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}})
	if err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	sdk := a.api.(projectReachConn).conn.(*sdkAPI)
	defer sdk.read.Conn.Close()
	defer sdk.db.Conn.Close()

	result := ""
	if shape == "watermark" {
		var watermark contextfabric.ProjectionWatermark
		watermark, err = a.ProjectionWatermark(ctx, "cancel-org", "cancel-source")
		result = watermark.Cursor
	} else {
		var page directread.EdgePage
		page, err = a.DirectEdgePage(ctx, storage.Principal{OrgID: "cancel-org"}, contextfabric.ResolvedGraphBinding{},
			directread.EdgePageQuery{Origins: []contextfabric.SubjectRef{{Kind: "work_item", CanonicalID: "issue-1"}},
				Direction: directread.EdgeDirectionOut, Limit: 1, ValidAt: time.Now()})
		if len(page.Edges) > 0 {
			result = page.Edges[0].Key.RelationshipID
		}
	}
	mu.Lock()
	looked, later := slices.Clone(metadata), slices.Clone(afterEnd)
	mu.Unlock()
	t.Logf("%s/%s/%s/%s: primary=%d lookups=%v after_end=%v result=%q err=%v", protocol, shape, stopAt, ending, primary.Load(), looked, later, result, err)
	if primary.Load() != 1 {
		t.Fatal("the primary read was not sent once")
	}
	if stopAt == "never" {
		if err != nil || result == "" {
			t.Fatalf("a live read returned %q, %v; want a result", result, err)
		}
		return
	}
	if !reached.Load() {
		t.Fatalf("the read never reached the %s lookup, so this measured nothing", stopAt)
	}
	// The read can return at its socket deadline before the context's timer
	// marks it done, so wait for the end before asking which end it was.
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the context never ended")
	}
	if !errors.Is(ctx.Err(), wantErr) {
		t.Fatalf("the context ended with %v, want %v during the lookup", ctx.Err(), wantErr)
	}
	if len(later) != 0 {
		t.Fatalf("lookups sent after the end: %v", later)
	}
	if !errors.Is(err, wantErr) || result != "" {
		t.Fatalf("result=%q err=%v, want no result and %v", result, err, wantErr)
	}
	if errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("err = %v reads as a graph dependency failure", err)
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("a read ended during decoding wrote an error line:\n%s", logs.String())
	}
}

func respInt(n int) string         { return fmt.Sprintf(":%d\r\n", n) }
func respString(s string) string   { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }
func respArray(v ...string) string { return fmt.Sprintf("*%d\r\n%s", len(v), strings.Join(v, "")) }

func respSchema(column string, values ...string) string {
	rows := make([]string, 0, len(values))
	for _, value := range values {
		rows = append(rows, respArray(respArray(respInt(2), respString(value))))
	}
	return respArray(respArray(respArray(respInt(1), respString(column))), respArray(rows...), respArray(respString("Query internal execution time: 0.01 milliseconds")))
}

func respCompact(columns []string, row ...string) string {
	header := make([]string, 0, len(columns))
	for _, column := range columns {
		header = append(header, respArray(respInt(1), respString(column)))
	}
	return respArray(respArray(header...), respArray(respArray(row...)), respArray(respString("Query internal execution time: 0.01 milliseconds")))
}
