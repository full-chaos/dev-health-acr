package mcpclientfixtures

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const childStderrTailBytes = 4096

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > childStderrTailBytes {
		b.buf = b.buf[len(b.buf)-childStderrTailBytes:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// connectStdioWithDiagnostics connects an MCP client over cmd and, when the
// handshake fails, reports how the child ended (exit code or signal), how long
// the attempt ran, and the tail of the child's stderr.
func connectStdioWithDiagnostics(ctx context.Context, cmd *exec.Cmd) (*mcpsdk.ClientSession, error) {
	tail := &tailBuffer{}
	cmd.Stderr = tail
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcpclientfixtures-smoke", Version: "0.0.1"}, nil)
	start := time.Now()
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err == nil {
		return session, nil
	}
	elapsed := time.Since(start)
	return nil, fmt.Errorf("%w\nelapsed before failure: %s\nchild: %s\nchild stderr tail: %q",
		err, elapsed, describeChildExit(cmd), strings.TrimSpace(tail.String()))
}

func describeChildExit(cmd *exec.Cmd) string {
	if cmd.Process == nil {
		return "never started"
	}
	state := cmd.ProcessState
	if state == nil {
		done := make(chan struct{})
		var waited error
		go func() {
			defer close(done)
			state, waited = cmd.Process.Wait()
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			return "still running 2s after the client gave up"
		}
		if state == nil {
			return fmt.Sprintf("exit status unavailable (%v)", waited)
		}
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return fmt.Sprintf("killed by signal %s", ws.Signal())
	}
	return fmt.Sprintf("exited with code %d", state.ExitCode())
}

func TestConnectDiagnosticsNameChildExitCodeAndStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "echo startup-boom >&2; exit 7")
	_, err := connectStdioWithDiagnostics(ctx, cmd)
	if err == nil {
		t.Fatal("expected the handshake against an exiting child to fail")
	}
	for _, want := range []string{"exited with code 7", "startup-boom", "elapsed before failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure message lacks %q:\n%v", want, err)
		}
	}
}
