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

func TestTrustedCodeGraphIndexAcceptsPrimaryGroupWritableManagedRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	repo, err := os.MkdirTemp(home, "acr-codegraph-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Chmod(repo, 0o775))
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	target, err := os.MkdirTemp(managed, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.True(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexRejectsGroupWritableRootWithoutManagedIndex(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	repo, err := os.MkdirTemp(home, "acr-codegraph-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Chmod(repo, 0o775))
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".codegraph"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".codegraph", "codegraph.db"), []byte("index"), 0o600))
	require.False(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexRejectsExtraACLForGroupWritableRoot(t *testing.T) {
	previous := codeGraphACLCheck
	codeGraphACLCheck = func(string) bool { return false }
	t.Cleanup(func() { codeGraphACLCheck = previous })
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	repo, err := os.MkdirTemp(home, "acr-codegraph-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Chmod(repo, 0o775))
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	target, err := os.MkdirTemp(managed, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.False(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexRejectsWorldWritableRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	repo, err := os.MkdirTemp(home, "acr-codegraph-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Chmod(repo, 0o777))
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	target, err := os.MkdirTemp(managed, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.False(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexRejectsManagedDatabaseSymlink(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	target, err := os.MkdirTemp(managed, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	outside := filepath.Join(target, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("index"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(target, "codegraph.db")))
	repo, err := os.MkdirTemp(home, "acr-codegraph-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.False(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexRejectsWritableManagedTarget(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	target, err := os.MkdirTemp(managed, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	require.NoError(t, os.Chmod(target, 0o775))
	require.NoError(t, os.WriteFile(filepath.Join(target, "codegraph.db"), []byte("index"), 0o600))
	repo, err := os.MkdirTemp(home, "acr-codegraph-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
	require.False(t, trustedCodeGraphIndex(repo))
}

func TestTrustedCodeGraphIndexRejectsNonDirectManagedTargets(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	managed := filepath.Join(home, ".omo", "codegraph", "projects")
	outside := t.TempDir()
	direct, err := os.MkdirTemp(managed, "acr-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(direct) })
	nested := filepath.Join(direct, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	for _, target := range []string{outside, nested} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			repo, createErr := os.MkdirTemp(home, "acr-codegraph-")
			require.NoError(t, createErr)
			t.Cleanup(func() { _ = os.RemoveAll(repo) })
			require.NoError(t, os.Symlink(target, filepath.Join(repo, ".codegraph")))
			require.False(t, trustedCodeGraphIndex(repo))
		})
	}
}

func TestTrustedCodeGraphGroupWritableMetadata(t *testing.T) {
	cases := []struct {
		name           string
		uid, gid, mode int
		want           bool
	}{
		{"primary group", os.Geteuid(), os.Getegid(), 0o775, true},
		{"foreign group", os.Geteuid(), os.Getegid() + 1, 0o775, false},
		{"wrong owner", os.Geteuid() + 1, os.Getegid(), 0o775, false},
		{"world writable", os.Geteuid(), os.Getegid(), 0o777, false},
		{"not group writable", os.Geteuid(), os.Getegid(), 0o755, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, trustedCodeGraphGroupWritableMetadata(tc.uid, tc.gid, os.FileMode(tc.mode), os.Geteuid(), os.Getegid()))
		})
	}
}
