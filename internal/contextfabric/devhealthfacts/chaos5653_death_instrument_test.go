package devhealthfacts_test

// CHAOS-5653: the shared ClickHouse container's own death is otherwise
// invisible to a caller (see chaos5270_shared_container_test.go). This file
// is the guard for the diagnostic instrument that file adds: it drives the
// log consumer and the death report with a FABRICATED death, never a real
// Docker container, so the guard runs anywhere this package's other tests
// run and proves the marker line and every required field show up without
// needing an actual container to die.

import (
	"fmt"
	"strings"
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
