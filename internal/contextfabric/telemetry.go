package contextfabric

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SlogEngineTelemetry is the production EngineTelemetry: every counter
// becomes one structured log line, content-safe by construction (org_id
// plus counts/booleans only -- never question text, subject labels,
// canonical IDs, or result IDs, matching EngineTelemetry's own doc
// comment). It is the first production implementation of this interface;
// composition previously left EngineTelemetry nil.
//
// AC-3782-8 (the reuse rate and the saved model-call count are recorded):
// both are derived entirely from the RecordAnswerReuse log line's
// "reused" field -- a log aggregation query counts reused=true events for
// the saved-call count, and reused=true / total for the rate. No separate
// counter or call is needed; see RecordAnswerReuse's doc comment on
// EngineTelemetry for why one boolean stream is sufficient.
type SlogEngineTelemetry struct {
	logger *slog.Logger
}

// NewSlogEngineTelemetry builds a SlogEngineTelemetry. A nil logger falls
// back to slog.Default(), matching observability.NewSlogSink's
// convention.
func NewSlogEngineTelemetry(logger *slog.Logger) SlogEngineTelemetry {
	if logger == nil {
		logger = slog.Default()
	}
	return SlogEngineTelemetry{logger: logger}
}

func (t SlogEngineTelemetry) RecordPriorSubjectReceiptsSkipped(ctx context.Context, principal storage.Principal, skipped int) {
	if skipped <= 0 {
		return
	}
	t.logger.WarnContext(ctx, "context fabric prior-subject receipts skipped", "org_id", principal.OrgID, "skipped_count", skipped)
}

func (t SlogEngineTelemetry) RecordAnswerReuse(ctx context.Context, principal storage.Principal, reused bool) {
	t.logger.InfoContext(ctx, "context fabric answer reuse outcome", "org_id", principal.OrgID, "reused", reused)
}
