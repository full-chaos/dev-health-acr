package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverWorkspace_Precedence proves explicit > mcp roots > cwd by
// pointing each lower-precedence source at a different repository than the
// higher-precedence source and asserting the higher one always wins.
func TestDiscoverWorkspace_Precedence(t *testing.T) {
	explicitRepo := newTestRepo(t)
	mcpRepo := newTestRepo(t)
	cwdRepo := newTestRepo(t)

	t.Run("explicit wins over mcp roots and cwd", func(t *testing.T) {
		info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
			ExplicitRepoPath: explicitRepo,
			MCPFileRoots:     []string{mcpRepo},
			WorkingDirectory: cwdRepo,
		})
		if err != nil {
			t.Fatalf("DiscoverWorkspace: %v", err)
		}
		assertSameGitRoot(t, info.GitRoot, explicitRepo)
		if info.RootSource != RootSourceExplicit {
			t.Fatalf("RootSource = %q, want explicit", info.RootSource)
		}
	})

	t.Run("mcp roots win over cwd when explicit is empty", func(t *testing.T) {
		info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
			MCPFileRoots:     []string{mcpRepo},
			WorkingDirectory: cwdRepo,
		})
		if err != nil {
			t.Fatalf("DiscoverWorkspace: %v", err)
		}
		assertSameGitRoot(t, info.GitRoot, mcpRepo)
		if info.RootSource != RootSourceMCPRoot {
			t.Fatalf("RootSource = %q, want mcp_root", info.RootSource)
		}
	})

	t.Run("cwd is used only when explicit and mcp roots are both empty", func(t *testing.T) {
		info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
			WorkingDirectory: cwdRepo,
		})
		if err != nil {
			t.Fatalf("DiscoverWorkspace: %v", err)
		}
		assertSameGitRoot(t, info.GitRoot, cwdRepo)
		if info.RootSource != RootSourceCWD {
			t.Fatalf("RootSource = %q, want cwd", info.RootSource)
		}
	})
}

func TestDiscoverWorkspace_MultipleMCPRootsSameRepoNotAmbiguous(t *testing.T) {
	repo := newTestRepo(t)
	sub := filepath.Join(repo, "sub dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		MCPFileRoots: []string{repo, sub},
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	assertSameGitRoot(t, info.GitRoot, repo)
}

func TestDiscoverWorkspace_MultipleMCPRootsDifferentReposIsAmbiguous(t *testing.T) {
	repoA := newTestRepo(t)
	repoB := newTestRepo(t)
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		MCPFileRoots: []string{repoA, repoB},
	})
	if !errors.Is(err, ErrAmbiguousWorkspaceRoot) {
		t.Fatalf("expected ErrAmbiguousWorkspaceRoot, got %v", err)
	}
}

func TestDiscoverWorkspace_OneValidOneInvalidMCPRootUsesTheValidOne(t *testing.T) {
	repo := newTestRepo(t)
	notGit := t.TempDir()
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		MCPFileRoots: []string{notGit, repo},
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	assertSameGitRoot(t, info.GitRoot, repo)
}

func TestDiscoverWorkspace_RejectsNonGitCWDWithoutExplicitRepository(t *testing.T) {
	notGit := t.TempDir()
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{WorkingDirectory: notGit})
	if !errors.Is(err, ErrNotGitRepository) {
		t.Fatalf("expected ErrNotGitRepository, got %v", err)
	}
}

func TestDiscoverWorkspace_RejectsNonGitExplicitRoot(t *testing.T) {
	notGit := t.TempDir()
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: notGit})
	if !errors.Is(err, ErrNotGitRepository) {
		t.Fatalf("expected ErrNotGitRepository, got %v", err)
	}
}

func TestDiscoverWorkspace_RejectsSymlinkRoot(t *testing.T) {
	repo := newTestRepo(t)
	outside := t.TempDir()
	link := filepath.Join(outside, "escape-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: link})
	if !errors.Is(err, ErrWorkspaceRootSymlink) {
		t.Fatalf("expected ErrWorkspaceRootSymlink, got %v", err)
	}
}

func TestDiscoverWorkspace_RejectsNonDirectoryRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: file})
	if !errors.Is(err, ErrWorkspaceRootNotDir) {
		t.Fatalf("expected ErrWorkspaceRootNotDir, got %v", err)
	}
}

func TestDiscoverWorkspace_RejectsNonFileRootScheme(t *testing.T) {
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		ExplicitRepoPath: "https://example.com/some/path",
	})
	if !errors.Is(err, ErrUnsupportedRootScheme) {
		t.Fatalf("expected ErrUnsupportedRootScheme, got %v", err)
	}
}

func TestDiscoverWorkspace_RejectsControlCharactersInRoot(t *testing.T) {
	repo := newTestRepo(t)
	malformed := repo + "/\x01evil"
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: malformed})
	if !errors.Is(err, ErrControlCharacters) {
		t.Fatalf("expected ErrControlCharacters, got %v", err)
	}
}

func TestDiscoverWorkspace_RejectsRelativeExplicitRoot(t *testing.T) {
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: "relative/path"})
	if !errors.Is(err, ErrInvalidWorkspaceRoot) {
		t.Fatalf("expected ErrInvalidWorkspaceRoot, got %v", err)
	}
}

func TestDiscoverWorkspace_MultipleRemotesWithOriginPicksOrigin(t *testing.T) {
	repo := newTestRepo(t)
	runGitCmdT(t, repo, "remote", "add", "upstream", "git@github.com:upstream-owner/upstream-repo.git")
	runGitCmdT(t, repo, "remote", "add", "origin", "https://github.com/origin-owner/origin-repo.git")

	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if info.Remote == nil || info.Remote.Slug() != "origin-owner/origin-repo" {
		t.Fatalf("expected origin remote to win, got %#v", info.Remote)
	}
}

func TestDiscoverWorkspace_MultipleRemotesNoOriginIsAmbiguous(t *testing.T) {
	repo := newTestRepo(t)
	runGitCmdT(t, repo, "remote", "add", "alpha", "git@github.com:a/a.git")
	runGitCmdT(t, repo, "remote", "add", "beta", "git@github.com:b/b.git")

	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if !errors.Is(err, ErrAmbiguousRemote) {
		t.Fatalf("expected ErrAmbiguousRemote, got %v", err)
	}
}

func TestDiscoverWorkspace_UnsupportedRemoteShapeRejected(t *testing.T) {
	repo := newTestRepo(t)
	runGitCmdT(t, repo, "remote", "add", "origin", "git://github.com/owner/repo.git")

	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if !errors.Is(err, ErrUnsupportedRemote) {
		t.Fatalf("expected ErrUnsupportedRemote, got %v", err)
	}
}

func assertSameGitRoot(t *testing.T, got, wantDir string) {
	t.Helper()
	want, err := filepath.EvalSymlinks(wantDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("GitRoot = %q, want %q", got, want)
	}
}
