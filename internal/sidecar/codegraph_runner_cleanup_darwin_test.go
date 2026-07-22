//go:build darwin

package sidecar

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func assertCodeGraphProcessExited(t *testing.T, pidPath string) {
	t.Helper()
	pid := codeGraphCleanupPID(t, pidPath)
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		select {
		case <-deadline.C:
			require.ErrorIs(t, err, syscall.ESRCH)
			return
		case <-ticker.C:
		}
	}
}

func assertCodeGraphProcessGroupExited(t *testing.T, pidPath string) {
	t.Helper()
	pgid := codeGraphCleanupPID(t, pidPath)
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		select {
		case <-deadline.C:
			require.ErrorIs(t, err, syscall.ESRCH)
			return
		case <-ticker.C:
		}
	}
}

func codeGraphCleanupPID(t *testing.T, pidPath string) int {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	require.NoError(t, err)
	return pid
}
