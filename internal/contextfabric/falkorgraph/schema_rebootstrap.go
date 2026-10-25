package falkorgraph

import (
	"context"
	"time"
)

// A graph key purged by ANOTHER process (acr-projector rebuild run by exec)
// loses its indexes and constraints, but this process keeps
// bootstrapDone[key]=true, so ensureOrgGraph never runs bootstrapSchema again
// and every embed batch fails with errVectorIndexNotReady until a restart.
// embedProjectionBatch re-probes the index on every batch; when the probe says
// the vector index is ABSENT after bootstrap succeeded, the cache is dropped so
// the next tick bootstraps again (indexes and constraints are created
// idempotently, also on a non-empty graph).
//
// A repeat is throttled: the first absent event invalidates at once, later
// ones only after an exponentially growing wait, so a graph that cannot keep
// its schema does not turn every tick into a bootstrap. The state clears when
// a batch finds the index present.
const (
	schemaRebootstrapBackoffBase = 30 * time.Second
	schemaRebootstrapBackoffMax  = 5 * time.Minute
)

type schemaRebootstrap struct {
	attempts int
	nextAt   time.Time
}

// SchemaRebootstrapTelemetry is an optional extension of GraphTelemetry,
// found by type assertion so the required interface (and its implementers)
// stay unchanged. A Telemetry that does not implement it sees nothing.
type SchemaRebootstrapTelemetry interface {
	// RecordGraphSchemaRebootstrap fires when a vanished vector index made
	// this process drop its cached bootstrap; attempt counts the drops for
	// the graph key since the index was last seen.
	RecordGraphSchemaRebootstrap(ctx context.Context, orgID string, attempt int)
}

func (a *Adapter) recordGraphSchemaRebootstrap(ctx context.Context, orgID string, attempt int) {
	if a.config.Telemetry == nil {
		return
	}
	if sink, ok := a.config.Telemetry.(SchemaRebootstrapTelemetry); ok {
		sink.RecordGraphSchemaRebootstrap(ctx, orgID, attempt)
	}
}

// rebootstrapAfterAbsentIndex drops bootstrapDone[key] when the throttle
// allows it and reports whether it did.
func (a *Adapter) rebootstrapAfterAbsentIndex(ctx context.Context, key, orgID string) bool {
	now := a.now()
	a.rebootstrapMu.Lock()
	state := a.rebootstrap[key]
	if state == nil {
		state = &schemaRebootstrap{}
		a.rebootstrap[key] = state
	}
	if now.Before(state.nextAt) {
		a.rebootstrapMu.Unlock()
		return false
	}
	state.attempts++
	attempt := state.attempts
	wait := schemaRebootstrapBackoffBase << (attempt - 1)
	if wait > schemaRebootstrapBackoffMax || wait <= 0 {
		wait = schemaRebootstrapBackoffMax
	}
	state.nextAt = now.Add(wait)
	a.rebootstrapMu.Unlock()

	a.forgetBootstrap(key)
	a.recordGraphSchemaRebootstrap(ctx, orgID, attempt)
	return true
}

// vectorIndexSeen clears the re-bootstrap throttle for key.
func (a *Adapter) vectorIndexSeen(key string) {
	a.rebootstrapMu.Lock()
	delete(a.rebootstrap, key)
	a.rebootstrapMu.Unlock()
}
