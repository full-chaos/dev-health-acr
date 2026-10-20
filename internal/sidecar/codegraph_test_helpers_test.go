package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// privateCodeGraphHome points HOME at a fresh owner-only directory for the
// test. The trust checks reject a home that is group- or other-writable or
// owned by another user, so a fixture under the runner's real HOME measures
// the runner (the self-hosted pool's HOME is group-writable), not the guard.
func privateCodeGraphHome(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := filepath.Join(parent, "home")
	require.NoError(t, os.Mkdir(home, 0o700))
	require.NoError(t, os.Chmod(home, 0o700))
	t.Setenv("HOME", home)
	return home
}

func managedCodeGraphFixtureRoot(t *testing.T, home string) string {
	t.Helper()
	managedRoot := filepath.Join(home, ".omo", "codegraph", "projects")
	if _, err := os.Stat(managedRoot); errors.Is(err, os.ErrNotExist) {
		require.NoError(t, os.MkdirAll(managedRoot, 0o700))
	} else {
		require.NoError(t, err)
	}
	return managedRoot
}
