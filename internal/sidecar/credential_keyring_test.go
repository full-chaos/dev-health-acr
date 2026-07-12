package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file exercises runKeyringCommand (credential_keyring.go) directly,
// via the real `sh` shell rather than a stub, so the process-management
// behavior itself -- not just the KeyringLookup seam credential_test.go
// stubs out -- is under test: bounded stdout, a subprocess that never
// closes stdout (or never exits at all), oversized output, secrets never
// appearing in an error, and a bounded stderr capture that cannot block
// command completion.

func requireSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this file drives a POSIX /bin/sh, not available on windows")
	}
}

// requireProcessGroupKill skips t on platforms where
// killKeyringProcessGroup cannot actually reach a backend's forked
// descendant: production only wires up real process-group support for
// darwin and linux (credential_keyring_procgroup_unix.go); every other
// platform falls back to killing cmd.Process alone
// (credential_keyring_procgroup_other.go), which never reaches a
// descendant a backend backgrounds after forking rather than
// exec-replacing itself.
func requireProcessGroupKill(t *testing.T) {
	t.Helper()
	requireSh(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("killKeyringProcessGroup only terminates a descendant's process group on darwin/linux; see credential_keyring_procgroup_other.go")
	}
}

// TestRunKeyringCommandCanaryOutputRoundTrips proves the replacement
// StdoutPipe/io.LimitReader read still returns a normal, small,
// legitimate lookup result unchanged -- the baseline every other test in
// this file is a deviation from.
func TestRunKeyringCommandCanaryOutputRoundTrips(t *testing.T) {
	requireSh(t)
	output, ok, err := runKeyringCommand(context.Background(), "sh", "-c", "printf %s canary-secret-output")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || output != "canary-secret-output" {
		t.Fatalf("unexpected result: output=%q ok=%v", output, ok)
	}
}

func TestRunKeyringCommandTreatsMissingBinaryAsUnavailable(t *testing.T) {
	_, ok, err := runKeyringCommand(context.Background(), "acr-test-definitely-not-a-real-binary-xyz")
	if err != nil {
		t.Fatalf("expected nil error for a missing binary, got %v", err)
	}
	if ok {
		t.Fatal("a missing binary was reported as available")
	}
}

// TestRunKeyringCommandUsesInjectedResolverForRealLookup proves
// runKeyringCommand's binary resolution goes through
// currentExecutableResolver (exec_resolver.go), not a name-as-argv0
// handed straight to exec.CommandContext: injecting a fake resolver for a
// name that does not exist on any real PATH must still let the lookup
// succeed against the injected script.
func TestRunKeyringCommandUsesInjectedResolverForRealLookup(t *testing.T) {
	requireSh(t)
	const fakeName = "acr-test-keyring-tool-injected-only"
	dir := t.TempDir()
	scriptPath := dir + "/" + fakeName
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf %s injected-secret\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	injectExecutableResolver(t, fakeName, scriptPath)
	output, ok, err := runKeyringCommand(context.Background(), fakeName)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || output != "injected-secret" {
		t.Fatalf("unexpected result: output=%q ok=%v", output, ok)
	}
}

// TestRunKeyringCommandTreatsUntrustedResolvedPathAsUnavailable proves a
// resolver that refuses to resolve a name (mirroring
// resolveTrustedExecutable rejecting a relative or missing path) is
// treated identically to a genuinely missing binary: ok=false, err=nil,
// never a hard failure.
func TestRunKeyringCommandTreatsUntrustedResolvedPathAsUnavailable(t *testing.T) {
	const fakeName = "acr-test-keyring-tool-untrusted"
	prev := currentExecutableResolver
	currentExecutableResolver = func(n string) (string, error) {
		if n == fakeName {
			return "", ErrUntrustedExecutable
		}
		return prev(n)
	}
	t.Cleanup(func() { currentExecutableResolver = prev })
	_, ok, err := runKeyringCommand(context.Background(), fakeName)
	if err != nil {
		t.Fatalf("expected nil error for an untrusted-resolution name, got %v", err)
	}
	if ok {
		t.Fatal("an untrusted-resolution name was reported as available")
	}
}

func TestRunKeyringCommandTreatsNonzeroExitAsUnavailable(t *testing.T) {
	requireSh(t)
	_, ok, err := runKeyringCommand(context.Background(), "sh", "-c", "exit 1")
	if err != nil {
		t.Fatalf("expected nil error for a nonzero exit, got %v", err)
	}
	if ok {
		t.Fatal("a nonzero exit was reported as available")
	}
}

// TestRunKeyringCommandRejectsOversizedOutputWithoutBlocking proves the
// bounded stdout read never waits for the child to close stdout (let
// alone exit): a backend that writes past maxKeyringOutputBytes and then
// hangs (via `sleep`, without ever closing its own stdout) is detected
// and its process killed immediately, well inside the test's watchdog
// deadline -- not left running until the keyring lookup's own timeout
// (which this test does not even configure on ctx) or the test binary's
// own timeout.
func TestRunKeyringCommandRejectsOversizedOutputWithoutBlocking(t *testing.T) {
	requireSh(t)
	oversizedCount := maxKeyringOutputBytes + 1000
	script := fmt.Sprintf("head -c %d /dev/zero; sleep 100", oversizedCount)

	done := make(chan struct {
		output string
		ok     bool
		err    error
	}, 1)
	start := time.Now()
	go func() {
		output, ok, err := runKeyringCommand(context.Background(), "sh", "-c", script)
		done <- struct {
			output string
			ok     bool
			err    error
		}{output, ok, err}
	}()

	select {
	case result := <-done:
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Fatalf("runKeyringCommand took %s to reject oversized output; expected an immediate kill, not a wait for the 100s sleep", elapsed)
		}
		if result.ok {
			t.Fatal("oversized keyring output was accepted")
		}
		if !errors.Is(result.err, errKeyringOutputTooLarge) {
			t.Fatalf("expected errKeyringOutputTooLarge, got %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runKeyringCommand blocked instead of killing the oversized, never-exiting backend process")
	}
}

// TestRunKeyringCommandDoesNotLeakOutputInOversizeError proves the "keep
// secrets out of errors" requirement specifically for the oversize path:
// even though the truncated read did capture up to maxKeyringOutputBytes+1
// bytes of a canary value embedded at the very start of the oversized
// stream before detecting the overflow, that captured data never reaches
// the returned error's text.
func TestRunKeyringCommandDoesNotLeakOutputInOversizeError(t *testing.T) {
	requireSh(t)
	const canary = "REALSECRETVALUE12345"
	oversizedCount := maxKeyringOutputBytes + 1000
	script := fmt.Sprintf("printf %%s %s; head -c %d /dev/zero; sleep 100", canary, oversizedCount)

	_, ok, err := runKeyringCommand(context.Background(), "sh", "-c", script)
	if ok {
		t.Fatal("oversized keyring output was accepted")
	}
	if err == nil {
		t.Fatal("expected an error for oversized output")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("oversize error leaked captured output: %v", err)
	}
}

// TestRunKeyringCommandRespectsContextTimeout proves the caller-supplied
// ctx deadline (credential.go's keyringLookupTimeout in production) still
// bounds wall-clock time for a backend that produces little or no output
// and simply hangs, independent of the byte-size ceiling this file adds.
//
// The script backgrounds its sleep with `&` and then blocks in the `wait`
// builtin rather than running `sleep 30` as sh's own last command: POSIX
// shells are free to exec-replace themselves into a lone last command
// (as macOS's /bin/sh does), which would leave only a single process for
// this test to kill and could hide a regression in how runKeyringCommand
// terminates a backend. Backgrounding forces every POSIX shell (macOS's
// /bin/sh included) to fork a genuine, separate descendant that inherits
// the stdout pipe -- the same shape a real dash `/bin/sh -c` invocation
// takes on Linux -- so this test exercises process-group termination
// (credential_keyring_procgroup_unix.go), not just single-process kill,
// on every platform it runs on.
func TestRunKeyringCommandRespectsContextTimeout(t *testing.T) {
	requireSh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, ok, err := runKeyringCommand(ctx, "sh", "-c", "sleep 30 & wait")
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("runKeyringCommand took %s; expected the 200ms context deadline to bound it", elapsed)
	}
	if ok {
		t.Fatal("a context-timed-out lookup was reported as available")
	}
	// A context-deadline kill surfaces through the same *exec.ExitError
	// classification as any other killed/nonzero-exit process (see
	// runKeyringCommand's doc comment): ok=false, err=nil. What matters
	// here is that it returned promptly, not which of the two
	// unavailable-reporting shapes it took.
	_ = err
}

// waitForMarker polls for path to exist, failing the test if it does not
// appear within timeout. Used to synchronize deterministically on a
// shell-side readiness signal instead of guessing a fixed sleep long
// enough for a backgrounded descendant to have started.
func waitForMarker(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for marker %s to appear", timeout, path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRunKeyringCommandKillsDescendantThatOutlivesTheImmediateChild proves
// runKeyringCommand's ctx-driven kill (see
// credential_keyring_procgroup_unix.go) actually terminates a descendant
// process the immediate child forked, rather than merely unblocking this
// function's own read while leaving that descendant to keep running as an
// orphan: TestRunKeyringCommandRespectsContextTimeout above proves prompt
// return, but prompt return alone would also happen if only stdout's pipe
// were force-closed (os/exec's own WaitDelay behavior) while the
// descendant survived untouched -- exactly the "no zombie process"
// requirement this test exists to close. This guarantee only holds on
// platforms where killKeyringProcessGroup reaches the whole process
// group; see requireProcessGroupKill.
//
// readyMarker and finishedMarker are passed as positional shell
// arguments ($1, $2) rather than interpolated into the script text, so a
// TMPDIR containing spaces (or any other shell metacharacter) cannot
// corrupt the command the backgrounded descendant runs.
//
// The script touches readyMarker immediately after forking the
// descendant, before blocking in `wait`, so this test can wait for that
// marker to deterministically confirm the descendant has actually
// started -- and is holding the inherited stdout pipe -- before
// cancelling ctx, rather than guessing a fixed delay is long enough for
// that fork to have already happened. The descendant then sleeps far
// longer than the cancellation this test triggers and touches
// finishedMarker only if left to run to completion; finishedMarker's
// continued absence well past that sleep duration is direct evidence the
// descendant was itself killed, not merely orphaned.
func TestRunKeyringCommandKillsDescendantThatOutlivesTheImmediateChild(t *testing.T) {
	requireProcessGroupKill(t)
	dir := t.TempDir()
	readyMarker := dir + "/descendant-ready"
	finishedMarker := dir + "/descendant-finished"
	const descendantSleep = 2 * time.Second
	script := fmt.Sprintf(`(touch "$1"; sleep %d; touch "$2") & wait`, int(descendantSleep.Seconds()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type lookupResult struct {
		output string
		ok     bool
		err    error
	}
	done := make(chan lookupResult, 1)
	go func() {
		output, ok, err := runKeyringCommand(ctx, "sh", "-c", script, "sh", readyMarker, finishedMarker)
		done <- lookupResult{output, ok, err}
	}()

	// Block until the descendant itself (not just the immediate `sh -c`
	// child) has actually started, so cancelling ctx below is guaranteed
	// to race a live descendant rather than possibly firing before the
	// fork even happened.
	waitForMarker(t, readyMarker, 5*time.Second)

	start := time.Now()
	cancel()
	var result lookupResult
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runKeyringCommand did not return after ctx cancellation")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("runKeyringCommand took %s to return after cancellation; expected a prompt kill", elapsed)
	}
	if result.ok {
		t.Fatal("a context-timed-out lookup was reported as available")
	}

	// Give a surviving descendant ample time past its own sleep to finish
	// and touch finishedMarker before asserting on finishedMarker's
	// absence.
	time.Sleep(descendantSleep + 500*time.Millisecond)
	if _, statErr := os.Stat(finishedMarker); statErr == nil {
		t.Fatal("descendant process outlived runKeyringCommand's context cancellation instead of being killed")
	}
}

// TestRunKeyringCommandStderrDoesNotBlockCompletion proves a backend that
// writes far more to stderr than maxKeyringStderrBytes still completes
// normally and quickly: the bounded stderr writer must never create
// backpressure against the child, regardless of how much stderr it
// produces.
func TestRunKeyringCommandStderrDoesNotBlockCompletion(t *testing.T) {
	requireSh(t)
	hugeStderrCount := maxKeyringStderrBytes * 50
	script := fmt.Sprintf("head -c %d /dev/zero 1>&2; printf %%s ok", hugeStderrCount)

	start := time.Now()
	output, ok, err := runKeyringCommand(context.Background(), "sh", "-c", script)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if !ok || output != "ok" {
		t.Fatalf("unexpected result: output=%q ok=%v", output, ok)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runKeyringCommand took %s with a large stderr writer; expected no backpressure", elapsed)
	}
}

// TestRunKeyringCommandStripsCredentialEnvironment proves runKeyringCommand
// launches its child with credentialSafeEnviron(), not this process's own
// inherited environment: an ACR_-prefixed variable (including one shaped
// like the real credential this lookup exists to eventually supply,
// ACR_API_TOKEN) must never be visible to the resolved keyring backend,
// while an ordinary, non-ACR_ variable still passes through unchanged.
func TestRunKeyringCommandStripsCredentialEnvironment(t *testing.T) {
	requireSh(t)
	t.Setenv("ACR_API_TOKEN", "acr_canary_should_never_reach_a_child_process")
	t.Setenv("ACR_KEYRING_ENV_TEST_NON_CREDENTIAL_MARKER", "also-stripped")
	t.Setenv("KEYRING_ENV_TEST_NON_ACR_MARKER", "still-here")

	script := `printf 'token=%s;marker=%s;plain=%s' "${ACR_API_TOKEN:-absent}" "${ACR_KEYRING_ENV_TEST_NON_CREDENTIAL_MARKER:-absent}" "${KEYRING_ENV_TEST_NON_ACR_MARKER:-absent}"`
	output, ok, err := runKeyringCommand(context.Background(), "sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the probe command to succeed")
	}
	want := "token=absent;marker=absent;plain=still-here"
	if output != want {
		t.Fatalf("keyring subprocess environment was not sanitized: got %q, want %q", output, want)
	}
}

func TestBoundedBufferDiscardsBeyondLimit(t *testing.T) {
	b := &boundedBuffer{limit: 8}
	n, err := b.Write([]byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("Write must report the full length consumed even when truncating, got n=%d", n)
	}
	if got := b.String(); got != "01234567" {
		t.Fatalf("expected truncation to the 8-byte limit, got %q", got)
	}
	// A second write past the limit must not grow the buffer further.
	if _, err := b.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "01234567" {
		t.Fatalf("expected no growth past the limit, got %q", got)
	}
}
