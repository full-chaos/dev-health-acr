//go:build linux

package sidecar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	assertCodeGraphCleanupExited(t, func() (bool, error) {
		return codeGraphLinuxProcessExited(pid)
	})
}

func assertCodeGraphProcessGroupExited(t *testing.T, pidPath string) {
	t.Helper()
	pgid := codeGraphCleanupPID(t, pidPath)
	assertCodeGraphCleanupExited(t, func() (bool, error) {
		return codeGraphLinuxProcessGroupExited(pgid)
	})
}

func TestCodeGraphLinuxProcessGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "enoent", err: fmt.Errorf("read proc stat: %w", &os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.ENOENT}), want: true},
		{name: "esrch", err: fmt.Errorf("kill process: %w", &os.PathError{Op: "kill", Path: "1", Err: syscall.ESRCH}), want: true},
		{name: "eacces", err: fmt.Errorf("read proc stat: %w", &os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.EACCES}), want: false},
		{name: "eio", err: fmt.Errorf("read proc stat: %w", &os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.EIO}), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, codeGraphLinuxProcessGone(test.err))
		})
	}
}

func assertCodeGraphCleanupExited(t *testing.T, exited func() (bool, error)) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		complete, err := exited()
		require.NoError(t, err)
		if complete {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("CodeGraph process remained live after cleanup")
		case <-ticker.C:
		}
	}
}

func codeGraphLinuxProcessGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func codeGraphLinuxProcessExited(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if codeGraphLinuxProcessGone(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	state, _, err := codeGraphLinuxProcessState(pid)
	if codeGraphLinuxProcessGone(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return state == "Z", nil
}

func codeGraphLinuxProcessGroupExited(processGroup int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		state, group, err := codeGraphLinuxProcessState(pid)
		if codeGraphLinuxProcessGone(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if group == processGroup && state != "Z" {
			return false, nil
		}
	}
	return true, nil
}

func codeGraphLinuxProcessState(pid int) (string, int, error) {
	payload, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, err
	}
	separator := strings.LastIndex(string(payload), ")")
	if separator == -1 {
		return "", 0, fmt.Errorf("parse process %d stat: missing command separator", pid)
	}
	fields := strings.Fields(string(payload)[separator+1:])
	if len(fields) < 3 {
		return "", 0, fmt.Errorf("parse process %d stat: missing state or group", pid)
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, fmt.Errorf("parse process %d group: %w", pid, err)
	}
	return fields[0], group, nil
}

func codeGraphCleanupPID(t *testing.T, pidPath string) int {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	require.NoError(t, err)
	return pid
}
