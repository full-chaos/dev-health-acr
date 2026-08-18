package contextfabric

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// requestIDLogAttrs (CHAOS-3888) returns the "request_id" slog attribute
// pair for ctx's observability request id, or nil when ctx carries none
// (any caller not reached through the HTTP request-id middleware, e.g. most
// existing unit tests) -- observability.RequestIDFromContext already
// existed; nothing in this package read it before this ticket. Spread into
// a log call via `attrs...` so a request-id-less ctx adds nothing to the
// line rather than logging an empty string.
func requestIDLogAttrs(ctx context.Context) []any {
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		return []any{"request_id", string(requestID)}
	}
	return nil
}

// SlogEngineTelemetry is the production EngineTelemetry: every counter
// becomes one structured log line, content-safe by construction (org_id
// plus counts/booleans only -- never question text, subject labels,
// canonical IDs, or result IDs, matching EngineTelemetry's own doc
// comment). It is the first production implementation of this interface;
// composition previously left EngineTelemetry nil.
//
// AC-3782-8 (the reuse rate and the saved model-call count are recorded):
// both are derived entirely from the RecordAnswerReuse log line's
// "outcome" field -- a log aggregation query counts outcome="hit" events
// for the saved-call count, and hits / total for the rate. The non-hit
// outcome values split WHY a call missed (authorization vs. evidence
// containment vs. no candidate at all), so a cratered rate is
// diagnosable from this one stream; see RecordAnswerReuse's doc comment
// on EngineTelemetry.
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
	// CHAOS-3888: request_id appended when ctx carries one (see
	// requestIDLogAttrs' own doc comment) -- observability.RequestIDFromContext
	// existed before this ticket but nothing in this package read it, so
	// this line and RecordAnswerReuse's below were not request-correlatable.
	args := append([]any{"org_id", principal.OrgID, "skipped_count", skipped}, requestIDLogAttrs(ctx)...)
	t.logger.WarnContext(ctx, "context fabric prior-subject receipts skipped", args...)
}

func (t SlogEngineTelemetry) RecordAnswerReuse(ctx context.Context, principal storage.Principal, outcome AnswerReuseOutcome) {
	args := append([]any{"org_id", principal.OrgID, "outcome", string(outcome)}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse outcome", args...)
}

// RecordSubjectlessTerminal logs at Info: the classification itself
// (empty_pool/authz_filtered_to_empty/ambiguous) is diagnostic detail about
// an already-ordinary outcome (no_match/clarification_required), never a
// sign anything is broken.
func (t SlogEngineTelemetry) RecordSubjectlessTerminal(ctx context.Context, principal storage.Principal, reason string) {
	args := append([]any{"org_id", principal.OrgID, "reason", reason}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric subjectless terminal", args...)
}

// RecordPriorSubjectReceiptSkipReason logs at Info: a per-reason breakdown
// of an already-reported RecordPriorSubjectReceiptsSkipped aggregate, not a
// new failure signal of its own.
func (t SlogEngineTelemetry) RecordPriorSubjectReceiptSkipReason(ctx context.Context, principal storage.Principal, reason string, count int) {
	if count <= 0 {
		return
	}
	args := append([]any{"org_id", principal.OrgID, "reason", reason, "count", count}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric prior-subject receipt skip reason", args...)
}

// RecordAnswerReuseServedRequestID logs at Info -- a mismatch is the
// ORDINARY case for a reuse hit (the whole point of reuse is serving an
// EARLIER call's answer for a later, differently-request-id'd one), never
// itself a sign anything is wrong, so this is diagnostic correlation
// detail, not a warning condition. request ids are opaque,
// randomly-generated operational identifiers (observability.RequestIDGenerator),
// never subject/question content, so both are safe to log directly, exactly
// like org_id already is throughout this file.
func (t SlogEngineTelemetry) RecordAnswerReuseServedRequestID(ctx context.Context, principal storage.Principal, servedRequestID string, requestIDMismatch bool) {
	args := append([]any{"org_id", principal.OrgID, "served_request_id", servedRequestID, "request_id_mismatch", requestIDMismatch}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric answer reuse served a stored result's own request id", args...)
}
