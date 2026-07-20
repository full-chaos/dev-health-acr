package sidecar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedCodeGraphGuard_discardsOutputWhenSymlinkTargetReplaced(t *testing.T) {
	repo, first, second := managedGuardFixture(t)
	guard, err := openManagedCodeGraphDB(repo)
	require.NoError(t, err)
	_, err = guard.file.Stat()
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(repo, ".codegraph")))
	require.NoError(t, os.Symlink(second, filepath.Join(repo, ".codegraph")))
	require.False(t, guard.unchanged(repo))
	_, err = guard.file.Stat()
	require.Error(t, err)
	_ = first
}

func TestManagedCodeGraphGuard_discardsOutputWhenDatabaseReplaced(t *testing.T) {
	repo, target, _ := managedGuardFixture(t)
	guard, err := openManagedCodeGraphDB(repo)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(target, "replacement"), []byte("replacement-index"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(target, "replacement"), filepath.Join(target, "codegraph.db")))
	require.False(t, guard.unchanged(repo))
	_, err = guard.file.Stat()
	require.Error(t, err)
}

func managedGuardFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	first, err := os.MkdirTemp(managed, "acr-guard-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(first) })
	second, err := os.MkdirTemp(managed, "acr-guard-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(second) })
	for _, target := range []string{first, second} {
		require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	}
	repo, err := os.MkdirTemp(home, "acr-guard-repo-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Symlink(first, filepath.Join(repo, ".codegraph")))
	return repo, first, second
}
