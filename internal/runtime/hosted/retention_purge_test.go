package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-11952: the Ask Dev retention loop, without a database.

type fakeRetentionSweeper struct {
	mu       sync.Mutex
	sweep    contextfabric.RetentionSweep
	err      error
	probeErr error
	probes   int
	calls    int
	limits   []int
}

func (f *fakeRetentionSweeper) ProbeRetention(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return f.probeErr
}

func (f *fakeRetentionSweeper) SweepRetention(_ context.Context, _ time.Time, limit int) (contextfabric.RetentionSweep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.limits = append(f.limits, limit)
	return f.sweep, f.err
}

func TestStartRetentionPurgeLoop_runsAPassAtStartupWithTheBatch(t *testing.T) {
	sweeper := &fakeRetentionSweeper{}
	stop, err := startRetentionPurgeLoop(context.Background(), sweeper, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if sweeper.probes != 1 || sweeper.calls != 1 || sweeper.limits[0] != contextfabric.RetentionSweepBatch {
		t.Fatalf("startup probes = %d passes = %d limits %v, want one probe and one pass of %d", sweeper.probes, sweeper.calls, sweeper.limits, contextfabric.RetentionSweepBatch)
	}
}

// A refused grant probe fails startup before any pass runs (codex #1808 r1:
// an empty first pass runs no DELETE, so it proves nothing on its own).
func TestStartRetentionPurgeLoop_aRefusedProbeFailsStartupBeforeAnyPass(t *testing.T) {
	refused := errors.New("permission denied for table context_fabric_threads (SQLSTATE 42501)")
	sweeper := &fakeRetentionSweeper{probeErr: refused}
	if _, err := startRetentionPurgeLoop(context.Background(), sweeper, nil, nil); !errors.Is(err, refused) {
		t.Fatalf("startup with a refused probe = %v, want the refusal", err)
	}
	if sweeper.calls != 0 {
		t.Fatalf("passes after a refused probe = %d, want 0", sweeper.calls)
	}
}

// A refused first pass (a runtime role missing a DELETE grant on the backing
// set) fails startup.
func TestStartRetentionPurgeLoop_aFailedFirstPassFailsStartup(t *testing.T) {
	refused := errors.New("permission denied for table context_fabric_investigation_results (SQLSTATE 42501)")
	if _, err := startRetentionPurgeLoop(context.Background(), &fakeRetentionSweeper{err: refused}, nil, nil); !errors.Is(err, refused) {
		t.Fatalf("startup with a refused pass = %v, want the refusal", err)
	}
}

// The pass logs one line with the counts only on every pass: "removed" when
// it removed, erased or fenced anything, "empty" otherwise (a heartbeat, so a
// due scan that stops finding rows shows at Info).
func TestRetentionPurgeFunc_logsCountsOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	sweeper := &fakeRetentionSweeper{sweep: contextfabric.RetentionSweep{ThreadsRemoved: 2, ThreadsErased: 1, RecordsRemoved: 3, ResultsRemoved: 7, Fenced: 1}}
	n, err := retentionPurgeFunc(sweeper, logger)(context.Background(), time.Now(), 10)
	if err != nil || n != 6 {
		t.Fatalf("pass = %d %v, want 6 units", n, err)
	}
	line := buf.String()
	for _, want := range []string{`"msg":"` + RetentionPurgeLogMessage + `"`, `"outcome":"removed"`, `"threads_removed":2`, `"threads_erased":1`, `"records_removed":3`, `"results_removed":7`, `"fenced":1`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %s lacks %s", line, want)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("want one line, got %q", line)
	}
	buf.Reset()
	if _, err := retentionPurgeFunc(&fakeRetentionSweeper{}, logger)(context.Background(), time.Now(), 10); err != nil ||
		strings.Count(buf.String(), "\n") != 1 || !strings.Contains(buf.String(), `"outcome":"empty"`) || !strings.Contains(buf.String(), `"results_removed":0`) {
		t.Fatalf("an empty pass wrote %q (%v), want one heartbeat line with outcome empty", buf.String(), err)
	}
}

// CHAOS-12214: a pass that fails partway already committed some units (each
// is its own transaction, counted only after its commit). Its line says what
// it removed, at Warn, with outcome failed and the counts only; the error
// itself is the loop's own Warn line.
func TestRetentionPurgeFunc_aFailedPassLogsWhatItAlreadyRemoved(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	refused := errors.New("refused: private detail")
	partial := contextfabric.RetentionSweep{ThreadsRemoved: 1, ThreadsErased: 2, RecordsRemoved: 3, ResultsRemoved: 4, Fenced: 5}
	n, err := retentionPurgeFunc(&fakeRetentionSweeper{sweep: partial, err: refused}, logger)(context.Background(), time.Now(), 10)
	if !errors.Is(err, refused) || n != 6 {
		t.Fatalf("failed pass = %d %v, want the error and the 6 units it committed", n, err)
	}
	line := buf.String()
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("a failed pass wrote %q, want one line", line)
	}
	for _, want := range []string{`"level":"WARN"`, `"msg":"` + RetentionPurgeLogMessage + `"`, `"outcome":"failed"`, `"threads_removed":1`, `"threads_erased":2`, `"records_removed":3`, `"results_removed":4`, `"fenced":5`} {
		if !strings.Contains(line, want) {
			t.Fatalf("failed pass line %s lacks %s", line, want)
		}
	}
	if strings.Contains(line, "private detail") {
		t.Fatalf("failed pass line %s carries the error; counts only (the loop's Warn line reports the error)", line)
	}

	// A pass that fails before its first commit says so too, with zeros.
	buf.Reset()
	if _, err := retentionPurgeFunc(&fakeRetentionSweeper{err: refused}, logger)(context.Background(), time.Now(), 10); err == nil ||
		strings.Count(buf.String(), "\n") != 1 || !strings.Contains(buf.String(), `"outcome":"failed"`) || !strings.Contains(buf.String(), `"results_removed":0`) {
		t.Fatalf("a pass that failed before any commit wrote %q (%v), want one failed line with zero counts", buf.String(), err)
	}
}
