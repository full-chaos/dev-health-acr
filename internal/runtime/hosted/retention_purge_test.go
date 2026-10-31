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
	mu     sync.Mutex
	sweep  contextfabric.RetentionSweep
	err    error
	calls  int
	limits []int
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
	if sweeper.calls != 1 || sweeper.limits[0] != contextfabric.RetentionSweepBatch {
		t.Fatalf("startup passes = %d limits %v, want one pass of %d", sweeper.calls, sweeper.limits, contextfabric.RetentionSweepBatch)
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

// The pass logs one line with the counts only, when it removed, erased or
// fenced anything, and nothing otherwise.
func TestRetentionPurgeFunc_logsCountsOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	sweeper := &fakeRetentionSweeper{sweep: contextfabric.RetentionSweep{ThreadsRemoved: 2, ThreadsErased: 1, RecordsRemoved: 3, ResultsRemoved: 7, Fenced: 1}}
	n, err := retentionPurgeFunc(sweeper, logger)(context.Background(), time.Now(), 10)
	if err != nil || n != 6 {
		t.Fatalf("pass = %d %v, want 6 units", n, err)
	}
	line := buf.String()
	for _, want := range []string{`"msg":"` + RetentionPurgeLogMessage + `"`, `"threads_removed":2`, `"threads_erased":1`, `"records_removed":3`, `"results_removed":7`, `"fenced":1`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %s lacks %s", line, want)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("want one line, got %q", line)
	}
	buf.Reset()
	if _, err := retentionPurgeFunc(&fakeRetentionSweeper{}, logger)(context.Background(), time.Now(), 10); err != nil || buf.Len() != 0 {
		t.Fatalf("an empty pass wrote %q (%v), want nothing", buf.String(), err)
	}
}
