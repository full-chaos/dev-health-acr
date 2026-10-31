package hosted

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// runTakeoverInterval is how often a replica takes over leased runs whose
// holder stopped renewing (CHAOS-11949, plan §2.7.9 "pod loss"). With the
// 15 s lease a lost run ends as run_lost at most about 20 s after its holder's
// last renewal; an attached client's own read takes it over sooner. Every
// replica runs one; the takeover is a conditional update of one row, so two
// replicas at once take each run once.
const runTakeoverInterval = 5 * time.Second

// runTakeoverTimeout bounds one takeover pass.
const runTakeoverTimeout = 10 * time.Second

// RunTakeoverLogMessage is the takeover's line, one per run taken over (the
// same message the API's attach writes when it takes over a run itself), and
// one Warn per pass that failed.
const RunTakeoverLogMessage = "context fabric investigation run taken over"

func runLostEnvelope(requestID string) contractsv1.ErrorEnvelope {
	return contractsv1.ContextFabricRunTerminalEnvelope(requestID, contractsv1.ErrorCodeRunLost)
}

func startRunTakeover(store contextfabric.RunStore, taker string, logger *slog.Logger, now func() time.Time, interval time.Duration) *submissionSweeper {
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
				takeOverLostRunsOnce(ctx, store, taker, logger, now)
			}
		}
	}()
	return sweeper
}

// takeOverLostRunsOnce runs one takeover pass and records it.
func takeOverLostRunsOnce(ctx context.Context, store contextfabric.RunStore, taker string, logger *slog.Logger, now func() time.Time) []contextfabric.LostRun {
	passCtx, cancel := context.WithTimeout(ctx, runTakeoverTimeout)
	defer cancel()
	lost, err := store.ReapLostRuns(passCtx, taker, nil, runLostEnvelope, now())
	for _, run := range lost {
		logger.WarnContext(ctx, RunTakeoverLogMessage,
			"org_id", contextfabric.SanitizeLogAttr(run.Run.OrgID),
			"request_id", contextfabric.SanitizeLogAttr(run.RequestID),
			"reserved_result_id", contextfabric.SanitizeLogAttr(run.Run.ReservedResultID),
			"previous_holder", contextfabric.SanitizeLogAttr(run.PreviousHolder),
			"taker", contextfabric.SanitizeLogAttr(taker),
			"lease_expired_at", run.LeaseExpiredAt.UTC().Format(time.RFC3339Nano),
			"terminal_seq", run.TerminalSeq, "terminal_appended", run.TerminalAppended)
	}
	if err != nil && ctx.Err() == nil {
		class := "store_error"
		if passCtx.Err() != nil {
			class = "deadline_exceeded"
		}
		logger.WarnContext(ctx, RunTakeoverLogMessage, "outcome", "failed", "error_class", class)
	}
	return lost
}
