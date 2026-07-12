package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// injectExecutableResolver overrides currentExecutableResolver so `name`
// resolves to `path` for the duration of the calling test (any other name
// still falls through to the resolver that was active before this call),
// restoring the previous resolver via t.Cleanup. Tests use this instead of
// manipulating the process PATH, so they exercise the exact seam
// production code (runGit, gitChangedFiles, runKeyringCommand) consults,
// rather than merely proving something about PATH search semantics this
// package does not trust directly.
func injectExecutableResolver(t *testing.T, name, path string) {
	t.Helper()
	prev := currentExecutableResolver
	currentExecutableResolver = func(n string) (string, error) {
		if n == name {
			return path, nil
		}
		return prev(n)
	}
	t.Cleanup(func() { currentExecutableResolver = prev })
}

func TestResolveTrustedExecutableAcceptsRealAbsoluteBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test resolves a POSIX shell, not available on windows")
	}
	path, err := resolveTrustedExecutable("sh")
	if err != nil {
		t.Fatalf("resolveTrustedExecutable(sh): %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("resolved path is not absolute: %q", path)
	}
}

func TestResolveTrustedExecutableRejectsMissingBinary(t *testing.T) {
	if _, err := resolveTrustedExecutable("acr-test-definitely-not-a-real-binary-xyz"); !errors.Is(err, ErrUntrustedExecutable) {
		t.Fatalf("expected ErrUntrustedExecutable, got %v", err)
	}
}

// TestResolveTrustedExecutableRejectsRelativePATHEntry proves a PATH
// containing only a relative directory entry -- one shape a workspace-
// relative (rather than a trusted system) location would take -- can
// never cause resolution to trust or execute an executable file named
// "git" sitting there, even though that file really exists and is
// executable. Since resolveTrustedExecutable no longer consults PATH at
// all (see its doc comment), this also proves the fake binary is simply
// never reached, not merely rejected after being found.
func TestResolveTrustedExecutableRejectsRelativePATHEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH relative-entry semantics differ on windows")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relDir := "chaos2908-relative-bin-" + strconv.Itoa(os.Getpid())
	absDir := filepath.Join(cwd, relDir)
	if err := os.Mkdir(absDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(absDir) })
	fake := filepath.Join(absDir, "git")
	canary := filepath.Join(absDir, "executed.canary")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+canary+"\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", relDir)
	if path, err := resolveTrustedExecutable("git"); err == nil && path == fake {
		t.Fatalf("a relative PATH entry was trusted to resolve an executable: %q", path)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("the relative-PATH-entry fake binary was executed")
	}
}

// TestResolveTrustedExecutableIgnoresMaliciousAbsolutePATHEntry is the
// TDD lock for this package's core fix: an absolute, workspace-controlled
// PATH entry -- the exact shape the pre-fix exec.LookPath + filepath.IsAbs
// check trusted, since "absolute" was the only property it verified --
// must never be resolved or executed, and the ACR API bearer credential
// this process holds must never reach it even if it somehow ran. PATH is
// pointed at nothing but the malicious directory (no real system
// directory anywhere in it), so a passing result here cannot be
// explained by a lucky PATH ordering that happened to also contain a real
// git; resolution must come from resolveTrustedExecutable's own trusted
// directory allowlist alone, never from PATH.
func TestResolveTrustedExecutableIgnoresMaliciousAbsolutePATHEntry(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("resolveTrustedExecutable's system directory allowlist is only defined for darwin and linux")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	canary := filepath.Join(dir, "executed.canary")
	script := "#!/bin/sh\ntouch " + canary + "\nprintf %s \"$ACR_API_TOKEN\" >> " + canary + "\necho pwned\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ACR_API_TOKEN", "acr_should_never_reach_a_fake_git_binary_1234567890")

	path, err := resolveTrustedExecutable("git")
	if err == nil && path == fake {
		t.Fatalf("resolved the malicious absolute PATH entry: %q", path)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("the malicious absolute PATH entry was executed")
	}
}

// TestInjectExecutableResolverFallsThroughForOtherNames proves the test
// helper only overrides the named tool, so a test that fakes "git" does
// not accidentally also fake unrelated resolutions (e.g. the real "sh"
// credential_keyring_test.go's process-management tests depend on).
func TestInjectExecutableResolverFallsThroughForOtherNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test resolves a POSIX shell, not available on windows")
	}
	injectExecutableResolver(t, "git", "/nonexistent/fake-git")
	path, err := currentExecutableResolver("sh")
	if err != nil {
		t.Fatalf("currentExecutableResolver(sh): %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("resolved path is not absolute: %q", path)
	}
}
