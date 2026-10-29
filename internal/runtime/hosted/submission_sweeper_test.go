package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// sweepCountingStore answers SweepSubmissions with sweep (or err) and counts
// the calls. Only the sweep method is reached.
type sweepCountingStore struct {
	contextfabric.SubmissionStore
	calls atomic.Int64
	sweep contextfabric.SubmissionSweep
	err   error
}

func (s *sweepCountingStore) SweepSubmissions(context.Context, time.Time) (contextfabric.SubmissionSweep, error) {
	s.calls.Add(1)
	return s.sweep, s.err
}

// CHAOS-11912: the sweeper sweeps on its interval, records a sweep that
// changed records at Info and a failed one at Warn, stays silent when nothing
// changed, and stops on Close.
func TestSubmissionSweeperSweepsRecordsAndStops(t *testing.T) {
	store := &sweepCountingStore{sweep: contextfabric.SubmissionSweep{Completed: 1, Abandoned: 2}}
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	sweeper := startSubmissionSweeper(store, logger, nil, 5*time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for store.calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the sweeper did not sweep within 10 s")
		}
		time.Sleep(time.Millisecond)
	}
	if err := sweeper.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped := store.calls.Load()
	time.Sleep(30 * time.Millisecond)
	if store.calls.Load() != stopped {
		t.Fatal("the sweeper swept after Close")
	}
	if !strings.Contains(logs.String(), `"msg":"context fabric submission sweep","outcome":"changed","completed":1,"abandoned":2`) {
		t.Fatalf("no changed line:\n%s", logs.String())
	}

	quiet := &bytes.Buffer{}
	sweepSubmissionsOnce(context.Background(), &sweepCountingStore{}, slog.New(slog.NewJSONHandler(quiet, nil)), time.Now)
	if quiet.Len() != 0 {
		t.Fatalf("a sweep that changed nothing wrote %s", quiet.String())
	}
	failed := &bytes.Buffer{}
	sweepSubmissionsOnce(context.Background(), &sweepCountingStore{err: errors.New("connection reset")}, slog.New(slog.NewJSONHandler(failed, nil)), time.Now)
	if !strings.Contains(failed.String(), `"level":"WARN"`) || !strings.Contains(failed.String(), `"error_class":"store_error"`) {
		t.Fatalf("a failed sweep wrote %s", failed.String())
	}
}
