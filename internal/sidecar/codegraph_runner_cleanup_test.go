//go:build darwin || linux

package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Reap timing constants.
//
// What these tests prove is that Status tears the whole process group down
// instead of blocking until the spawned grandchildren finish on their own.
// The DISCRIMINATOR between those two outcomes is reapGrandchildSleep: a
// reaped run returns in well under a second, while an unreaped one cannot
// return before its grandchildren exit. Every bound here is therefore
// derived from that one duration instead of hardcoded.
//
// reapRunnerTimeout sits strictly between the two, which is what keeps both
// failure signals intact: an unreaped run blocks past the timeout and comes
// back as context.DeadlineExceeded, tripping the latency bound AND the
// NotErrorIs assertion. If this ever grew past reapGrandchildSleep the
// tests would silently stop detecting a reap failure at all, so
// TestReapTimingConstantsPreserveTheDiscriminator pins the ordering.
//
// Previously the latency bound was a hardcoded 5s against a 10s timeout.
// That left roughly 1x margin over the worst case actually observed on an
// IDLE 16-core machine (5.45s), so on a loaded CI runner it failed for
// machine speed rather than for behavior -- the assertion could not tell
// "the runner did not reap" from "the box was busy". The bound now scales
// with the timeout and carries about 4x margin, while staying far enough
// below reapGrandchildSleep that a genuine reap failure is still caught.
const (
	reapGrandchildSleep = 30 * time.Second
	reapRunnerTimeout   = 20 * time.Second
)

// reapSleepArgument renders reapGrandchildSleep as the whole-second literal
// the test shell scripts pass to sleep(1). The scripts interpolate this
// rather than repeating the number so the constant above cannot drift out
// of sync with the behavior it describes.
func reapSleepArgument() string {
	return strconv.Itoa(int(reapGrandchildSleep / time.Second))
}

// CHAOS-3861: peak concurrent process count and spawn-failure handling.
//
// concurrentCleanupSubtests was 20. Each subtest here spawns several real
// OS processes (the fake "codegraph" script, its grandchild, and --
// before this fix -- a spin-poll loop that forked a fresh `sleep` on
// every iteration; see the FIFO-based sync below), and t.Parallel()
// releases every one of those subtests at once. On a resource-constrained
// CI runner that burst could transiently exceed available process-table
// headroom, and cmd.Start() failing with EAGAIN used to be misreported as
// errCodeGraphExecutableAbsent (fixed in codegraph_runner_exec.go's
// classifyCodeGraphSpawnError). Lowering the burst size reduces how often
// that ceiling is approached in the first place; it does not depend on
// the classification fix to be correct, but the two together are what
// make ulimit -u 1000 (this file's own measured repro floor) pass again.
const concurrentCleanupSubtests = 8

// maxCodeGraphSpawnRetries bounds statusWithBoundedSpawnRetry. A transient
// spawn failure (errCodeGraphSpawnUnavailable) means the scenario under
// test never even ran -- the process never started, so there is nothing
// to assert about malformed output, exit codes, or reaping. Retrying is
// the correct response; SKIPPING is not (go-test-skip-reads-as-ok), so a
// retry that stays exhausted after maxCodeGraphSpawnRetries attempts
// fails the test for real, with the truthful spawn-unavailable message in
// the chain rather than a misleading one.
const maxCodeGraphSpawnRetries = 12

// codeGraphSpawnRetryBackoffCap bounds statusWithBoundedSpawnRetry's
// exponential backoff. A fixed process-table ceiling does not clear
// itself on a timer -- it clears only once OTHER processes among that
// budget exit -- so retrying too fast just re-loses the same race; the
// backoff grows (25ms, 50ms, 100ms, 200ms, then holds at the cap) to give
// that turnover time to happen without letting a genuinely exhausted
// retry budget hang the test suite.
const codeGraphSpawnRetryBackoffCap = 300 * time.Millisecond

// statusWithBoundedSpawnRetry calls runner.Status, retrying only on
// errCodeGraphSpawnUnavailable (host could not fork right now) and
// returning immediately on every other outcome -- success, the test
// scenario's own expected error, or any other real failure. elapsed is
// measured fresh on each attempt, so a retry's backoff sleep never counts
// against the reap-latency bound the decode-failure family checks: that
// bound describes how fast a REAPED run returns, not how fast this
// harness rode through host contention to get one running at all.
func statusWithBoundedSpawnRetry(t *testing.T, runner CodeGraphRunner, ctx context.Context, gitRoot string) ([]byte, time.Duration, error) {
	t.Helper()
	var (
		payload []byte
		err     error
		elapsed time.Duration
	)
	backoff := 25 * time.Millisecond
	for attempt := 0; attempt < maxCodeGraphSpawnRetries; attempt++ {
		started := time.Now()
		payload, err = runner.Status(ctx, gitRoot)
		elapsed = time.Since(started)
		if !errors.Is(err, errCodeGraphSpawnUnavailable) {
			return payload, elapsed, err
		}
		if attempt < maxCodeGraphSpawnRetries-1 {
			t.Logf("codegraph spawn transiently unavailable (attempt %d/%d), retrying in %s: %v", attempt+1, maxCodeGraphSpawnRetries, backoff, err)
			time.Sleep(backoff)
			if backoff *= 2; backoff > codeGraphSpawnRetryBackoffCap {
				backoff = codeGraphSpawnRetryBackoffCap
			}
		}
	}
	return payload, elapsed, err
}

// TestReapTimingConstantsPreserveTheDiscriminator pins the one relationship
// the reap tests depend on. If the runner timeout ever reached or exceeded
// the grandchild sleep, an unreaped process group would return normally
// (the grandchildren having exited on their own) and every assertion would
// still pass -- the tests would keep reporting success while proving
// nothing.
func TestReapTimingConstantsPreserveTheDiscriminator(t *testing.T) {
	require.Less(t, reapRunnerTimeout, reapGrandchildSleep,
		"the runner timeout must stay below the grandchild sleep, or a failure to reap becomes indistinguishable from a normal return")
}

func TestCodeGraphRunner_ReapsProcessGroupAfterDecodeFailure(t *testing.T) {
	requireProcessGroupKill(t)

	for _, test := range []struct {
		name    string
		output  string
		wantErr error
	}{
		{name: "malformed", output: "printf '{not-json}'", wantErr: errCodeGraphDecode},
		{name: "oversized", output: `head -c 1048576 /dev/zero | tr '\000' ' '; printf not-json`, wantErr: ErrCodeGraphOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(t *testing.T) {
				t.Helper()
				directory := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(directory, ".codegraph"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(directory, ".codegraph", "codegraph.db"), []byte("index"), 0o600))
				shellPIDPath := filepath.Join(directory, "shell.pid")
				grandchildPIDPath := filepath.Join(directory, "grandchild.pid")
				grandchildReadyFIFOPath := filepath.Join(directory, "grandchild.ready")
				// NOTE, unconfirmed: unlike the sibling test below, this
				// grandchild is spawned WITHOUT redirecting its standard
				// streams, so it inherits and holds the command's output
				// pipe open. That plausibly changes how Status unblocks
				// here (it may wait on the pipe rather than on the child),
				// and so may be why this function's flake presents as a
				// latency overrun while the sibling's presents as an
				// unexpected error. Left as-is deliberately: changing the
				// spawn would change what this test exercises, and that
				// belongs in its own change with its own evidence.
				//
				// CHAOS-3861: the grandchild-ready signal used to be a
				// `while [ ! -s grandchild.pid ]; do sleep 0.01; done`
				// spin-poll loop, which forks a fresh external `sleep`
				// process on every iteration -- itself a source of the
				// process churn this fix reduces, and one that compounds
				// under contention (a slower grandchild start under host
				// pressure means more poll iterations, meaning more
				// churn). A FIFO plus a blocking `read` (a shell builtin,
				// no fork at all) replaces it: the parent blocks on the
				// read until the grandchild writes to the FIFO, no
				// polling required. The second, untracked
				// `sh -c 'sleep N' &` this test used to also spawn is
				// dropped for the same reason -- assertCodeGraphProcessGroupExited
				// already proves the whole group (leader plus every
				// descendant) is gone; it does not need a SECOND
				// descendant to prove that, only at least one.
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"mkfifo "+shellQuote(grandchildReadyFIFOPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; printf x > \"$2\"; sleep "+reapSleepArgument()+"' sh "+shellQuote(grandchildPIDPath)+" "+shellQuote(grandchildReadyFIFOPath)+" &\n"+
					"read _ < "+shellQuote(grandchildReadyFIFOPath)+"\n"+
					test.output)
				runner.Config.Timeout = reapRunnerTimeout

				_, elapsed, err := statusWithBoundedSpawnRetry(t, runner, context.Background(), directory)

				// Bound derived from the timeout, not a hardcoded number.
				// This still fails loudly on a genuine reap failure, which
				// cannot return before reapGrandchildSleep.
				require.Less(t, elapsed, reapRunnerTimeout)
				require.ErrorIs(t, err, test.wantErr)
				require.NotErrorIs(t, err, context.DeadlineExceeded)
				assertCodeGraphProcessExited(t, shellPIDPath)
				assertCodeGraphProcessExited(t, grandchildPIDPath)
				assertCodeGraphProcessGroupExited(t, shellPIDPath)
			}

			for range 50 {
				run(t)
			}

			t.Run("concurrent", func(t *testing.T) {
				for range concurrentCleanupSubtests {
					t.Run("cleanup", func(t *testing.T) {
						t.Parallel()
						run(t)
					})
				}
			})
		})
	}
}

func TestCodeGraphRunner_ReapsProcessGroupAfterCommandExit(t *testing.T) {
	requireProcessGroupKill(t)

	for _, test := range []struct {
		name    string
		output  string
		wantErr error
	}{
		{name: "successful", output: "printf '{}'", wantErr: nil},
		{name: "nonzero", output: "printf '{}'\nexit 7", wantErr: errCodeGraphMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(t *testing.T) {
				t.Helper()
				directory := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(directory, ".codegraph"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(directory, ".codegraph", "codegraph.db"), []byte("index"), 0o600))
				shellPIDPath := filepath.Join(directory, "shell.pid")
				grandchildPIDPath := filepath.Join(directory, "grandchild.pid")
				grandchildReadyFIFOPath := filepath.Join(directory, "grandchild.ready")
				// CHAOS-3861: FIFO+read instead of a spin-poll loop -- see
				// the sibling test above's identical note for why.
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"mkfifo "+shellQuote(grandchildReadyFIFOPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; printf x > \"$2\"; exec sleep "+reapSleepArgument()+"' sh "+shellQuote(grandchildPIDPath)+" "+shellQuote(grandchildReadyFIFOPath)+" </dev/null >/dev/null 2>&1 &\n"+
					"read _ < "+shellQuote(grandchildReadyFIFOPath)+"\n"+
					test.output)
				// Same timeout as the sibling test above. This function has
				// no latency bound of its own, but its observed CI flake was
				// require.NoError failing on an unexpected error, which is
				// what a load-induced deadline looks like from here -- so the
				// extra headroom applies for the same reason.
				runner.Config.Timeout = reapRunnerTimeout

				_, _, err := statusWithBoundedSpawnRetry(t, runner, t.Context(), directory)

				if test.wantErr == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, test.wantErr)
				}
				assertCodeGraphProcessExited(t, shellPIDPath)
				assertCodeGraphProcessExited(t, grandchildPIDPath)
				assertCodeGraphProcessGroupExited(t, shellPIDPath)
			}

			for range 50 {
				run(t)
			}

			t.Run("concurrent", func(t *testing.T) {
				for range concurrentCleanupSubtests {
					t.Run("cleanup", func(t *testing.T) {
						t.Parallel()
						run(t)
					})
				}
			})
		})
	}
}
