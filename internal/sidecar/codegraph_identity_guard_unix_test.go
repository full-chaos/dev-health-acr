//go:build darwin || linux

package sidecar

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenTrustedCodeGraphDatabaseRejectsFIFOWithoutBlocking(t *testing.T) {
	// Given
	directory := t.TempDir()
	path := filepath.Join(directory, "codegraph.db")
	require.NoError(t, os.WriteFile(path, []byte("index"), 0o600))
	expected, err := os.Lstat(path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	// When
	done := make(chan error, 1)
	go func() {
		file, _, openErr := openTrustedCodeGraphDatabase(path, expected)
		if file != nil {
			_ = file.Close()
		}
		done <- openErr
	}()

	// Then
	select {
	case openErr := <-done:
		require.Error(t, openErr)
	case <-time.After(2 * time.Second):
		t.Fatal("CodeGraph database open blocked on a FIFO replacement")
	}
}
