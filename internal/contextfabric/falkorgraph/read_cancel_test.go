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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
