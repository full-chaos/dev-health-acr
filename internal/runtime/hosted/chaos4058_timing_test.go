package hosted_test

// CHAOS-4058: pure-logic proofs for the two-turn confirmation harness's
// timing observability plumbing (twoTurnModelCallCapture/twoTurnTimedModelRuntime,
// generative_trial_live_test.go; buildTwoTurnArmTiming/summarizeTwoTurnTiming,
// chaos3742_two_turn_confirmation_test.go). These run unconditionally under
// `make verify` -- no live corpus, no hosted.Open, no network -- the same
// non-live seam TestFileExchangeRoundTrip and TestDiscoverExchangeRequestFileSkipsInFlightTempFile
// use for the file-exchange transport itself.
//
// What this file does NOT prove: that a live TestChaos3742TwoTurnConfirmationReplay
// run correctly attributes a given InterpretQuestion/SynthesizeAnswer call
// to the right arm (that requires wiring modelCallCapture through a real
// investigator.Investigate() call against a real corpus, which is exactly
// what the withheld-corpus live gate above skips outside a real trial run).
// This file instead proves the two building blocks that live run depends
// on in isolation: (1) twoTurnTimedModelRuntime measures the WRAPPED
// runtime's own wall time and passes every value/error through unchanged,
// and (2) buildTwoTurnArmTiming/summarizeTwoTurnTiming reduce captured
// samples into the report fields correctly, including the nil-capture
// (real_api transport) and never-ran (mutation arm skipped) cases.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos4058SleepyModelRuntime is a minimal contextfabric.ModelRuntime whose
// InterpretQuestion/SynthesizeAnswer block for a caller-controlled duration
// before returning canned values/errors -- this file's non-live stand-in
// for a real generative call, used to prove twoTurnTimedModelRuntime times
// the underlying call itself rather than some fixed or zero duration.
// Mirrors org_model_config_test.go's fakeRuntime pattern; that type lives
// in package hosted (white-box), not hosted_test (black-box, what this
// file and chaos3742_two_turn_confirmation_test.go are in), hence a fresh
// minimal type here rather than a cross-package reuse.
type chaos4058SleepyModelRuntime struct {
	sleep           time.Duration
	interpretErr    error
	synthesizeErr   error
	interpretCalls  int
	synthesizeCalls int
}

func (r *chaos4058SleepyModelRuntime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	r.interpretCalls++
	time.Sleep(r.sleep)
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{Provider: "sleepy"}, r.interpretErr
}

func (r *chaos4058SleepyModelRuntime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	r.synthesizeCalls++
	time.Sleep(r.sleep)
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{Provider: "sleepy"}, r.synthesizeErr
}

func TestTwoTurnModelCallCaptureStats(t *testing.T) {
	c := &twoTurnModelCallCapture{}
	if count, total, max := c.stats(); count != 0 || total != 0 || max != 0 {
		t.Fatalf("stats() on an empty capture = (%d, %v, %v), want all zero", count, total, max)
	}

	c.samples = append(c.samples,
		twoTurnModelCallSample{Operation: "interpret", Duration: 10 * time.Millisecond},
		twoTurnModelCallSample{Operation: "synthesize", Duration: 30 * time.Millisecond},
		twoTurnModelCallSample{Operation: "interpret", Duration: 5 * time.Millisecond},
	)
	count, total, max := c.stats()
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if total != 45*time.Millisecond {
		t.Errorf("total = %v, want 45ms", total)
	}
	if max != 30*time.Millisecond {
		t.Errorf("max = %v, want 30ms", max)
	}

	c.reset()
	if count, total, max := c.stats(); count != 0 || total != 0 || max != 0 {
		t.Errorf("stats() after reset() = (%d, %v, %v), want all zero", count, total, max)
	}
}

// TestTwoTurnTimedModelRuntimeRecordsRoundTrips is the red-first proof this
// wrapper actually times the WRAPPED call (not some fixed/zero duration)
// and passes every return value and error through unchanged -- the two
// properties CHAOS-4058's per-responder-call latency field depends on.
func TestTwoTurnTimedModelRuntimeRecordsRoundTrips(t *testing.T) {
	const sleep = 20 * time.Millisecond
	underlying := &chaos4058SleepyModelRuntime{sleep: sleep, interpretErr: errors.New("boom")}
	capture := &twoTurnModelCallCapture{}
	wrapped := &twoTurnTimedModelRuntime{underlying: underlying, capture: capture}

	if _, _, err := wrapped.InterpretQuestion(context.Background(), storage.Principal{}, contextfabric.InvestigationRequest{}); err == nil || err.Error() != "boom" {
		t.Fatalf("InterpretQuestion() error = %v, want the underlying's own \"boom\" passed through unchanged", err)
	}
	if _, _, err := wrapped.SynthesizeAnswer(context.Background(), storage.Principal{}, contextfabric.SynthesisInput{}); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want nil (underlying returns nil)", err)
	}

	if got := len(capture.samples); got != 2 {
		t.Fatalf("capture recorded %d samples, want 2 (one interpret, one synthesize)", got)
	}
	for _, s := range capture.samples {
		if s.Duration < sleep {
			t.Errorf("sample %+v duration is below the underlying call's own sleep (%v) -- the wrapper is not timing the real call", s, sleep)
		}
	}
	if underlying.interpretCalls != 1 || underlying.synthesizeCalls != 1 {
		t.Errorf("underlying calls = (%d interpret, %d synthesize), want (1, 1) -- the wrapper must call through exactly once, never retry or drop the call", underlying.interpretCalls, underlying.synthesizeCalls)
	}
}

func TestBuildTwoTurnArmTiming(t *testing.T) {
	capture := &twoTurnModelCallCapture{}
	capture.samples = append(capture.samples,
		twoTurnModelCallSample{Operation: "interpret", Duration: 10 * time.Millisecond},
		twoTurnModelCallSample{Operation: "synthesize", Duration: 40 * time.Millisecond},
	)
	started := time.Now().Add(-100 * time.Millisecond)

	timing := buildTwoTurnArmTiming("positive", started, capture)
	if timing.Arm != "positive" {
		t.Errorf("Arm = %q, want %q", timing.Arm, "positive")
	}
	if timing.WallDurationMS < 100 {
		t.Errorf("WallDurationMS = %d, want >= 100", timing.WallDurationMS)
	}
	if timing.ResponderCallCount != 2 {
		t.Errorf("ResponderCallCount = %d, want 2", timing.ResponderCallCount)
	}
	if timing.ResponderCallTotalMS != 50 {
		t.Errorf("ResponderCallTotalMS = %d, want 50", timing.ResponderCallTotalMS)
	}
	if timing.ResponderCallMaxMS != 40 {
		t.Errorf("ResponderCallMaxMS = %d, want 40", timing.ResponderCallMaxMS)
	}

	// nil capture is exactly the real_api-transport case
	// (TestChaos3742TwoTurnConfirmationReplay's own setup comment): wall
	// time is still measured, but the responder-call fields must read zero,
	// never panic on the nil dereference.
	zeroTiming := buildTwoTurnArmTiming("confirmed_wrong", started, nil)
	if zeroTiming.WallDurationMS < 100 {
		t.Errorf("buildTwoTurnArmTiming(nil capture).WallDurationMS = %d, want >= 100 (wall time must still be measured)", zeroTiming.WallDurationMS)
	}
	if zeroTiming.ResponderCallCount != 0 || zeroTiming.ResponderCallTotalMS != 0 || zeroTiming.ResponderCallMaxMS != 0 {
		t.Errorf("buildTwoTurnArmTiming(nil capture) = %+v, want zero responder-call fields", zeroTiming)
	}
}

// TestSummarizeTwoTurnTiming pins the run-level aggregate's arithmetic
// (mean/p50/max, responder-call count/total) and its two edge cases: an
// arm that never ran on one case (mutation, WallDurationMS==0 with no
// calls -- the mutationTiming zero-value default at the harness's own call
// site) still contributes a real (zero) sample, never a skipped one; and
// arm order in the summary follows first-seen order across cases, matching
// the per-case loop's own execution sequence.
func TestSummarizeTwoTurnTiming(t *testing.T) {
	timings := []twoTurnCaseTiming{
		{Index: 0, Member: "expected_kind", Arms: []twoTurnArmTiming{
			{Arm: "turn1", WallDurationMS: 100, ResponderCallCount: 1, ResponderCallTotalMS: 100, ResponderCallMaxMS: 100},
			{Arm: "positive", WallDurationMS: 200, ResponderCallCount: 2, ResponderCallTotalMS: 300, ResponderCallMaxMS: 200},
			{Arm: "mutation", WallDurationMS: 0},
		}},
		{Index: 1, Member: "expected_kind", Arms: []twoTurnArmTiming{
			{Arm: "turn1", WallDurationMS: 300, ResponderCallCount: 1, ResponderCallTotalMS: 250, ResponderCallMaxMS: 250},
			{Arm: "positive", WallDurationMS: 400, ResponderCallCount: 1, ResponderCallTotalMS: 150, ResponderCallMaxMS: 150},
			{Arm: "mutation", WallDurationMS: 600, ResponderCallCount: 3, ResponderCallTotalMS: 900, ResponderCallMaxMS: 400},
		}},
	}

	summary := summarizeTwoTurnTiming(timings)
	if len(summary) != 3 {
		t.Fatalf("summarizeTwoTurnTiming() returned %d arms, want 3 (turn1, positive, mutation)", len(summary))
	}
	gotOrder := []string{summary[0].Arm, summary[1].Arm, summary[2].Arm}
	wantOrder := []string{"turn1", "positive", "mutation"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("arm order = %v, want %v (first-seen order, matching the per-case loop's own execution sequence)", gotOrder, wantOrder)
		}
	}

	byArm := map[string]twoTurnArmTimingSummary{}
	for _, s := range summary {
		byArm[s.Arm] = s
	}

	turn1 := byArm["turn1"]
	if turn1.SampleCount != 2 || turn1.WallMeanMS != 200 || turn1.WallP50MS != 300 || turn1.WallMaxMS != 300 {
		t.Errorf("turn1 wall summary = %+v, want {count:2 mean:200 p50:300 max:300}", turn1)
	}
	if turn1.ResponderCallCount != 2 || turn1.ResponderCallTotalMS != 350 {
		t.Errorf("turn1 responder-call summary = %+v, want {count:2 total:350}", turn1)
	}

	// mutation: case 0 never ran it (zero-value default), case 1 did --
	// the aggregate must still average over BOTH cases (including the
	// zero), not silently drop the never-ran one.
	mutation := byArm["mutation"]
	if mutation.SampleCount != 2 || mutation.WallMeanMS != 300 || mutation.WallMaxMS != 600 {
		t.Errorf("mutation wall summary = %+v, want {count:2 mean:300 max:600}", mutation)
	}
	if mutation.ResponderCallCount != 3 || mutation.ResponderCallTotalMS != 900 {
		t.Errorf("mutation responder-call summary = %+v, want {count:3 total:900} (only case 1's real run contributes calls)", mutation)
	}
}
