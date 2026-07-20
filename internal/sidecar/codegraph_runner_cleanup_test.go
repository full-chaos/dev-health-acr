package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphRunner_ReapsChildAfterDecodeFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		wantErr error
	}{
		{name: "malformed", output: "printf '{not-json}'", wantErr: errCodeGraphDecode},
		{name: "oversized", output: `printf '\"'; head -c 1048577 /dev/zero | tr '\000' x; printf '\"'`, wantErr: ErrCodeGraphOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			runner := newTestCodeGraphRunner(t, "printf '%s' \"$$\" > "+shellQuote(pidPath)+"\n"+test.output+"\nsleep 30")

			started := time.Now()
			_, err := runner.Status(context.Background(), t.TempDir())
			elapsed := time.Since(started)

			require.Less(t, elapsed, 1500*time.Millisecond)
			require.ErrorIs(t, err, test.wantErr)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
			assertCodeGraphChildExited(t, pidPath)
		})
	}
}

func assertCodeGraphChildExited(t *testing.T, pidPath string) {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(payload))
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.ErrorIs(t, err, syscall.ESRCH)
}
