package devhealthfacts_test

// CHAOS-5653: the shared ClickHouse container's own death is otherwise
// invisible to a caller (see chaos5270_shared_container_test.go). This file
// is the guard for the diagnostic instrument that file adds: it drives the
// log consumer and the death report with a FABRICATED death, never a real
// Docker container, so the guard runs anywhere this package's other tests
// run and proves the marker line and every required field show up without
// needing an actual container to die.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// errFakeDial stands in for a real dial error in guard tests that exercise
// reportClickHouseContainerDeath directly, without a real dying container.
var errFakeDial = errors.New("fake dial refused")

func TestDeathLogBufferKeepsOnlyItsBoundedTail(t *testing.T) {
	buf := newDeathLogBuffer(3)
	buf.Accept(testcontainers.Log{LogType: testcontainers.StdoutLog, Content: []byte("line-1\nline-2\n")})
	buf.Accept(testcontainers.Log{LogType: testcontainers.StderrLog, Content: []byte("line-3\nline-4\n")})

	got := buf.tail()
	want := []string{"line-2", "line-3", "line-4"}
	if len(got) != len(want) {
		t.Fatalf("tail() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tail()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDeathLogBufferDropsBlankLines(t *testing.T) {
	buf := newDeathLogBuffer(10)
	buf.Accept(testcontainers.Log{LogType: testcontainers.StdoutLog, Content: []byte("\n\nonly-real-line\n\n")})

	got := buf.tail()
	if len(got) != 1 || got[0] != "only-real-line" {
		t.Fatalf("tail() = %v, want [only-real-line]", got)
	}
}

// TestClickHouseContainerDeathReportCapturesFakeDeath is the CHAOS-5653
// guard: it calls reportClickHouseContainerDeath directly with a fabricated
// OOM-killed state, a fabricated log tail, and a fixed uptime/test-count,
// and asserts the emitted report carries the clickhouse_container_death
// marker plus every required field. It never starts a real container.
func TestClickHouseContainerDeathReportCapturesFakeDeath(t *testing.T) {
	buf := newDeathLogBuffer(200)
	buf.Accept(testcontainers.Log{LogType: testcontainers.StderrLog, Content: []byte("fake ClickHouse stderr line before death\n")})

	fakeState := &dockercontainer.State{
		OOMKilled:  true,
		ExitCode:   137,
		FinishedAt: "2026-09-12T00:00:00Z",
	}
	startedAt := time.Now().Add(-42 * time.Second)
	lastGoodAt := time.Date(2026, 9, 12, 0, 0, 1, 0, time.UTC)

	var captured strings.Builder
	logf := func(format string, args ...any) {
		captured.WriteString(fmt.Sprintf(format, args...))
	}

	reportClickHouseContainerDeath(logf, buf, fakeState, "fake-container-id", "127.0.0.1:9000", startedAt, 7, "fake_death_for_guard_test", "in_flight", lastGoodAt, nil, errFakeDial)

	out := captured.String()

	requiredSubstrings := []string{
		clickHouseDeathMarker,
		"trigger=\"fake_death_for_guard_test\"",
		"detected_at=\"in_flight\"",
		"last_good_at=\"2026-09-12T00:00:01Z\"",
		`addr="127.0.0.1:9000"`,
		`dial_err="fake dial refused"`,
		"oom_killed=true",
		"exit_code=137",
		"finished_at=\"2026-09-12T00:00:00Z\"",
		"test_count=7",
		"fake ClickHouse stderr line before death",
	}
	for _, want := range requiredSubstrings {
		if !strings.Contains(out, want) {
			t.Fatalf("death report missing %q; full report:\n%s", want, out)
		}
	}

	if !strings.HasPrefix(out, clickHouseDeathMarker+" ") {
		t.Fatalf("death report's first line must start with the marker; got:\n%s", out)
	}

	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, clickHouseDeathMarker) {
			t.Fatalf("every line of a death report must carry the marker so it's grep-able; offending line: %q\nfull report:\n%s", line, out)
		}
	}
}

// fakeStateProber is a containerStateProber test double that reports
// whatever running/error state the test configures -- a genuinely settable
// state, not a fixed one, since the signal under test must never be a
// cached flag.
type fakeStateProber struct {
	mu      sync.Mutex
	running bool
	err     error
}

func (f *fakeStateProber) State(context.Context) (*dockercontainer.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &dockercontainer.State{Running: f.running}, nil
}

func (f *fakeStateProber) GetContainerID() string { return "fake-container-id" }

// listenAndAccept starts a real TCP listener that accepts and immediately
// closes every connection -- the CONTROL's "genuinely alive" port, as
// opposed to a fabricated Running=true that the watcher's OWN dial probe
// would otherwise catch as a lie.
func listenAndAccept(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

// refusingAddr returns an address nothing is listening on: bind a real
// listener to claim a genuinely free port, then close it immediately --
// unlike a random guessed port, this can never collide with something
// else already listening, and a dial against a closed loopback listener
// reliably refuses rather than timing out.
func refusingAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// guardLog is a concurrency-safe sink watchClickHouseContainerDeath can log
// into from its own goroutine while a test reads it from another, after
// joining on done.
type guardLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (g *guardLog) logf(format string, args ...any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fmt.Fprintf(&g.b, format, args...)
}

func (g *guardLog) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.b.String()
}

// startGuardWatcher launches watchClickHouseContainerDeath against a fake
// prober, returning the log it writes into and the done channel the
// production teardown path itself joins on -- so a guard test exercises the
// exact same start/join shape startSharedClickHouseContainer/terminate use.
func startGuardWatcher(prober containerStateProber, addr string, stop <-chan struct{}) (log *guardLog, done chan struct{}) {
	log = &guardLog{}
	done = make(chan struct{})
	var once sync.Once
	go watchClickHouseContainerDeath(prober, addr, "guard-test", stop, done, newDeathLogBuffer(50), time.Now(), &once, func() int64 { return 3 }, log.logf)
	return log, done
}

// runWatcherForGuard drives watchClickHouseContainerDeath to completion (it
// always returns after either firing once or stop closing) and reports
// whether the death marker is present -- never merely whether anything was
// logged, since the watch_started/watch_stopped liveness lines are logged
// on every run regardless of outcome. A bounded wait means a guard that
// regresses into an infinite loop fails the test instead of hanging the
// suite.
func runWatcherForGuard(t *testing.T, prober containerStateProber, addr string, stop <-chan struct{}) (fired bool, report string) {
	t.Helper()
	log, done := startGuardWatcher(prober, addr, stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("watchClickHouseContainerDeath did not return within 10s -- it must always return once it fires or stop closes")
	}
	out := log.String()
	return strings.Contains(out, clickHouseDeathMarker), out
}

// TestWatchClickHouseContainerDeathFiresOnStateNotRunning proves the State
// signal is independently sufficient: a live docker inspect (never a
// cached flag) says the container is not running, on two consecutive
// ticks, with a genuinely open port (so the dial probe alone could never
// explain a fire).
func TestWatchClickHouseContainerDeathFiresOnStateNotRunning(t *testing.T) {
	prober := &fakeStateProber{running: false}
	addr := listenAndAccept(t) // port is fine; State says not-running regardless
	fired, report := runWatcherForGuard(t, prober, addr, make(chan struct{}))
	if !fired {
		t.Fatalf("watcher never fired on a container State reporting not-running")
	}
	if !strings.Contains(report, clickHouseDeathMarker) || !strings.Contains(report, `trigger="container_not_running"`) {
		t.Fatalf("report missing the marker or the container_not_running trigger; got:\n%s", report)
	}
}

// TestWatchClickHouseContainerDeathFiresOnPortRefused proves the TCP probe
// is independently sufficient: docker's State reports the container as
// Running (a live container whose ClickHouse process/port has died is
// exactly the shape a cached IsRunning flag could never catch), but the
// mapped port refuses every dial, on two consecutive ticks.
func TestWatchClickHouseContainerDeathFiresOnPortRefused(t *testing.T) {
	prober := &fakeStateProber{running: true}
	addr := refusingAddr(t)
	fired, report := runWatcherForGuard(t, prober, addr, make(chan struct{}))
	if !fired {
		t.Fatalf("watcher never fired on a refused ClickHouse port with a live-reporting container")
	}
	if !strings.Contains(report, clickHouseDeathMarker) || !strings.Contains(report, `trigger="clickhouse_port_refused"`) {
		t.Fatalf("report missing the marker or the clickhouse_port_refused trigger; got:\n%s", report)
	}
}

// TestWatchClickHouseContainerDeathControlNeverFiresOnALiveContainer is the
// DISCRIMINATING CONTROL every finding above needs: a container reporting
// Running=true with a genuinely open, accepting port must never fire, no
// matter how many ticks pass. Without this, a watcher that always fires
// (e.g. an inverted condition) would pass both tests above too.
func TestWatchClickHouseContainerDeathControlNeverFiresOnALiveContainer(t *testing.T) {
	prober := &fakeStateProber{running: true}
	addr := listenAndAccept(t)
	stop := make(chan struct{})
	// Long enough to cover several ticks (750ms cadence, 2-consecutive-bad
	// debounce) with margin, short enough to keep the suite fast.
	time.AfterFunc(3*time.Second, func() { close(stop) })
	fired, report := runWatcherForGuard(t, prober, addr, stop)
	if fired {
		t.Fatalf("CONTROL BROKEN: watcher fired on a live container with an open port; report:\n%s", report)
	}
}

// TestWatchClickHouseContainerDeathAlwaysFiresBeforeStopRegardlessOfTiming
// pins the "never exits without a verdict" invariant: a death must be
// reported whether it's caught by the in-flight debounce or only by the
// final synchronous probe stop triggers, at every timing shape that
// matters -- including the exact shape a real hosted death demonstrated
// (a package finishing and closing stop before the in-flight debounce
// could complete). Ticks are 150ms; 0ms/100ms both land before the first
// tick (a *time.Ticker never fires early, so this is deterministic, not
// a race), forcing the teardown path; 600ms/5s both land well after the
// in-flight debounce would already have fired at ~300ms.
func TestWatchClickHouseContainerDeathAlwaysFiresBeforeStopRegardlessOfTiming(t *testing.T) {
	cases := []struct {
		name           string
		stopDelay      time.Duration
		wantDetectedAt string
	}{
		{"stop immediately", 0, "teardown"},
		{"stop before the first tick", 100 * time.Millisecond, "teardown"},
		{"stop after the in-flight debounce already fired", 600 * time.Millisecond, "in_flight"},
		{"stop long after", 5 * time.Second, "in_flight"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prober := &fakeStateProber{running: false}
			addr := refusingAddr(t)
			stop := make(chan struct{})
			time.AfterFunc(c.stopDelay, func() { close(stop) })
			fired, report := runWatcherForGuard(t, prober, addr, stop)
			if !fired {
				t.Fatalf("watcher never fired with stop at %s -- a death must never be read as a clean shutdown regardless of teardown timing", c.stopDelay)
			}
			wantDetectedAt := fmt.Sprintf("detected_at=%q", c.wantDetectedAt)
			if !strings.Contains(report, wantDetectedAt) {
				t.Fatalf("report missing %s at stop delay %s; got:\n%s", wantDetectedAt, c.stopDelay, report)
			}
			if !strings.Contains(report, "last_good_at=") {
				t.Fatalf("report missing last_good_at; got:\n%s", report)
			}
		})
	}
}

// TestWatchClickHouseContainerDeathHealthyControlNeverFiresEvenAtTeardown is
// the companion DISCRIMINATING CONTROL for the table above: a genuinely
// healthy container, with stop closing mid-run, must never fire -- the new
// final-synchronous-probe-on-stop path must not itself become a
// false-positive source. Without this, a watcher that always fires on stop
// regardless of the probe result would pass every case above too.
func TestWatchClickHouseContainerDeathHealthyControlNeverFiresEvenAtTeardown(t *testing.T) {
	prober := &fakeStateProber{running: true}
	addr := listenAndAccept(t)
	stop := make(chan struct{})
	time.AfterFunc(600*time.Millisecond, func() { close(stop) })
	fired, report := runWatcherForGuard(t, prober, addr, stop)
	if fired {
		t.Fatalf("CONTROL BROKEN: watcher fired on a live container with an open port at teardown; report:\n%s", report)
	}
}

// TestWatchClickHouseContainerDeathReportsBeforeJoinReturns pins the
// production sequence terminate() itself relies on: start the watcher, kill
// both signals, close stop, then join by waiting on done exactly as
// terminate() does -- and the death marker plus the stopped verdict must
// already be in the log the instant that wait unblocks, with no sleep or
// poll needed. This is what guarantees a death detected at teardown can
// never be abandoned mid-report by a caller that proceeds to os.Exit right
// after joining.
func TestWatchClickHouseContainerDeathReportsBeforeJoinReturns(t *testing.T) {
	prober := &fakeStateProber{running: false}
	addr := refusingAddr(t)
	stop := make(chan struct{})
	log, done := startGuardWatcher(prober, addr, stop)
	close(stop)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("watcher did not join within 10s")
	}

	out := log.String()
	for _, want := range []string{
		clickHouseWatchStartedMarker,
		clickHouseDeathMarker,
		`detected_at="teardown"`,
		clickHouseWatchStoppedMarker,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q already present the instant join returns; got:\n%s", want, out)
		}
	}
}

// TestWatchClickHouseContainerDeathHealthyControlReportsCleanVerdict is the
// DISCRIMINATING CONTROL for the test above: a genuinely healthy container
// still emits both liveness lines (started, and stopped with a clean
// verdict) but never the death marker. Without this, a watcher that always
// stamped verdict as a death regardless of the probes would pass the test
// above too.
func TestWatchClickHouseContainerDeathHealthyControlReportsCleanVerdict(t *testing.T) {
	prober := &fakeStateProber{running: true}
	addr := listenAndAccept(t)
	stop := make(chan struct{})
	time.AfterFunc(300*time.Millisecond, func() { close(stop) })
	log, done := startGuardWatcher(prober, addr, stop)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("watcher did not join within 10s")
	}

	out := log.String()
	if strings.Contains(out, clickHouseDeathMarker) {
		t.Fatalf("CONTROL BROKEN: death marker present for a healthy container; got:\n%s", out)
	}
	for _, want := range []string{clickHouseWatchStartedMarker, `verdict="clean"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q for a healthy container; got:\n%s", want, out)
		}
	}
}

// TestClickHouseContainerDeathReportHandlesNoInspectState covers the case
// where docker inspect itself failed (state == nil) -- the report still
// emits with zero-value fields rather than panicking or silently skipping
// the dump.
func TestClickHouseContainerDeathReportHandlesNoInspectState(t *testing.T) {
	buf := newDeathLogBuffer(200)
	var captured strings.Builder
	logf := func(format string, args ...any) {
		captured.WriteString(fmt.Sprintf(format, args...))
	}

	reportClickHouseContainerDeath(logf, buf, nil, "", "", time.Now(), 0, "no_inspect_state", "teardown", time.Now(), errors.New("inspect failed"), nil)

	out := captured.String()
	for _, want := range []string{clickHouseDeathMarker, "oom_killed=false", "exit_code=0", `finished_at=""`} {
		if !strings.Contains(out, want) {
			t.Fatalf("death report with nil state missing %q; full report:\n%s", want, out)
		}
	}
}
