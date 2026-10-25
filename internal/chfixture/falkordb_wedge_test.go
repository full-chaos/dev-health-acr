package chfixture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// wedgingServer answers PING and GRAPH.LIST until the first GRAPH.QUERY arrives; from then on every connection, old
// or new, is accepted and never answered, until thaw is closed, after which it answers again; once thawed, the index
// command fails until indexReadyAt.
type wedgingServer struct {
	addr         string
	wedged       atomic.Bool
	thaw         chan struct{}
	indexReadyAt atomic.Int64
}

func newWedgingServer(t *testing.T) *wedgingServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &wedgingServer{addr: ln.Addr().String(), thaw: make(chan struct{})}
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
			go s.serve(c)
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
	return s
}

func (s *wedgingServer) frozen() bool {
	if !s.wedged.Load() {
		return false
	}
	select {
	case <-s.thaw:
		return false
	default:
		return true
	}
}

func (s *wedgingServer) serve(c net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		req := strings.ToUpper(string(buf[:n]))
		if strings.Contains(req, "GRAPH.QUERY") {
			s.wedged.CompareAndSwap(false, true)
		}
		if s.frozen() {
			continue
		}
		switch {
		case strings.Contains(req, "PING"):
			_, _ = c.Write([]byte("+PONG\r\n"))
		case strings.Contains(req, "GRAPH.LIST"):
			_, _ = c.Write([]byte("*0\r\n"))
		case strings.Contains(req, "GRAPH.QUERY") && time.Now().UnixNano() < s.indexReadyAt.Load():
			_, _ = c.Write([]byte("-ERR Index not ready\r\n"))
		case strings.Contains(req, "GRAPH.QUERY"):
			_, _ = c.Write([]byte("*1\r\n$18\r\nIndices created: 1\r\n"))
		case strings.Contains(req, "GRAPH.DELETE"):
			_, _ = c.Write([]byte("+Graph removed\r\n"))
		case strings.Contains(req, "HELLO"), strings.Contains(req, "CLIENT"):
			_, _ = c.Write([]byte("-ERR unknown command\r\n"))
		}
	}
}

func insideNoReply(context.Context) error {
	return errors.New("exec redis-cli ping: context deadline exceeded")
}

func TestAwaitFalkorServingNamesAServerThatStopsAnsweringAfterItServed(t *testing.T) {
	s := newWedgingServer(t)
	var pl probeLog
	began := time.Now()
	err := awaitFalkorServingInside(context.Background(), s.addr, 20*time.Second, 100*time.Millisecond, 20*time.Millisecond, 300*time.Millisecond, insideNoReply, pl.logf)
	if !errors.Is(err, ErrServerWedged) {
		t.Fatalf("a server that answered PING and then went silent on the port and inside must be named as stalled: %v", err)
	}
	if errors.Is(err, ErrPublishedPortUnreachable) {
		t.Fatalf("a stalled server is not a dead published port: %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the verdict must come after the silence window, not after the whole budget: %s", took)
	}
	var wedged *ServerWedgedError
	if !errors.As(err, &wedged) {
		t.Fatalf("the error must carry the stall facts: %T", err)
	}
	wantStatement := `GRAPH.QUERY ` + wedged.Graph + ` "CREATE INDEX FOR (n:ReadyProbe) ON (n.k)"`
	if !strings.HasPrefix(wedged.Graph, "acr_ready_probe_") || wedged.InFlight != wantStatement || wedged.ServedTry != 1 {
		t.Fatalf("graph=%q inFlight=%q servedTry=%d", wedged.Graph, wedged.InFlight, wedged.ServedTry)
	}
	if wedged.Silent < 300*time.Millisecond {
		t.Fatalf("the stall must be reported only after the silence window, got %s", wedged.Silent)
	}
	for _, want := range []string{"stopped answering after it served", "try 1 answered PING", wantStatement, "PING", "inside the container: exec redis-cli ping: context deadline exceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must carry %q: %v", want, err)
		}
	}
}

func TestAwaitFalkorServingNeverCallsANeverServedServerStalled(t *testing.T) {
	addr := silentListener(t)
	var pl probeLog
	err := awaitFalkorServingInside(context.Background(), addr, 700*time.Millisecond, 100*time.Millisecond, 20*time.Millisecond, 200*time.Millisecond, insideNoReply, pl.logf)
	if err == nil || errors.Is(err, ErrServerWedged) {
		t.Fatalf("a server that never answered PING is not up, not stalled: %v", err)
	}
	if !strings.Contains(err.Error(), "within 700ms") {
		t.Fatalf("a server that never answered must use the whole budget: %v", err)
	}
}

func TestAwaitFalkorServingCallsASilentPortWithALiveInsideServerUnreachableEvenAfterItServed(t *testing.T) {
	s := newWedgingServer(t)
	var pl probeLog
	err := awaitFalkorServingInside(context.Background(), s.addr, 20*time.Second, 100*time.Millisecond, 20*time.Millisecond, 300*time.Millisecond, func(context.Context) error { return nil }, pl.logf)
	if !errors.Is(err, ErrPublishedPortUnreachable) || errors.Is(err, ErrServerWedged) {
		t.Fatalf("a server that answers inside is not stalled, whatever the port does: %v", err)
	}
}

func TestAwaitFalkorServingAcceptsAServerWhoseSilenceEndsInsideTheWindow(t *testing.T) {
	s := newWedgingServer(t)
	go func() {
		for !s.wedged.Load() {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(250 * time.Millisecond)
		close(s.thaw)
	}()
	var pl probeLog
	if err := awaitFalkorServingInside(context.Background(), s.addr, 20*time.Second, 100*time.Millisecond, 20*time.Millisecond, 2*time.Second, insideNoReply, pl.logf); err != nil {
		t.Fatalf("a server that answers again before the silence window ends must be accepted: %v\n%s", err, strings.Join(pl.lines, "\n"))
	}
}

func TestAwaitFalkorServingResetsTheSilenceWhenTheServerAnswersPINGAgain(t *testing.T) {
	s := newWedgingServer(t)
	go func() {
		for !s.wedged.Load() {
			time.Sleep(5 * time.Millisecond)
		}
		s.indexReadyAt.Store(time.Now().Add(1500 * time.Millisecond).UnixNano())
		time.Sleep(250 * time.Millisecond)
		close(s.thaw)
	}()
	var pl probeLog
	if err := awaitFalkorServingInside(context.Background(), s.addr, 20*time.Second, 100*time.Millisecond, 20*time.Millisecond, 600*time.Millisecond, insideNoReply, pl.logf); err != nil {
		t.Fatalf("a silence that ended must not count toward a later stall verdict: %v\n%s", err, strings.Join(pl.lines, "\n"))
	}
}

func TestAwaitFalkorServingNeverCallsAServerThatStillAnswersPINGStalled(t *testing.T) {
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
	err = awaitFalkorServingInside(context.Background(), ln.Addr().String(), 1500*time.Millisecond, 100*time.Millisecond, 20*time.Millisecond, 300*time.Millisecond, insideNoReply, pl.logf)
	if err == nil || errors.Is(err, ErrServerWedged) {
		t.Fatalf("a server whose host PING still answers is not stalled, whatever the inside probe says: %v", err)
	}
	if !strings.Contains(err.Error(), "CREATE INDEX") || !strings.Contains(err.Error(), "within 1.5s") {
		t.Fatalf("the hard fail must carry the index error after the whole budget: %v", err)
	}
}

type stalledTarget struct {
	host, port string
	hangExec   bool
	inspectErr error
}

func (s *stalledTarget) Host(context.Context) (string, error) { return s.host, nil }
func (s *stalledTarget) Ports(context.Context) (network.PortMap, error) {
	return nil, errors.New("not used")
}

func (s *stalledTarget) MappedPort(context.Context, string) (network.Port, error) {
	return network.ParsePort(s.port + "/tcp")
}

func (s *stalledTarget) Inspect(context.Context) (*container.InspectResponse, error) {
	if s.inspectErr != nil {
		return nil, s.inspectErr
	}
	return &container.InspectResponse{
		Image:  "sha256:1111",
		Config: &container.Config{Image: "falkordb/falkordb@sha256:ad09"},
		State:  &container.State{Status: "running", Running: true, Pid: 4242, StartedAt: "2026-10-25T00:44:46Z"},
	}, nil
}

func (s *stalledTarget) Logs(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(strings.Repeat("x", 9000) + "\n1:M * Ready to accept connections tcp\n")), nil
}

func (s *stalledTarget) Exec(ctx context.Context, cmd []string, _ ...tcexec.ProcessOption) (int, io.Reader, error) {
	if s.hangExec {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}
	switch cmd[0] {
	case "redis-cli":
		<-ctx.Done()
		return 0, nil, ctx.Err()
	case "cat":
		return 0, bytes.NewBufferString("Name:\tredis-server\nUmask:\t0022\nState:\tS (sleeping)\nThreads:\t9\nVmRSS:\t12345 kB\n"), nil
	default:
		return 0, bytes.NewBufferString("1 S do_epoll_wait redis-server\n7 S futex_wait_queue redis-server\n"), nil
	}
}

func (s *stalledTarget) State(context.Context) (*container.State, error) {
	return &container.State{Status: "running", Running: true}, nil
}

func (s *stalledTarget) CopyFileFromContainer(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

func TestFalkorReadyReportsTheContainerStateOfAStalledServer(t *testing.T) {
	s := newWedgingServer(t)
	host, port, _ := net.SplitHostPort(s.addr)
	err := falkorServing{budget: time.Minute, tryLimit: 200 * time.Millisecond, poll: 20 * time.Millisecond, unreachableAfter: 500 * time.Millisecond}.WaitUntilReady(context.Background(), &stalledTarget{host: host, port: port})
	if !errors.Is(err, ErrServerWedged) {
		t.Fatalf("want a stalled-server failure: %v", err)
	}
	for _, want := range []string{
		"container image: falkordb/falkordb@sha256:ad09 (image id sha256:1111)",
		"container state: status=running running=true paused=false",
		"pid=4242",
		"/proc/1/status (exit 0):", "State:\tS (sleeping)", "Threads:\t9", "VmRSS:\t12345 kB",
		"threads of pid 1 (tid state wchan comm) (exit 0):", "7 S futex_wait_queue redis-server",
		"container log (last 4096 bytes):", "Ready to accept connections tcp",
		`"CREATE INDEX FOR (n:ReadyProbe) ON (n.k)"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure must carry %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "Umask") {
		t.Fatalf("only the named /proc/1/status lines are kept:\n%v", err)
	}
}

func TestFalkorReadyStillFailsInBoundedTimeWhenTheDiagnosticReadsHang(t *testing.T) {
	s := newWedgingServer(t)
	host, port, _ := net.SplitHostPort(s.addr)
	began := time.Now()
	err := falkorServing{budget: time.Minute, tryLimit: 200 * time.Millisecond, poll: 20 * time.Millisecond, unreachableAfter: 500 * time.Millisecond}.WaitUntilReady(context.Background(), &stalledTarget{host: host, port: port, hangExec: true, inspectErr: errors.New("daemon busy")})
	if !errors.Is(err, ErrServerWedged) {
		t.Fatalf("want a stalled-server failure: %v", err)
	}
	for _, want := range []string{"container inspect: unavailable: daemon busy", "/proc/1/status: unavailable: context deadline exceeded", "threads of pid 1 (tid state wchan comm): unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("a failed read must be reported in place, missing %q:\n%v", want, err)
		}
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("diagnostics must be bounded per read: %s", took)
	}
}
