package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunGit_CanceledContext exercises long_commands/cancellation directly
// against the small-output Git wrapper: an already-canceled context must
// abort promptly with context.Canceled rather than ever invoking the
// process.
func TestRunGit_CanceledContext(t *testing.T) {
	repo := newTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runGit(ctx, repo, "rev-parse", "--show-toplevel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestRunGit_RealRepoRoundTrip is a direct, real-Git sanity check on
// runGit itself (previously exercised only indirectly through
// DiscoverWorkspace), proving the small-output path returns exactly the
// trimmed stdout of a real, well-behaved invocation.
func TestRunGit_RealRepoRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	out, err := runGit(context.Background(), repo, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("runGit returned empty output for a real repository")
	}
}

// TestRunGit_NeverEOFOversizedStdoutKillsRatherThanHangs proves the
// stdout-bounding fix against a real child process, not just a synthetic
// in-process reader: a fake `git` that never stops writing to stdout must
// still cause runGit to return ErrGitOutputTooLarge promptly, because the
// process is killed once the bound is exceeded rather than waited on
// normally (which would otherwise hang forever against a child still
// blocked on a full pipe).
func TestRunGit_NeverEOFOversizedStdoutKillsRatherThanHangs(t *testing.T) {
	// newFakeGitBinary injects the fake script only into the resolver seam
	// (currentExecutableResolver), which newTestRepo's own git setup calls
	// (runGitCmdT) never consult, so ordering relative to newTestRepo no
	// longer matters for correctness -- kept first here only to match this
	// file's other tests.
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
chunk=$(head -c 65536 /dev/zero | tr '\0' 'x')
while true; do
  printf '%s' "$chunk"
done
`)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := runGit(context.Background(), repo, "status")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrGitOutputTooLarge) {
			t.Fatalf("expected ErrGitOutputTooLarge, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("runGit took too long against a never-EOF child: %s", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runGit did not return; the oversized child was likely waited on instead of killed")
	}
}

// TestRunGit_StderrIsBounded proves stderr capture is bounded independent
// of the stdout bound: a fake `git` that writes far more than
// maxGitStderrBytes to stderr and then exits non-zero must still produce
// an error whose text stays close to the bound, rather than growing with
// however much stderr the child chose to write.
func TestRunGit_StderrIsBounded(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
head -c 200000 /dev/zero | tr '\0' 'e' >&2
exit 1
	`)

	_, err := runGit(context.Background(), repo, "status")
	if err == nil {
		t.Fatal("expected an error from a non-zero-exit child")
	}
	if got := len(err.Error()); got > maxGitStderrBytes+256 {
		t.Fatalf("runGit error text was %d bytes; stderr capture was not bounded: %v", got, err)
	}
}

// TestRunGit_ChildExitNonZeroIsReportedCleanly proves a well-behaved
// child's ordinary non-zero exit (no oversized output, no adversarial
// stderr) still surfaces a clear, bounded error rather than hanging or
// panicking, confirming the kill-before-wait changes did not regress the
// common failure path.
func TestRunGit_ChildExitNonZeroIsReportedCleanly(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
echo "fatal: not a git repository" >&2
exit 128
	`)

	_, err := runGit(context.Background(), repo, "status")
	if err == nil {
		t.Fatal("expected an error from a non-zero-exit child")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected stderr text to be included in the error, got %v", err)
	}
}

// TestRunGit_DisablesCoreFsmonitor is the canary for the core.fsmonitor
// `-c` override: it proves runGit actually passes
// "-c" "core.fsmonitor=false" on every invocation, not just that the
// gitSafeConfigArgs variable exists. A fake `git` inspects its own argv
// for the exact override and fails loudly if it is missing, so this test
// would fail if a future edit to runGit ever dropped gitSafeConfigArgs
// from the constructed argv.
func TestRunGit_DisablesCoreFsmonitor(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
case "$*" in
  *"core.fsmonitor=false"*) printf ok ;;
  *) echo "MISSING core.fsmonitor=false override: $*" >&2; exit 1 ;;
esac
`)
	out, err := runGit(context.Background(), repo, "status")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %q", out)
	}
}

// TestRunGit_DisablesCoreHooksPath is the canary for the core.hooksPath
// `-c` override added alongside core.fsmonitor: it proves runGit passes
// "-c" "core.hooksPath=/dev/null" on every invocation, disabling any
// hook a repository's own .git/hooks (or a repository-local
// core.hooksPath override) could otherwise run for a read-only operation
// this package performs.
func TestRunGit_DisablesCoreHooksPath(t *testing.T) {
	repo := newTestRepo(t)
	newFakeGitBinary(t, `
case "$*" in
  *"core.hooksPath=/dev/null"*) printf ok ;;
  *) echo "MISSING core.hooksPath=/dev/null override: $*" >&2; exit 1 ;;
esac
`)
	out, err := runGit(context.Background(), repo, "status")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %q", out)
	}
}

// TestRunGit_StripsCredentialEnvironment proves runGit launches its child
// with credentialSafeEnviron(), not this process's own inherited
// environment: an ACR_-prefixed variable (including one shaped like the
// real credential, ACR_API_TOKEN) must never be visible to the resolved
// git binary, while an ordinary, non-ACR_ variable still passes through
// unchanged.
func TestRunGit_StripsCredentialEnvironment(t *testing.T) {
	repo := newTestRepo(t)
	t.Setenv("ACR_API_TOKEN", "acr_canary_should_never_reach_a_child_process")
	t.Setenv("ACR_GIT_ENV_TEST_NON_CREDENTIAL_MARKER", "also-stripped")
	t.Setenv("GIT_ENV_TEST_NON_ACR_MARKER", "still-here")
	newFakeGitBinary(t, `printf 'token=%s;marker=%s;plain=%s' "${ACR_API_TOKEN:-absent}" "${ACR_GIT_ENV_TEST_NON_CREDENTIAL_MARKER:-absent}" "${GIT_ENV_TEST_NON_ACR_MARKER:-absent}"`)

	out, err := runGit(context.Background(), repo, "status")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	want := "token=absent;marker=absent;plain=still-here"
	if out != want {
		t.Fatalf("git subprocess environment was not sanitized: got %q, want %q", out, want)
	}
}

// TestRunGit_IgnoresMaliciousAbsolutePATHEntry drives the full production
// pipeline (runGit -> currentExecutableResolver -> resolveTrustedExecutable),
// with no test-injected resolver, against a hostile absolute PATH entry
// and a real-credential-shaped ACR_API_TOKEN: the malicious "git" must
// never run, and runGit must still succeed against the real, trusted git
// binary the repository fixture itself was created with.
func TestRunGit_IgnoresMaliciousAbsolutePATHEntry(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("resolveTrustedExecutable's system directory allowlist is only defined for darwin and linux")
	}
	repo := newTestRepo(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	canary := filepath.Join(dir, "executed.canary")
	script := "#!/bin/sh\ntouch " + canary + "\nprintf %s \"$ACR_API_TOKEN\" >> " + canary + "\necho fake-output\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ACR_API_TOKEN", "acr_should_never_leak_to_a_fake_git_binary")

	out, err := runGit(context.Background(), repo, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("runGit unexpectedly failed against the real trusted git: %v", err)
	}
	if strings.Contains(out, "fake-output") {
		t.Fatalf("runGit returned output from the malicious PATH entry: %q", out)
	}
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatal("the malicious absolute PATH entry was executed by runGit")
	}
}
