package api

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// readinessState values shared by every transition cell (aggregate and
// per-check) a ReadinessTransitionLogger tracks.
const (
	readinessStateUnknown int32 = iota
	readinessStateReady
	readinessStateNotReady
)

func readinessStateOf(status string) int32 {
	if status == "ready" {
		return readinessStateReady
	}
	return readinessStateNotReady
}

func readinessStateLabel(state int32) string {
	switch state {
	case readinessStateReady:
		return "ready"
	case readinessStateNotReady:
		return "not_ready"
	default:
		return "unknown"
	}
}

// ReadinessCheckObservation is one named check's CURRENT status, as reported
// by a /readyz poll -- the shape both acr-api's handleReady and
// acr-projector's readinessHandler already build for their own JSON
// response, reused here so neither caller retypes it a second time.
type ReadinessCheckObservation struct {
	Name   string
	Status string // "ready" | "not_ready"
}

// ReadinessTransitionLogger logs one Info line the FIRST time it observes a
// readiness poll and again every time the AGGREGATE status actually changes
// -- not ready -> ready and back, never on a repeated poll at the same
// status -- naming every check's current state. It ALSO logs a separate
// Info line whenever any INDIVIDUAL check's own status changes, naming
// that check specifically: two checks can each flip on their own schedule
// while the aggregate stays "not_ready" throughout (one dependency already
// down, a second one failing afterward), and that second failure is
// otherwise invisible at Info -- the aggregate-only line never fires again
// once the aggregate is already not_ready.
//
// Shared by acr-api (internal/api.App) and acr-projector
// (cmd/acr-projector's readinessHandler) so both binaries' readiness
// telemetry is the SAME class, not two hand-rolled trackers that can drift.
type ReadinessTransitionLogger struct {
	aggregate atomic.Int32
	perCheck  sync.Map // check name (string) -> *atomic.Int32
}

// NewReadinessTransitionLogger returns a tracker whose aggregate and every
// per-check state starts at readinessStateUnknown, guaranteeing the FIRST
// observation of each is logged as a transition -- the pre-entry state is
// observable from the trace even when the very first probe already finds
// everything ready.
func NewReadinessTransitionLogger() *ReadinessTransitionLogger {
	return &ReadinessTransitionLogger{}
}

// Observe logs the aggregate transition and every per-check transition for
// one /readyz poll. ctx carries the request; requestID is threaded through
// explicitly (both callers already resolve it their own way -- acr-api via
// RequestID(ctx), acr-projector has no request-ID middleware at all -- so
// this stays a plain string parameter rather than assuming one context key
// shape).
func (t *ReadinessTransitionLogger) Observe(ctx context.Context, logger *slog.Logger, requestID string, aggregateStatus string, checks []ReadinessCheckObservation) {
	for _, check := range checks {
		cellAny, _ := t.perCheck.LoadOrStore(check.Name, new(atomic.Int32))
		cell := cellAny.(*atomic.Int32)
		next := readinessStateOf(check.Status)
		previous := cell.Swap(next)
		if previous == next {
			continue
		}
		logger.InfoContext(ctx, "readiness check state changed",
			"request_id", requestID,
			"check", check.Name,
			"previous_status", readinessStateLabel(previous),
			"status", check.Status,
		)
	}

	next := readinessStateOf(aggregateStatus)
	previous := t.aggregate.Swap(next)
	if previous == next {
		return
	}
	states := make([]any, 0, len(checks)*2)
	for _, check := range checks {
		states = append(states, check.Name, check.Status)
	}
	logger.InfoContext(ctx, "readiness state changed",
		append([]any{
			"request_id", requestID,
			"previous_status", readinessStateLabel(previous),
			"status", aggregateStatus,
		}, states...)...,
	)
}
