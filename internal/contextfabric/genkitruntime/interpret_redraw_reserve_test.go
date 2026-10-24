package genkitruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type slowRedrawGenerator struct {
	*redrawGenerator
	clock *fakeClock
	took  time.Duration
}

func (g slowRedrawGenerator) Interpret(ctx context.Context, req generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	g.clock.now = g.clock.now.Add(g.took)
	return g.redrawGenerator.Interpret(ctx, req)
}

func TestInterpretRedrawReserveValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		fallback bool
		slowest  time.Duration
		want     time.Duration
	}{
		{"fallback, no draw measured: draw + fallback leg + downstream", 45 * time.Second, true, 0, 65 * time.Second},
		{"fallback, 11s draw", 45 * time.Second, true, 11 * time.Second, 69 * time.Second},
		{"no fallback, capped by the old gate", 45 * time.Second, false, 11 * time.Second, 45 * time.Second},
		{"no fallback, below the old gate", 90 * time.Second, false, 0, 45 * time.Second},
		{"cap holds for a slow draw", 45 * time.Second, true, 90 * time.Second, 90 * time.Second},
	} {
		if got := interpretRedrawReserve(tc.timeout, tc.fallback, tc.slowest); got != tc.want {
			t.Errorf("%s: interpretRedrawReserve(%s, %v, %s) = %s, want %s", tc.name, tc.timeout, tc.fallback, tc.slowest, got, tc.want)
		}
	}
}

func TestInterpretRedrawReserveNeverExceedsTheOldGate(t *testing.T) {
	t.Parallel()
	for _, timeout := range []time.Duration{time.Second, 10 * time.Second, 45 * time.Second, 2 * time.Minute} {
		for _, fallback := range []bool{false, true} {
			for _, slowest := range []time.Duration{0, 8 * time.Second, 11 * time.Second, 90 * time.Second} {
				old := timeout
				if fallback {
					old *= 2
				}
				if got := interpretRedrawReserve(timeout, fallback, slowest); got > old || got <= 0 {
					t.Fatalf("reserve(%s, fallback=%v, slowest=%s) = %s, want in (0, %s]", timeout, fallback, slowest, got, old)
				}
			}
		}
	}
}

func interpretReserveRuntime(t *testing.T, draw time.Duration) (*Runtime, *redrawGenerator, *trackedErroringFallback) {
	t.Helper()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
	fallback := &trackedErroringFallback{err: errors.New("fallback unavailable"), receipt: validReceipt(contextfabric.ModelOperationInterpret)}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rt := mustRuntime(t, slowRedrawGenerator{gen, clock, draw}, Config{Timeout: 45 * time.Second, MaxAttempts: 1, MaxSynthesisResynthesisAttempts: 3, Fallback: fallback})
	rt.now = clock.read
	return rt, gen, fallback
}

// With a 45s timeout and a fallback the old gate wanted 90s. After a 11s
// invalid draw, 75s left covers a second draw, a fallback leg and the
// downstream steps (69s), so the valid second draw must be requested.
func TestInterpretRedrawRunsWhenTheTimeLeftCoversTheDrawAndWhatFollows(t *testing.T) {
	t.Parallel()
	rt, gen, fallback := interpretReserveRuntime(t, 11*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	served, _, err := contextfabric.RuntimeQuestionInterpreter{Runtime: rt}.Interpret(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v, want the second draw served", err)
	}
	if served.RequestedJudgment != validInterpretationOutput().RequestedJudgment || len(served.SubjectTerms) != 1 || served.SubjectTerms[0] != "Ask Dev" {
		t.Fatalf("served = %+v, want the valid second draw, not the fallback's interpretation", served)
	}
	if len(gen.seeds) != 2 || fallback.calls != 0 {
		t.Fatalf("draws = %d fallback.calls = %d, want 2 and 0", len(gen.seeds), fallback.calls)
	}
}

// 60s left cannot hold a redraw, a fallback leg and one synthesis draw: the
// invalid first draw goes to the fallback and the redraw does not eat the
// time synthesis needs.
func TestInterpretRedrawStopsWhenItWouldStarveWhatFollows(t *testing.T) {
	t.Parallel()
	rt, gen, fallback := interpretReserveRuntime(t, 11*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, _, err := (contextfabric.RuntimeQuestionInterpreter{Runtime: rt}).Interpret(ctx, storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("Interpret() error = nil, want the fallback's error: 60s is under the 69s reserve")
	}
	if len(gen.seeds) != 1 || fallback.calls != 1 {
		t.Fatalf("draws = %d fallback.calls = %d, want 1 and 1", len(gen.seeds), fallback.calls)
	}
}

// An admitted redraw whose draw and fallback leg each cost their estimate
// leaves at least the downstream slice: the reserve's own contract.
func TestInterpretRedrawReserveLeavesTheDownstreamSliceWhenEveryLegRunsAtItsEstimate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		fallback bool
		slowest  time.Duration
	}{
		{"fallback, 1s measured draw (the review's case)", 45 * time.Second, true, time.Second},
		{"fallback, 11s measured draw", 45 * time.Second, true, 11 * time.Second},
		{"fallback, nothing measured", 45 * time.Second, true, 0},
		{"no fallback, long timeout", 2 * time.Minute, false, 11 * time.Second},
	} {
		estimate := max(synthesisRedrawFloor, 2*tc.slowest)
		legs := estimate
		if tc.fallback {
			legs += estimate
		}
		reserved := interpretRedrawReserve(tc.timeout, tc.fallback, tc.slowest)
		if reserved >= map[bool]time.Duration{false: tc.timeout, true: 2 * tc.timeout}[tc.fallback] {
			t.Fatalf("%s: reserve %s hit the old-gate cap, the row does not test the uncapped contract", tc.name, reserved)
		}
		if left := reserved - legs; left < interpretRedrawDownstream {
			t.Errorf("%s: an admitted redraw leaves %s after its legs, want >= %s", tc.name, left, interpretRedrawDownstream)
		}
	}
}

func TestInterpretRedrawDecisionLogsTheDeadlineBasis(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		budget      time.Duration
		wantStopped bool
	}{
		{"stopped for the deadline", 60 * time.Second, true},
		{"redraw admitted", 75 * time.Second, false},
	} {
		handler, logger := newCaptureLogger()
		gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
		fallback := &trackedErroringFallback{err: errors.New("fallback unavailable"), receipt: validReceipt(contextfabric.ModelOperationInterpret)}
		clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
		rt := mustRuntime(t, slowRedrawGenerator{gen, clock, 11 * time.Second}, Config{Logger: logger, Timeout: 45 * time.Second, MaxAttempts: 1, MaxSynthesisResynthesisAttempts: 3, Fallback: fallback})
		rt.now = clock.read
		ctx, cancel := context.WithTimeout(context.Background(), tc.budget)
		_, _, _ = rt.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
		cancel()

		attrs := onlyDecisionEvent(t, handler).Attrs
		if got := attrs["resynthesis_deadline_reserved_ms"]; got != int64(69000) {
			t.Errorf("%s: reserved_ms = %#v, want 69000", tc.name, got)
		}
		remaining, ok := attrs["resynthesis_deadline_remaining_ms"].(int64)
		if !ok || remaining <= 0 || remaining > tc.budget.Milliseconds() {
			t.Errorf("%s: remaining_ms = %#v, want in (0, %d]", tc.name, attrs["resynthesis_deadline_remaining_ms"], tc.budget.Milliseconds())
		}
		if got := attrs["resynthesis_stopped_for_deadline"]; got != tc.wantStopped {
			t.Errorf("%s: stopped = %#v, want %v", tc.name, got, tc.wantStopped)
		}
	}
}

func TestInterpretRedrawDecisionCarriesNoDeadlineFieldsWithoutADeadline(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
	rt := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: 3})
	if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatal(err)
	}
	if _, present := onlyDecisionEvent(t, handler).Attrs["resynthesis_deadline_remaining_ms"]; present {
		t.Fatal("a call with no deadline must not write the deadline fields")
	}
}
