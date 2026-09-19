package contextfabric

import (
	"context"
	"time"
)

// findingsRun drives one period-delta pass and returns the served facts.
func findingsRun(engine *Engine, frame QuestionFrame, subjects []SubjectRef, facts []CanonicalFact, asOf time.Time) []CanonicalFact {
	return engine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), frame, subjects, facts, asOf, true)
}

// findingsAsOf resolves a time context's as-of instant.
func findingsAsOf(tc TimeContext, wall time.Time) time.Time {
	asOf, _ := periodDeltaCurrentAsOf(tc, wall)
	return asOf
}
