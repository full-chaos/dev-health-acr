//go:build darwin || linux

package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// browserOpenerName is the opener production selects for this platform.
func browserOpenerName() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// The opener launches an arbitrary user-configured browser, so two things must
// hold at the exec boundary: the binary comes from trusted resolution and never
// from PATH, and the child sees an allowlisted environment rather than this
// process's own -- which carries the ACR API bearer credential.
//
// No host browser is started here: the resolver seam is injected to point at a
// recording shell script, and the PATH entry planted alongside it is a tripwire
// that must never run.
func TestOpenVerificationURIUsesTrustedResolutionAndAMinimalEnvironment(t *testing.T) {
	// Given
	requireSh(t)
	home := t.TempDir()
	recorded := filepath.Join(home, "opener.record")
	tripwire := filepath.Join(home, "tripwire.record")
	trustedDirectory := t.TempDir()
	pathDirectory := t.TempDir()
	opener := filepath.Join(trustedDirectory, browserOpenerName())
	// argv and the environment are written to a temporary file and published by
	// rename, so a reader can never observe a half-written record. Reading as
	// soon as any newline appeared could return the argv line alone, and the
	// environment assertions below would then scan an empty list and pass
	// without having examined anything.
	if err := os.WriteFile(opener, []byte("#!/bin/sh\n{ printf '%s\\n' \"$@\"; env; } > \"$HOME/opener.tmp\"\nmv \"$HOME/opener.tmp\" \"$HOME/opener.record\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"open", "xdg-open"} {
		planted := filepath.Join(pathDirectory, name)
		if err := os.WriteFile(planted, []byte("#!/bin/sh\nprintf 'launched\\n' > \"$HOME/tripwire.record\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", pathDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ACR_API_TOKEN", validTestToken(61))
	t.Setenv("ACR_API_TOKEN_FILE", filepath.Join(home, "token"))
	injectExecutableResolver(t, browserOpenerName(), opener)
	uri := "https://acr.example.com/device?code=ABCDEFGH"

	// When
	if err := OpenVerificationURI(uri); err != nil {
		t.Fatalf("OpenVerificationURI: %v", err)
	}

	// Then
	contents := waitForOpenerRecord(t, recorded)
	lines := strings.Split(strings.TrimRight(contents, "\n"), "\n")
	if len(lines) == 0 || lines[0] != uri {
		t.Fatalf("opener argv = %q, want exactly the verification address", lines)
	}
	if len(lines) < 2 {
		t.Fatalf("opener record has %d line(s), want argv plus the child's environment; an empty environment scan proves nothing", len(lines))
	}
	sawHome := false
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "ACR_") {
			t.Fatalf("opener environment carried an ACR variable: %q", strings.SplitN(line, "=", 2)[0])
		}
		if strings.HasPrefix(line, "HOME=") {
			sawHome = true
		}
	}
	if !sawHome {
		t.Fatal("opener record contains no HOME entry, so the environment it captured is not the child's")
	}
	if _, err := os.Stat(tripwire); !os.IsNotExist(err) {
		t.Fatalf("a PATH-resolved opener was launched: %v", err)
	}
}

// waitForOpenerRecord blocks until the recording script has published its
// output by rename.
//
// Existence is the completion signal precisely because the fixture publishes
// atomically: the path either does not exist or holds the whole record. The
// previous version returned as soon as the file contained any newline, which
// could hand back the argv line before the environment had been appended --
// and the caller's environment assertions then examined nothing at all.
//
// A timeout here is a failure, never a silently accepted empty result: an
// unobserved launch would read as coverage while proving nothing.
func waitForOpenerRecord(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			return string(contents)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read browser opener record: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("browser opener fixture never recorded an invocation at %s", path)
	return ""
}

// A desktop opener is supposed to hand the address to an already-running
// browser and exit. One that instead blocks -- a handler waiting on a lock, an
// xdg-open that falls through to a foreground text browser, a hostile
// substitute -- used to live for the whole login session, because the only
// thing waiting on it was an unbounded background Wait that could never
// return. Nothing bounded it and nothing reaped its forked descendants.
//
// This test injects an opener that hangs and forks, and requires two things:
// the reap completes under a fixed deadline, and the descendant the opener
// forked is gone afterwards -- which only a process-group kill achieves,
// since signalling the immediate child alone leaves the fork running.
//
// No host browser is involved: the resolver seam points at a shell script.
func TestOpenVerificationURIReapsAHangingOpenerUnderAFixedDeadline(t *testing.T) {
	// Given
	requireSh(t)
	home := t.TempDir()
	descendantFile := filepath.Join(home, "descendant.pid")
	trustedDirectory := t.TempDir()
	opener := filepath.Join(trustedDirectory, browserOpenerName())
	// The opener forks a long-lived descendant and then blocks forever itself,
	// so a guard that only kills the immediate child leaves the fork behind.
	// The pid is published by rename, so the reader can never observe a
	// half-written record and mistake it for the fixture's completed output.
	script := "#!/bin/sh\nsleep 300 &\nprintf '%s\\n' \"$!\" > \"$HOME/descendant.tmp\"\nmv \"$HOME/descendant.tmp\" \"$HOME/descendant.pid\"\nwait\n"
	if err := os.WriteFile(opener, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	injectExecutableResolver(t, browserOpenerName(), opener)
	restoreDeadline := browserOpenerDeadline
	// Two seconds is orders of magnitude above the milliseconds a shell needs
	// to fork and publish its pid, and orders of magnitude below both the
	// opener's 300-second sleep and the assertion bound below, so neither the
	// fixture's startup nor a loaded machine decides this test's outcome.
	browserOpenerDeadline = 2 * time.Second
	t.Cleanup(func() { browserOpenerDeadline = restoreDeadline })
	reaped := make(chan struct{})
	browserOpenerReaped = func() { close(reaped) }
	t.Cleanup(func() { browserOpenerReaped = nil })

	// When
	if err := OpenVerificationURI("https://acr.example.com/device"); err != nil {
		t.Fatalf("OpenVerificationURI: %v", err)
	}

	// Then
	// Ten seconds is far below the opener's 300-second sleep and far above the
	// two-second deadline, so this can only pass if the bound actually fired: it is
	// the assertion, not a convenience timeout.
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("a hanging browser opener was never reaped; the launch deadline did not bound it")
	}
	descendant := waitForOpenerDescendantPID(t, descendantFile)
	t.Cleanup(func() { _ = syscall.Kill(descendant, syscall.SIGKILL) })
	if !waitForProcessGone(descendant) {
		t.Fatalf("the descendant the opener forked (pid %d) survived the reap; only the immediate child was signalled", descendant)
	}
}

// waitForOpenerDescendantPID reads the PID the fixture recorded, waiting for a
// complete line so a partially flushed write is never parsed as a PID.
func waitForOpenerDescendantPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(contents), "\n") {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(contents)))
			if convErr != nil {
				t.Fatalf("opener fixture recorded an unparsable descendant pid %q: %v", contents, convErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("opener fixture never recorded a forked descendant at %s", path)
	return 0
}

// waitForProcessGone polls signal 0 until the process no longer exists. A
// kill is asynchronous, so a single immediate check would report a survivor
// that is merely still being torn down.
func waitForProcessGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
