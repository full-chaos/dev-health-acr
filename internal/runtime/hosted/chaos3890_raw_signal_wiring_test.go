package hosted

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// fakeOverrideObserver is a minimal graphrank.RawSignalObserver double used
// only to prove identity (that defaultRawSignalObserver returns THIS exact
// value, not a wrapper around it).
type fakeOverrideObserver struct{}

func (fakeOverrideObserver) ObserveCandidate(context.Context, string, graphrank.CandidateNode) {}

// TestDefaultRawSignalObserver_NilOverrideFallsBackToSlogSink is the
// CHAOS-3890 wiring proof: request.options.RawSignalObserver is nil for
// every real deployment (its own doc comment), and that must no longer
// mean "RawSignalObserver stays nil forever" -- it must default to the
// production, debug-gated sink instead.
//
// Mutation check: reverting defaultRawSignalObserver to `return override`
// unconditionally makes this test fail (the result would stay nil).
func TestDefaultRawSignalObserver_NilOverrideFallsBackToSlogSink(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	observer := defaultRawSignalObserver(nil, logger)

	if observer == nil {
		t.Fatal("defaultRawSignalObserver(nil, logger) = nil, want the default SlogRawSignalObserver -- RawSignalObserver must not stay nil in production")
	}
	if _, ok := observer.(graphrank.SlogRawSignalObserver); !ok {
		t.Fatalf("defaultRawSignalObserver(nil, logger) = %T, want graphrank.SlogRawSignalObserver", observer)
	}
}

// TestDefaultRawSignalObserver_ExplicitOverrideStillWins is the
// CHAOS-3858 measurement-harness compatibility proof: an explicitly
// configured observer (the generative-trial harness's own
// trialRawSignalCollector) must keep taking priority, unchanged, exactly
// as it did before this ticket.
func TestDefaultRawSignalObserver_ExplicitOverrideStillWins(t *testing.T) {
	override := fakeOverrideObserver{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	observer := defaultRawSignalObserver(override, logger)

	if observer != override {
		t.Fatalf("defaultRawSignalObserver(override, logger) = %#v, want the exact override value unchanged", observer)
	}
}
