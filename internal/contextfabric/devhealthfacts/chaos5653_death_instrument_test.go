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
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

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

	var captured strings.Builder
	logf := func(format string, args ...any) {
		captured.WriteString(fmt.Sprintf(format, args...))
	}

	reportClickHouseContainerDeath(logf, buf, fakeState, "fake-container-id", startedAt, 7, "fake_death_for_guard_test")

	out := captured.String()

	requiredSubstrings := []string{
		clickHouseDeathMarker,
		"trigger=\"fake_death_for_guard_test\"",
		"oom_killed=true",
		"exit_code=137",
		"finished_at=\"2026-09-12T00:00:00Z\"",
		"test_count=7",
		"fake ClickHouse stderr line before death",
		clickHouseDeathMarker + " host_free_m:",
		clickHouseDeathMarker + " container_stats:",
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

// runWatcherForGuard drives watchClickHouseContainerDeath to completion (it
// always returns after either firing once or stop closing), injecting its
// own logf so the guard can see exactly what a fire would have written --
// same "take the sink as a parameter" testability reportClickHouseContainerDeath
// itself already uses. A bounded wait means a guard that regresses into an
// infinite loop fails the test instead of hanging the suite.
func runWatcherForGuard(t *testing.T, prober containerStateProber, addr string, stop <-chan struct{}) (fired bool, report string) {
	t.Helper()
	buf := newDeathLogBuffer(50)
	var once sync.Once
	var mu sync.Mutex
	var captured strings.Builder
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		captured.WriteString(fmt.Sprintf(format, args...))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchClickHouseContainerDeath(prober, addr, stop, buf, time.Now(), &once, func() int64 { return 3 }, logf)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("watchClickHouseContainerDeath did not return within 10s -- it must always return once it fires or stop closes")
	}
	mu.Lock()
	defer mu.Unlock()
	return captured.Len() > 0, captured.String()
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

// TestWatchClickHouseContainerDeathHonoursTeardownStopChannel keeps the
// pre-existing exclusion intact: closing stop BEFORE the debounce completes
// must return without ever reporting, even though every probe would
// otherwise be bad -- an intentional teardown must never read as a death.
func TestWatchClickHouseContainerDeathHonoursTeardownStopChannel(t *testing.T) {
	prober := &fakeStateProber{running: false}
	addr := refusingAddr(t)
	stop := make(chan struct{})
	close(stop) // already closed: the watcher must return on its very first select
	fired, report := runWatcherForGuard(t, prober, addr, stop)
	if fired {
		t.Fatalf("watcher reported a death after stop was already closed; report:\n%s", report)
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

	reportClickHouseContainerDeath(logf, buf, nil, "", time.Now(), 0, "no_inspect_state")

	out := captured.String()
	for _, want := range []string{clickHouseDeathMarker, "oom_killed=false", "exit_code=0", `finished_at=""`} {
		if !strings.Contains(out, want) {
			t.Fatalf("death report with nil state missing %q; full report:\n%s", want, out)
		}
	}
}
