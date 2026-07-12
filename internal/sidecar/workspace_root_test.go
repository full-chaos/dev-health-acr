package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestValidateRootPath_AcceptsPlainAbsoluteDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := validateRootPath(dir)
	if err != nil {
		t.Fatalf("validateRootPath: %v", err)
	}
	want := filepath.Clean(dir)
	if got != want {
		t.Fatalf("validateRootPath() = %q, want %q", got, want)
	}
}

func TestValidateRootPath_AcceptsFileURI(t *testing.T) {
	dir := t.TempDir()
	got, err := validateRootPath("file://" + dir)
	if err != nil {
		t.Fatalf("validateRootPath: %v", err)
	}
	want := filepath.Clean(dir)
	if got != want {
		t.Fatalf("validateRootPath() = %q, want %q", got, want)
	}
}

func TestValidateRootPath_RejectsNonFileScheme(t *testing.T) {
	_, err := validateRootPath("https://example.com/repo")
	if !errors.Is(err, ErrUnsupportedRootScheme) {
		t.Fatalf("expected ErrUnsupportedRootScheme, got %v", err)
	}
}

func TestValidateRootPath_RejectsRelativePath(t *testing.T) {
	_, err := validateRootPath("relative/dir")
	if !errors.Is(err, ErrInvalidWorkspaceRoot) {
		t.Fatalf("expected ErrInvalidWorkspaceRoot, got %v", err)
	}
}

func TestValidateRootPath_RejectsMissingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	_, err := validateRootPath(missing)
	if !errors.Is(err, ErrInvalidWorkspaceRoot) {
		t.Fatalf("expected ErrInvalidWorkspaceRoot, got %v", err)
	}
}

func TestValidateRootPath_RejectsSymlinkRoot(t *testing.T) {
	real := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(outside, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	_, err := validateRootPath(link)
	if !errors.Is(err, ErrWorkspaceRootSymlink) {
		t.Fatalf("expected ErrWorkspaceRootSymlink, got %v", err)
	}
}

func TestValidateRootPath_RejectsNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := validateRootPath(file)
	if !errors.Is(err, ErrWorkspaceRootNotDir) {
		t.Fatalf("expected ErrWorkspaceRootNotDir, got %v", err)
	}
}

func TestValidateRootPath_RejectsControlCharacters(t *testing.T) {
	_, err := validateRootPath("/tmp/evil\x07path")
	if !errors.Is(err, ErrControlCharacters) {
		t.Fatalf("expected ErrControlCharacters, got %v", err)
	}
}

// TestValidateRootPath_MacOSAncestorSymlinkIsNotAnEscape guards against a
// false positive: t.TempDir() on macOS lives under /var, itself a symlink
// to /private/var, but the literal root path supplied by the caller is not
// itself a symlink, so it must be accepted.
func TestValidateRootPath_MacOSAncestorSymlinkIsNotAnEscape(t *testing.T) {
	dir := t.TempDir()
	if _, err := validateRootPath(dir); err != nil {
		t.Fatalf("an ordinary t.TempDir() root must validate cleanly, got: %v", err)
	}
}

func TestResolveCandidateRoots_Precedence(t *testing.T) {
	t.Run("explicit takes precedence", func(t *testing.T) {
		candidates, err := resolveCandidateRoots(DiscoverOptions{
			ExplicitRepoPath: "/explicit",
			MCPFileRoots:     []string{"/mcp"},
			WorkingDirectory: "/cwd",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 || candidates[0].Path != "/explicit" || candidates[0].Source != RootSourceExplicit {
			t.Fatalf("unexpected candidates: %+v", candidates)
		}
	})

	t.Run("mcp roots take precedence over cwd", func(t *testing.T) {
		candidates, err := resolveCandidateRoots(DiscoverOptions{
			MCPFileRoots:     []string{"/mcp1", "/mcp2"},
			WorkingDirectory: "/cwd",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 2 {
			t.Fatalf("unexpected candidates: %+v", candidates)
		}
		for _, c := range candidates {
			if c.Source != RootSourceMCPRoot {
				t.Fatalf("unexpected source: %+v", c)
			}
		}
	})

	t.Run("cwd used only when both are empty", func(t *testing.T) {
		candidates, err := resolveCandidateRoots(DiscoverOptions{WorkingDirectory: "/cwd"})
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 || candidates[0].Path != "/cwd" || candidates[0].Source != RootSourceCWD {
			t.Fatalf("unexpected candidates: %+v", candidates)
		}
	})

	t.Run("blank mcp roots are filtered and empty result errors", func(t *testing.T) {
		_, err := resolveCandidateRoots(DiscoverOptions{MCPFileRoots: []string{"", "   "}})
		if !errors.Is(err, ErrNoWorkspaceRoot) {
			t.Fatalf("expected ErrNoWorkspaceRoot, got %v", err)
		}
	})
}

// TestResolveCandidateRoots_RejectsTooManyMCPRoots exercises the MCP root
// count bound (many_roots adversarial case): a caller supplying more than
// MaxMCPFileRoots roots fails closed with a typed error rather than
// performing unbounded per-root validation/Git-invocation work.
func TestResolveCandidateRoots_RejectsTooManyMCPRoots(t *testing.T) {
	roots := make([]string, MaxMCPFileRoots+1)
	for i := range roots {
		roots[i] = "/nonexistent-" + strconv.Itoa(i)
	}
	_, err := resolveCandidateRoots(DiscoverOptions{MCPFileRoots: roots})
	if !errors.Is(err, ErrTooManyWorkspaceRoots) {
		t.Fatalf("expected ErrTooManyWorkspaceRoots, got %v", err)
	}
}

func TestResolveCandidateRoots_AcceptsExactlyMaxMCPRoots(t *testing.T) {
	roots := make([]string, MaxMCPFileRoots)
	for i := range roots {
		roots[i] = "/nonexistent-" + strconv.Itoa(i)
	}
	candidates, err := resolveCandidateRoots(DiscoverOptions{MCPFileRoots: roots})
	if err != nil {
		t.Fatalf("resolveCandidateRoots: %v", err)
	}
	if len(candidates) != MaxMCPFileRoots {
		t.Fatalf("len(candidates) = %d, want %d", len(candidates), MaxMCPFileRoots)
	}
}
