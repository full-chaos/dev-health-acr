package sidecar

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitCmdT runs `git -C dir <args...>` for test fixture setup and fails
// the test immediately on error, printing combined output for diagnosis.
func runGitCmdT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out.String())
	}
	return out.String()
}

// initTestRepo initializes a Git repository at dir with a deterministic
// local identity (repo-scoped config only, never touching global/user Git
// config) so commits succeed hermetically in CI and locally alike.
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	runGitCmdT(t, dir, "init", "-q", "-b", "main")
	runGitCmdT(t, dir, "config", "user.email", "acr-test@example.com")
	runGitCmdT(t, dir, "config", "user.name", "ACR Test")
	runGitCmdT(t, dir, "config", "commit.gpgsign", "false")
	runGitCmdT(t, dir, "config", "tag.gpgsign", "false")
}

// commitFile writes name/contents into dir, stages it, commits it, and
// returns the resulting commit SHA.
func commitFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmdT(t, dir, "add", name)
	runGitCmdT(t, dir, "commit", "-q", "-m", "add "+name)
	return strings.TrimSpace(runGitCmdT(t, dir, "rev-parse", "HEAD"))
}

// newTestRepo creates a fresh spaced-path scratch repo with one commit and
// returns its absolute directory.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initTestRepo(t, dir)
	commitFile(t, dir, "README.md", "# scratch\n")
	return dir
}

// newFakeGitBinary writes an executable shell script named "git" into a
// fresh temporary directory and injects that script's absolute path into
// the executable-resolver seam (currentExecutableResolver, exec_resolver.go)
// for the duration of the calling test, via injectExecutableResolver
// (exec_resolver_test.go). Every runGit/gitChangedFiles invocation goes
// through that seam, never a bare "git" argv0 left for exec.CommandContext
// to search PATH for on its own, so injecting the resolver directly --
// rather than prepending a directory to PATH -- exercises the exact
// production seam and proves it, rather than an incidental PATH-search
// side effect. It still drives real exec.Cmd/os.Pipe plumbing
// (backpressure, Kill, Wait) that a synthetic in-process reader cannot
// reproduce, since the resolver only chooses which binary runs, not how
// runGit/gitChangedFiles run it.
func newFakeGitBinary(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	contents := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	injectExecutableResolver(t, "git", path)
}
