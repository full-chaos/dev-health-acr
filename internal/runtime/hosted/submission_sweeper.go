package hosted

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// submissionSweepInterval is how often the sweeper applies the submission
// reconcile rule (CHAOS-11912, plan §2.7.5). Every replica runs one; the
// statement is idempotent, so two replicas sweeping at once change each
// record once.
const submissionSweepInterval = 30 * time.Second

// submissionSweepTimeout bounds one sweep statement.
const submissionSweepTimeout = 10 * time.Second

// SubmissionSweepLogMessage is the sweeper's line: Info when a sweep changed
// records, Warn when it failed. A sweep that changed nothing writes nothing.
const SubmissionSweepLogMessage = "context fabric submission sweep"

// submissionSweeper completes running records whose reserved result was
// stored (a save whose completion was lost) and abandons running records
// past their expiry (a run that died with its pod).
type submissionSweeper struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func startSubmissionSweeper(store contextfabric.SubmissionStore, logger *slog.Logger, now func() time.Time, interval time.Duration) *submissionSweeper {
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	sweeper := &submissionSweeper{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(sweeper.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepSubmissionsOnce(ctx, store, logger, now)
			}
		}
	}()
	return sweeper
}

// sweepSubmissionsOnce runs one sweep and records it.
func sweepSubmissionsOnce(ctx context.Context, store contextfabric.SubmissionStore, logger *slog.Logger, now func() time.Time) contextfabric.SubmissionSweep {
	sweepCtx, cancel := context.WithTimeout(ctx, submissionSweepTimeout)
	defer cancel()
	sweep, err := store.SweepSubmissions(sweepCtx, now())
	switch {
	case err != nil && ctx.Err() != nil:
		// Shutting down: not a failure of the sweep.
	case err != nil:
		class := "store_error"
		if errors.Is(err, context.DeadlineExceeded) {
			class = "deadline_exceeded"
		}
		logger.WarnContext(ctx, SubmissionSweepLogMessage, "outcome", "failed", "error_class", class)
	case sweep.Completed > 0 || sweep.Abandoned > 0:
		logger.InfoContext(ctx, SubmissionSweepLogMessage, "outcome", "changed", "completed", sweep.Completed, "abandoned", sweep.Abandoned)
	}
	return sweep
}

// Close stops the sweeper and waits for an in-flight sweep, up to ctx.
func (s *submissionSweeper) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return errors.New("submission sweeper did not stop before the shutdown deadline")
	}
}
