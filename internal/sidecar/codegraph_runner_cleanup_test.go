//go:build darwin || linux

package sidecar

import (
	"context"
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
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; sleep "+reapSleepArgument()+"' sh "+shellQuote(grandchildPIDPath)+" &\n"+
					"while [ ! -s "+shellQuote(grandchildPIDPath)+" ]; do sleep 0.01; done\n"+
					test.output+"\n"+
					"sh -c 'sleep "+reapSleepArgument()+"' &")
				runner.Config.Timeout = reapRunnerTimeout

				started := time.Now()
				_, err := runner.Status(context.Background(), directory)
				elapsed := time.Since(started)

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
				for range 20 {
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
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; exec sleep "+reapSleepArgument()+"' sh "+shellQuote(grandchildPIDPath)+" </dev/null >/dev/null 2>&1 &\n"+
					"while [ ! -s "+shellQuote(grandchildPIDPath)+" ]; do sleep 0.01; done\n"+
					test.output)
				// Same timeout as the sibling test above. This function has
				// no latency bound of its own, but its observed CI flake was
				// require.NoError failing on an unexpected error, which is
				// what a load-induced deadline looks like from here -- so the
				// extra headroom applies for the same reason.
				runner.Config.Timeout = reapRunnerTimeout

				_, err := runner.Status(t.Context(), directory)

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
				for range 20 {
					t.Run("cleanup", func(t *testing.T) {
						t.Parallel()
						run(t)
					})
				}
			})
		})
	}
}
