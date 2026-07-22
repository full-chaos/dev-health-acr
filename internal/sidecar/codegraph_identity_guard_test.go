package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagedCodeGraphGuard_discardsOutputWhenSymlinkTargetReplaced(t *testing.T) {
	repo, first, second := managedGuardFixture(t)
	guard, err := openCodeGraphDB(repo)
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

func TestManagedCodeGraphGuard_createsMissingManagedParent(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)

	// When
	repo, first, second := managedGuardFixture(t)

	// Then
	for _, path := range []string{repo, first, second} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, info.IsDir())
	}
}

func TestManagedCodeGraphGuard_discardsOutputWhenDatabaseReplaced(t *testing.T) {
	repo, target, _ := managedGuardFixture(t)
	guard, err := openCodeGraphDB(repo)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(target, "replacement"), []byte("replacement-index"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(target, "replacement"), filepath.Join(target, "codegraph.db")))
	require.False(t, guard.unchanged(repo))
	_, err = guard.file.Stat()
	require.Error(t, err)
}

func TestDirectCodeGraphGuard_discardsOutputWhenDatabaseReplaced(t *testing.T) {
	// Given
	repo := directGuardFixture(t)
	guard, err := openCodeGraphDB(repo)
	require.NoError(t, err)

	// When
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".codegraph", "replacement"), []byte("replacement-index"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(repo, ".codegraph", "replacement"), filepath.Join(repo, ".codegraph", "codegraph.db")))

	// Then
	require.False(t, guard.unchanged(repo))
	_, err = guard.file.Stat()
	require.Error(t, err)
}

func TestDirectCodeGraphGuard_discardsOutputWhenIndexDirectoryReplaced(t *testing.T) {
	// Given
	repo := directGuardFixture(t)
	guard, err := openCodeGraphDB(repo)
	require.NoError(t, err)

	// When
	oldIndex := filepath.Join(repo, "old-codegraph")
	require.NoError(t, os.Rename(filepath.Join(repo, ".codegraph"), oldIndex))
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".codegraph"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".codegraph", "codegraph.db"), []byte("replacement-index"), 0o600))

	// Then
	require.False(t, guard.unchanged(repo))
	_, err = guard.file.Stat()
	require.Error(t, err)
}

func TestDirectCodeGraphGuard_acceptsSecureDirectIndex(t *testing.T) {
	// Given
	repo := directGuardFixture(t)

	// When
	guard, err := openCodeGraphDB(repo)

	// Then
	require.NoError(t, err)
	require.True(t, guard.unchanged(repo))
	_, err = guard.file.Stat()
	require.Error(t, err)
}

func TestCodeGraphProvider_discardsValidLookingOutputAfterDirectDatabaseReplacement(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	started, release := blockedCodeGraphScript(t, provider)

	// When
	result := make(chan error, 1)
	go func() {
		_, err := provider.Capabilities(context.Background())
		result <- err
	}()
	waitForCodeGraphBlock(t, started)
	require.NoError(t, os.WriteFile(filepath.Join(workspace.GitRoot, ".codegraph", "replacement"), []byte("replacement-index"), 0o600))
	require.NoError(t, os.Rename(filepath.Join(workspace.GitRoot, ".codegraph", "replacement"), filepath.Join(workspace.GitRoot, ".codegraph", "codegraph.db")))
	require.NoError(t, os.WriteFile(release, nil, 0o600))

	// Then
	assertCodeGraphFailure(t, <-result, LocalIndexErrorMissing, LocalIndexFreshnessUnknown, "local_index_missing")
}

func TestCodeGraphProvider_discardsValidLookingOutputAfterDirectIndexReplacement(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	started, release := blockedCodeGraphScript(t, provider)

	// When
	result := make(chan error, 1)
	go func() {
		_, err := provider.Capabilities(context.Background())
		result <- err
	}()
	waitForCodeGraphBlock(t, started)
	oldIndex := filepath.Join(workspace.GitRoot, "old-codegraph")
	require.NoError(t, os.Rename(filepath.Join(workspace.GitRoot, ".codegraph"), oldIndex))
	require.NoError(t, os.Mkdir(filepath.Join(workspace.GitRoot, ".codegraph"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace.GitRoot, ".codegraph", "codegraph.db"), []byte("replacement-index"), 0o600))
	require.NoError(t, os.WriteFile(release, nil, 0o600))

	// Then
	assertCodeGraphFailure(t, <-result, LocalIndexErrorMissing, LocalIndexFreshnessUnknown, "local_index_missing")
}

func managedGuardFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	managed := managedCodeGraphFixtureRoot(t, home)
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

func directGuardFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".codegraph"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".codegraph", "codegraph.db"), []byte("index"), 0o600))
	return repo
}

func blockedCodeGraphScript(t *testing.T, provider *CodeGraphLocalIndexProvider) (string, string) {
	t.Helper()
	directory := t.TempDir()
	started := filepath.Join(directory, "started")
	release := filepath.Join(directory, "release")
	fixtureDirectory := filepath.Join(filepath.Dir(provider.runner.Config.Executable), "fixtures")
	body := "printf started > " + shellQuote(started) + "\nwhile [ ! -e " + shellQuote(release) + " ]; do :; done\n/bin/cat " + shellQuote(filepath.Join(fixtureDirectory, "status.json"))
	setCodeGraphProviderScript(t, provider, body)
	return started, release
}

func waitForCodeGraphBlock(t *testing.T, started string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("CodeGraph command did not reach the replacement barrier")
		case <-tick.C:
			if _, err := os.Stat(started); err == nil {
				return
			}
		}
	}
}
