//go:build darwin || linux

package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphRunner_ReapsProcessGroupAfterDecodeFailure(t *testing.T) {
	requireProcessGroupKill(t)

	for _, test := range []struct {
		name    string
		output  string
		wantErr error
	}{
		{name: "malformed", output: "printf '{not-json}'", wantErr: errCodeGraphDecode},
		{name: "oversized", output: `printf '\"'; head -c 1048577 /dev/zero | tr '\000' x; printf '\"'`, wantErr: ErrCodeGraphOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(t *testing.T) {
				t.Helper()
				directory := t.TempDir()
				shellPIDPath := filepath.Join(directory, "shell.pid")
				grandchildPIDPath := filepath.Join(directory, "grandchild.pid")
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; sleep 30' sh "+shellQuote(grandchildPIDPath)+" &\n"+
					"while [ ! -s "+shellQuote(grandchildPIDPath)+" ]; do :; done\n"+
					test.output+"\n"+
					"sh -c 'sleep 30' &")
				runner.Config.Timeout = 10 * time.Second

				started := time.Now()
				_, err := runner.Status(context.Background(), directory)
				elapsed := time.Since(started)

				require.Less(t, elapsed, 5*time.Second)
				require.ErrorIs(t, err, test.wantErr)
				require.NotErrorIs(t, err, context.DeadlineExceeded)
				assertCodeGraphProcessExited(t, shellPIDPath)
				assertCodeGraphProcessExited(t, grandchildPIDPath)
				assertCodeGraphProcessGroupExited(t, shellPIDPath)
			}

			for range 50 {
				run(t)
			}

			t.Run("concurrent", func(t *testing.T) {
				for range 20 {
					t.Run("cleanup", func(t *testing.T) {
						t.Parallel()
						run(t)
					})
				}
			})
		})
	}
}

func TestCodeGraphRunner_ReapsProcessGroupAfterCommandExit(t *testing.T) {
	requireProcessGroupKill(t)

	for _, test := range []struct {
		name    string
		output  string
		wantErr error
	}{
		{name: "successful", output: "printf '{}'", wantErr: nil},
		{name: "nonzero", output: "printf '{}'\nexit 7", wantErr: errCodeGraphMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(t *testing.T) {
				t.Helper()
				directory := t.TempDir()
				shellPIDPath := filepath.Join(directory, "shell.pid")
				grandchildPIDPath := filepath.Join(directory, "grandchild.pid")
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; exec sleep 30' sh "+shellQuote(grandchildPIDPath)+" </dev/null >/dev/null 2>&1 &\n"+
					"while [ ! -s "+shellQuote(grandchildPIDPath)+" ]; do :; done\n"+
					test.output)
				runner.Config.Timeout = 10 * time.Second

				_, err := runner.Status(t.Context(), directory)

				if test.wantErr == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, test.wantErr)
				}
				assertCodeGraphProcessExited(t, shellPIDPath)
				assertCodeGraphProcessExited(t, grandchildPIDPath)
				assertCodeGraphProcessGroupExited(t, shellPIDPath)
			}

			for range 50 {
				run(t)
			}

			t.Run("concurrent", func(t *testing.T) {
				for range 20 {
					t.Run("cleanup", func(t *testing.T) {
						t.Parallel()
						run(t)
					})
				}
			})
		})
	}
}

func assertCodeGraphProcessExited(t *testing.T, pidPath string) {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
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

func assertCodeGraphProcessGroupExited(t *testing.T, pidPath string) {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pgid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.ErrorIs(t, err, syscall.ESRCH)
}
