package sidecar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrustedCodeGraphIndexRejectsArbitrarySymlink(t *testing.T) {
	repo := t.TempDir()
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.False(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexAcceptsManagedSymlink(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	managedRoot := filepath.Join(home, ".omo", "codegraph", "projects")
	require.NoError(t, os.MkdirAll(managedRoot, 0o700))
	target, err := os.MkdirTemp(managedRoot, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	repo := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.True(t, trustedCodeGraphIndex(repo))
}
