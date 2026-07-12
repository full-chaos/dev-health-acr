package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestBaselineCharacterization documents the pre-existing package surface:
// before this change, internal/sidecar exposed only credential handling
// (LoadCredential); there was no workspace discovery type or function, and
// no fallback behavior existed for resolving a Git root, remote, branch,
// SHA, or changed files from local state. DiscoverWorkspace and
// WorkspaceInfo are new; this test simply pins that the zero-value options
// against a real repository produce a fully populated, non-degraded result,
// so a future regression cannot silently reintroduce placeholder/empty
// fallback output.
func TestBaselineCharacterization(t *testing.T) {
	repo := newTestRepo(t)
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{WorkingDirectory: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if info.GitRoot == "" || info.CommitSHA == "" || info.Branch == "" {
		t.Fatalf("expected fully populated discovery result, got %#v", info)
	}
	if info.RootSource != RootSourceCWD {
		t.Fatalf("expected RootSourceCWD, got %q", info.RootSource)
	}
}

func TestDiscoverWorkspace_BasicFields(t *testing.T) {
	repo := newTestRepo(t)
	runGitCmdT(t, repo, "remote", "add", "origin", "https://github.com/full-chaos/dev-health-acr.git")

	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if info.RootSource != RootSourceExplicit {
		t.Fatalf("RootSource = %q, want explicit", info.RootSource)
	}
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.GitRoot != realRepo {
		t.Fatalf("GitRoot = %q, want %q", info.GitRoot, realRepo)
	}
	if info.Branch != "main" {
		t.Fatalf("Branch = %q, want main", info.Branch)
	}
	if info.Detached {
		t.Fatal("expected Detached=false on a normal branch checkout")
	}
	if len(info.CommitSHA) != 40 {
		t.Fatalf("CommitSHA = %q, want a 40-char SHA", info.CommitSHA)
	}
	if info.Remote == nil {
		t.Fatal("expected a resolved remote")
	}
	if info.Remote.Slug() != "full-chaos/dev-health-acr" {
		t.Fatalf("Remote.Slug() = %q, want full-chaos/dev-health-acr", info.Remote.Slug())
	}
	if info.ChangedFiles != nil {
		t.Fatalf("ChangedFiles should be nil when IncludeChangedFiles is false, got %v", info.ChangedFiles)
	}
}

func TestDiscoverWorkspace_NoRemote(t *testing.T) {
	repo := newTestRepo(t)
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if info.Remote != nil {
		t.Fatalf("expected nil Remote when no remote is configured, got %#v", info.Remote)
	}
}

func TestDiscoverWorkspace_DetachedHEAD(t *testing.T) {
	repo := newTestRepo(t)
	sha := commitFile(t, repo, "second.txt", "second\n")
	runGitCmdT(t, repo, "checkout", "-q", "--detach", sha)

	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if !info.Detached {
		t.Fatal("expected Detached=true after checkout --detach")
	}
	if info.Branch != "" {
		t.Fatalf("expected empty Branch on detached HEAD, got %q", info.Branch)
	}
	if info.CommitSHA != sha {
		t.Fatalf("CommitSHA = %q, want %q", info.CommitSHA, sha)
	}
}

func TestDiscoverWorkspace_NoCommitsYet(t *testing.T) {
	dir := t.TempDir()
	initTestRepo(t, dir)

	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: dir})
	if !errors.Is(err, ErrNoCommits) {
		t.Fatalf("expected ErrNoCommits, got %v", err)
	}
}

func TestDiscoverWorkspace_ChangedFiles_DirtyWorktree(t *testing.T) {
	repo := newTestRepo(t)

	// Staged addition.
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmdT(t, repo, "add", "staged.txt")

	// Unstaged modification of a tracked file.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# scratch\nmodified\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Untracked file, including one nested in a new directory.
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub dir", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		ExplicitRepoPath:    repo,
		IncludeChangedFiles: true,
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}

	want := []string{"README.md", "staged.txt", "sub dir/nested.txt", "untracked.txt"}
	if len(info.ChangedFiles) != len(want) {
		t.Fatalf("ChangedFiles = %v, want %v", info.ChangedFiles, want)
	}
	for i, w := range want {
		if info.ChangedFiles[i] != w {
			t.Fatalf("ChangedFiles[%d] = %q, want %q (full: %v)", i, info.ChangedFiles[i], w, info.ChangedFiles)
		}
	}
	if info.ChangedFilesTruncated {
		t.Fatal("did not expect truncation below the bound")
	}
	if !sortedStrings(info.ChangedFiles) {
		t.Fatalf("ChangedFiles must be deterministically sorted: %v", info.ChangedFiles)
	}
}

func TestDiscoverWorkspace_ChangedFiles_HardMaximum(t *testing.T) {
	repo := newTestRepo(t)
	const total = 12
	const max = 5
	for i := range total {
		name := "file" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		ExplicitRepoPath:    repo,
		IncludeChangedFiles: true,
		MaxChangedFiles:     max,
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if len(info.ChangedFiles) != max {
		t.Fatalf("len(ChangedFiles) = %d, want hard max %d", len(info.ChangedFiles), max)
	}
	if !info.ChangedFilesTruncated {
		t.Fatal("expected ChangedFilesTruncated=true when the true count exceeds the bound")
	}

	// Determinism: repeated calls against unchanged state return the exact
	// same bounded slice.
	info2, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		ExplicitRepoPath:    repo,
		IncludeChangedFiles: true,
		MaxChangedFiles:     max,
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace (second call): %v", err)
	}
	if len(info.ChangedFiles) != len(info2.ChangedFiles) {
		t.Fatalf("non-deterministic changed-file count: %v vs %v", info.ChangedFiles, info2.ChangedFiles)
	}
	for i := range info.ChangedFiles {
		if info.ChangedFiles[i] != info2.ChangedFiles[i] {
			t.Fatalf("non-deterministic changed-file order: %v vs %v", info.ChangedFiles, info2.ChangedFiles)
		}
	}
}

func TestDiscoverWorkspace_ChangedFiles_DefaultBound(t *testing.T) {
	repo := newTestRepo(t)
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		ExplicitRepoPath:    repo,
		IncludeChangedFiles: true,
		// MaxChangedFiles intentionally left at zero; must fall back to
		// DefaultMaxChangedFiles rather than zero (which would degenerate
		// to "always empty" or "always truncated").
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if info.ChangedFilesTruncated {
		t.Fatalf("did not expect truncation of a clean worktree against the default bound")
	}
	_ = DefaultMaxChangedFiles // sanity: constant is exported and used above
}

func sortedStrings(items []string) bool {
	for i := 1; i < len(items); i++ {
		if items[i-1] > items[i] {
			return false
		}
	}
	return true
}
