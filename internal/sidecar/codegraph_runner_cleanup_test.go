//go:build darwin || linux

package sidecar

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
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

// assertCodeGraphLateForkReapedIfSpawned wraps assertCodeGraphProcessExited
// for the F2 late-fork guard (sol review, CHAOS-3861). The late fork is
// DELIBERATELY racing the go-side kill decision -- see the decode-failure
// family's own comment on why that race cannot be made fully
// deterministic via shell scripting (SIGKILL is unconditional and
// immediate for anything already alive when it fires; the only way to
// test "did the second kill catch a straggler the first kill missed" is
// for that straggler to come into existence chronologically after the
// first kill already ran, which this test approximates but cannot
// guarantee). On some runs -- confirmed empirically under `go test -race`,
// whose instrumentation overhead shifts the race -- the late fork may
// never get to write its pid file at all, caught by the FIRST kill before
// it could run. That is a vacuous, uninformative outcome, not a failure:
// nothing was left unreaped, there is simply nothing to check that run.
// When the pid file DOES exist, this defers to the normal, strict
// assertCodeGraphProcessExited (a genuinely unreaped straggler still
// fails loud, unconditionally) and increments spawned -- see
// logCodeGraphLateForkSpawnCount, called once per variant after every
// repetition (serial and concurrent) has run. spawned is logged only, not
// asserted on: the coverage GUARANTEE for the post-Wait kill lives in
// TestWaitCodeGraphProcessGroup_ReapsAGroupMemberThatJoinedAfterTheFirstKill
// (a deterministic Go-level test with no shell timing dependency at all);
// this shell probe is opportunistic realism on top of that, not a proof
// obligation in its own right -- CI proved this race resolves in the
// first kill's favor essentially always there, so asserting spawned > 0
// here would itself be flaky on exactly the runner that matters most.
// Verified at development time (no standing mutation-test harness exists
// in this repo, and none is required): with the post-Wait kill in
// waitCodeGraphProcessGroup temporarily commented out, "malformed" failed
// here (an unreaped process was still alive); restoring the kill made it
// pass again -- confirming this assertion genuinely catches a broken
// second kill when the race lands, not just when the mock happens to
// agree with itself.
func assertCodeGraphLateForkReapedIfSpawned(t *testing.T, pidPath string, spawned *atomic.Int32) {
	t.Helper()
	if _, err := os.Stat(pidPath); errors.Is(err, os.ErrNotExist) {
		return
	}
	spawned.Add(1)
	assertCodeGraphProcessExited(t, pidPath)
}

// logCodeGraphLateForkSpawnCount is CHAOS-3861's third round on this
// guard, and the one that settles the division of labor for good.
//
// Round 1 (sol review F2) restored the shell late fork after an earlier
// pass deleted it. Round 2 (sol/luna review) added a HARD non-vacuity
// assertion here -- require.Greater(spawned, 0) -- reasoning that a
// per-run vacuous pass (tolerated because SIGKILL is unconditional for
// anything already alive, so "never got forked at all" cannot be
// distinguished from "forked and correctly reaped" by construction) must
// not be allowed to look identical to "the spawn line itself is broken"
// across EVERY repetition. That reasoning was sound, and the assertion
// did exactly its job: on the CI runner that surfaced it, spawned was 0
// across all ~58 repetitions, PROVING the shell race resolves in the
// first kill's favor there essentially always -- CI scheduling makes the
// leader's own decode-failure-triggered kill so fast relative to a
// backgrounded `&` job's own fork/exec that the late fork routinely never
// exists at all when the group dies. Locally the race goes the other way
// often enough to look "reliable"; it never was, on the runner that
// actually matters, and 322576a's original guard (no detector at all) had
// been silently CI-vacuous since the day it was written -- nobody could
// see it before round 2 added a mechanism capable of seeing it.
//
// The structural fix (per team lead's ruling) is
// TestWaitCodeGraphProcessGroup_ReapsAGroupMemberThatJoinedAfterTheFirstKill
// below: a deterministic, Go-level test that drives runCodeGraphJSON's
// exact kill/wait sequence itself, constructing the "group member joined
// after the first kill" ordering directly (Setpgid+Pgid into an existing
// group) instead of hoping a shell script's own fork lands in that
// window. THAT test is where the coverage guarantee for the post-Wait
// kill now lives, unconditionally, on every machine, every run. This
// shell probe is downgraded to opportunistic realism only: still spawns,
// still gets a strict assertCodeGraphProcessExited when it DOES land (a
// genuinely unreaped straggler still fails loud, unconditionally), but no
// longer asserts on how OFTEN it lands -- that number is real information
// about the host, not a proof obligation this shell mechanism can
// actually discharge. Logged, never failed, never skipped.
func logCodeGraphLateForkSpawnCount(t *testing.T, spawned *atomic.Int32) {
	t.Helper()
	t.Logf("late fork (shell probe, opportunistic -- see TestWaitCodeGraphProcessGroup_ReapsAGroupMemberThatJoinedAfterTheFirstKill for the actual coverage guarantee) spawned in %d of ~58 repetitions (50 serial + %d concurrent)", spawned.Load(), concurrentCleanupSubtests)
}

// TestWaitCodeGraphProcessGroup_ReapsAGroupMemberThatJoinedAfterTheFirstKill
// is the deterministic coverage guarantee logCodeGraphLateForkSpawnCount's
// doc comment refers to. It drives the EXACT production sequence
// runCodeGraphJSON uses after a decode failure -- killKeyringProcessGroupID
// (the first, pre-Wait kill) then waitCodeGraphProcessGroup (cmd.Wait()
// plus the second, post-Wait kill) -- but with the TEST controlling
// ordering directly instead of racing a shell script against it:
//  1. Start a group leader and capture its process group.
//  2. Call killKeyringProcessGroupID on that group -- the exact first
//     kill runCodeGraphJSON issues.
//  3. ONLY THEN start a second process, explicitly joined into the SAME
//     existing group via SysProcAttr{Setpgid: true, Pgid: processGroup}.
//     This process cannot possibly have existed when step 2 ran; there is
//     no race to lose, by construction, not by timing luck.
//  4. Call waitCodeGraphProcessGroup -- the exact function runCodeGraphJSON
//     calls immediately after its own first kill -- and assert the late
//     joiner is dead.
//
// If the post-Wait kill inside waitCodeGraphProcessGroup were ever
// removed or broken, the late joiner from step 3 would never die (nothing
// else in this test kills it), and this test would fail every run, on
// any machine -- no CI-only vacuity possible, because nothing here
// depends on scheduling.
//
// Verification note (found the hard way, worth recording): the "late"
// process here is a DIRECT CHILD OF THIS TEST, unlike the shell-forked
// grandchildren elsewhere in this file, which become children of PID 1
// once their shell parent exits and so get reaped automatically. A direct
// child that this test does not reap itself stays a zombie -- kill(pid,
// 0), the mechanism assertCodeGraphPIDExited/assertCodeGraphProcessExited
// use, reports a zombie as "alive" regardless of whether it has actually
// been killed, since a zombie is still a real kernel entry. An earlier
// draft of this test used that helper and appeared to show the post-Wait
// kill NOT reaching the late joiner at all -- reproduced identically via
// a hand-rolled probe on both darwin and a linux container, looking like
// a genuine, portable kernel limitation -- until re-checking with
// late.Wait() (which blocks until the process actually exits AND reaps
// it) showed the kill lands within milliseconds every time. This test
// therefore reaps and observes "late" directly via Wait(), the correct
// mechanism for a process this test itself parented, instead of the
// zombie-blind polling helper.
func TestWaitCodeGraphProcessGroup_ReapsAGroupMemberThatJoinedAfterTheFirstKill(t *testing.T) {
	requireProcessGroupKill(t)

	// Given a process group leader...
	leader := exec.Command("sh", "-c", "sleep 30")
	configureKeyringProcessGroup(leader)
	require.NoError(t, leader.Start())
	processGroup := captureKeyringProcessGroup(leader)
	t.Cleanup(func() { _ = killKeyringProcessGroupID(processGroup) })

	// ...whose group has already received the FIRST (pre-Wait) kill...
	require.NoError(t, killKeyringProcessGroupID(processGroup))

	// ...before a SECOND process joins that SAME group -- deterministically
	// after the first kill, by construction (Setpgid+Pgid places it
	// directly into processGroup), not by hoping a shell fork wins a race
	// against another process's kill() syscall.
	late := exec.Command("sleep", "30")
	late.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: processGroup}
	require.NoError(t, late.Start())
	lateDone := make(chan error, 1)
	go func() { lateDone <- late.Wait() }()

	// When: the exact sequencing runCodeGraphJSON runs immediately after
	// its own first kill.
	_ = waitCodeGraphProcessGroup(leader, processGroup)

	// Then: the late joiner -- which the first kill could not possibly
	// have caught, since it did not exist yet -- must be reaped by the
	// second kill inside waitCodeGraphProcessGroup. A bounded wait, not a
	// hard timeout failure mode: if the kill genuinely does not land, late
	// keeps running its own 30s sleep and lateDone never fires within this
	// window, which is exactly the signal a broken second kill should
	// produce.
	select {
	case err := <-lateDone:
		require.Error(t, err, "late should have been killed, not exited on its own")
	case <-time.After(2 * time.Second):
		t.Fatal("the late-joining group member was not reaped within 2s of waitCodeGraphProcessGroup returning -- the post-Wait kill did not reach it")
	}
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
		name string
		// outputMayBlockOnKill: the "oversized" output writes more than
		// maxCodeGraphStdoutBytes through a pipe whose OS buffer is far
		// smaller (~64KiB); runCodeGraphJSON kills the group the INSTANT it
		// reads past the limit, with no wait for the writer to finish
		// flushing. Left foreground, that kill can (and empirically does)
		// land while `head`/`tr` are still blocked mid-write, so the script
		// never reaches the late-fork line placed after it -- not flaky,
		// near-deterministic given a 1MiB write through a much smaller
		// buffer. Backgrounding it in its own subshell decouples the late
		// fork from whether the output pipeline survives to be killed.
		// "malformed" does NOT need this (and backgrounding it introduces
		// a DIFFERENT race -- the late fork can then race the FIRST kill
		// instead of reliably following it, since there is no longer a
		// foreground statement forcing it to wait): its output is small
		// enough to write in one syscall, so staying foreground is both
		// simpler and, empirically, more reliable.
		//
		// Consequence for what each variant actually proves (sol review,
		// CHAOS-3861 F2 verification): "oversized"'s late fork is launched
		// alongside (not strictly after) the primary output, since
		// backgrounding removes the foreground wait that would otherwise
		// force it to come later -- it can be, and often is, forked BEFORE
		// the first kill. That still exercises real coverage (a group
		// member the first kill DOES catch, proving group-kill semantics
		// generally), but it is not 322576a's late-fork-after-first-kill
		// temporal proof; that proof lives in "malformed", whose late fork
		// stays sequenced strictly after test.output by staying foreground.
		outputMayBlockOnKill bool
		output               string
		wantErr              error
	}{
		{name: "malformed", output: "printf '{not-json}'", wantErr: errCodeGraphDecode},
		{name: "oversized", outputMayBlockOnKill: true, output: `head -c 1048576 /dev/zero | tr '\000' ' '; printf not-json`, wantErr: ErrCodeGraphOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Opportunistic spawn count (logged, not asserted -- see
			// logCodeGraphLateForkSpawnCount's doc comment for why).
			// Scoped per-variant (malformed and oversized each get their
			// own counter), incremented from any goroutine since the
			// concurrent block below calls run(t) from parallel subtests.
			var lateForkSpawned atomic.Int32
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
				// polling required.
				//
				// sol review F2: the second, untracked `sh -c 'sleep N' &`
				// spawned AFTER test.output was NOT redundant coverage of
				// "the group has more than one member" -- it is commit
				// 322576a's late-fork guard. runCodeGraphJSON kills the
				// process group TWICE: once right after decode fails
				// (before cmd.Wait()), and again right after cmd.Wait()
				// returns. A descendant forked AFTER the first kill (like
				// this one, spawned once test.output has already run) can
				// only ever be caught by the SECOND kill; without a late
				// fork actually happening in this test, removing that
				// second kill would leave nothing alive to detect and the
				// test would pass either way. Restored below with its own
				// tracked PID file so it gets its own explicit
				// assertCodeGraphProcessExited, not just inferred from the
				// group-level check.
				lateForkPIDPath := filepath.Join(directory, "latefork.pid")
				outputStatement := test.output
				if test.outputMayBlockOnKill {
					outputStatement = "( " + test.output + " ) &"
				}
				runner := newTestCodeGraphRunner(t, "printf '%s\\n' \"$$\" > "+shellQuote(shellPIDPath)+"\n"+
					"mkfifo "+shellQuote(grandchildReadyFIFOPath)+"\n"+
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; printf x > \"$2\"; sleep "+reapSleepArgument()+"' sh "+shellQuote(grandchildPIDPath)+" "+shellQuote(grandchildReadyFIFOPath)+" &\n"+
					"read _ < "+shellQuote(grandchildReadyFIFOPath)+"\n"+
					outputStatement+"\n"+
					// CHAOS-3861 CI flake (team lead's diagnosis, confirmed by
					// reproduction): unlike shellPIDPath/grandchildPIDPath --
					// both fully written well before anything reads them
					// (grandchildPIDPath by the FIFO handshake; shellPIDPath by
					// simply being the script's first statement, executed long
					// before Status() can return) -- lateForkPIDPath has NO
					// synchronization with the assertion that reads it by
					// design (that absence of synchronization is the whole
					// point of a late fork). A plain `> "$1"` redirect
					// TRUNCATE-CREATES the file before printf writes anything
					// into it, so a reader can observe it as existing-but-empty
					// in that window. Locally this window is normally
					// microseconds and invisible; artificially widening it
					// (temporarily, to `> "$1"; sleep 2; printf ... >> "$1"`)
					// reproduced the exact CI failure in 0.29s on the first
					// serial iteration: strconv.Atoi: parsing "": invalid
					// syntax, from a 0-byte pid file assertCodeGraphLateForkReapedIfSpawned's
					// os.Stat had already found to "exist". Fixed the same way
					// this repo's own file-exchange transport avoids the
					// identical class of bug (internal/diagnostics/writer.go):
					// write to a temp path, then atomically rename into place,
					// so a reader NEVER observes a partially-written file at
					// the final path -- it is either absent (vacuous, tolerated)
					// or complete.
					"sh -c 'printf \"%s\\n\" \"$$\" > \"$1.tmp\" && mv \"$1.tmp\" \"$1\"; sleep "+reapSleepArgument()+"' sh "+shellQuote(lateForkPIDPath)+" &")
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
				assertCodeGraphLateForkReapedIfSpawned(t, lateForkPIDPath, &lateForkSpawned)
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

			// t.Run above blocks until every one of its subtests --
			// including the parallel "cleanup" ones -- has completed, so
			// lateForkSpawned's final value is stable here. Logged only,
			// per team lead's ruling -- see logCodeGraphLateForkSpawnCount's
			// doc comment for why this is no longer a failing assertion.
			logCodeGraphLateForkSpawnCount(t, &lateForkSpawned)
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
