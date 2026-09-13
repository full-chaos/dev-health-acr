package genkitruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// drawSequenceGenerator (CHAOS-5655) returns a DIFFERENT synthesisOutput per
// Synthesize call, so one runtime can express "draw 1 rejected, draw 2
// rejected, draw 3 validates". Neither existing stub covers this:
// generatorStub returns one fixed output for the life of the stub, and
// sequencedGenerator (chaos5380_attempt_outcomes_test.go) varies the
// TRANSPORT error per call but still returns one fixed synthesisOutput on
// every call that does not error -- CHAOS-5655's own loop never varies the
// transport outcome, only the DRAFT content across draws.
type drawSequenceGenerator struct {
	outputs []synthesisOutput
	calls   int
	// requests (codex r1 finding 4) records every Synthesize call's own
	// generationRequest, so a test can assert the SAME prompt was sent on
	// every draw -- CHAOS-5655's own invariant ("re-sends the SAME encoded
	// synthesis prompt") was previously asserted only by reading the loop's
	// source, never by a generator that could see whether it held.
	requests []generationRequest
}

func (g *drawSequenceGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("drawSequenceGenerator.Interpret is unused")
}

func (g *drawSequenceGenerator) Synthesize(_ context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	g.requests = append(g.requests, request)
	index := g.calls
	if index >= len(g.outputs) {
		index = len(g.outputs) - 1
	}
	g.calls++
	return g.outputs[index], contextfabric.ModelUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}, nil
}

func (g *drawSequenceGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("drawSequenceGenerator.Phrase is unused")
}

// invalidTitleSynthesisOutput is validSynthesisOutput with a driver title
// overrunning ContextFabricDriverTitleMaxLength -- the SAME clause-bearing
// rejection TestRuntimeClassifiesSynthesisBoundViolationWithoutFallback
// already uses, reused here so the rejection shape (driver_invalid,
// ContextFabricClauseDriverTitle) is proven correct independently of this
// file.
func invalidTitleSynthesisOutput() synthesisOutput {
	output := validSynthesisOutput()
	output.Drivers[0].Title = stringsRepeatA(513)
	// codex r1 finding 4: validSynthesisOutput's DirectJudgment is a fixed
	// literal, identical across every variant built from it -- a test
	// asserting "the served draft is draw N's, not an earlier one" by
	// comparing DirectJudgment alone could not actually distinguish them.
	// Marked per-variant so that comparison is genuinely discriminating.
	output.DirectJudgment = "draw-1-rejected: title overrun"
	return output
}

// invalidEvidenceSynthesisOutput is validSynthesisOutput with invented
// evidence -- a business-rule rejection (evidence_unknown) that carries NO
// clause, the same shape TestRuntimeRejectsSynthesisThatInventsEvidence
// uses. Reused here so a test can assert a draw's Clause is
// ContextFabricClauseNone for a real, non-struct rejection.
func invalidEvidenceSynthesisOutput() synthesisOutput {
	output := validSynthesisOutput()
	output.EvidenceRefIDs = []string{"evidence_not_in_input"}
	output.DirectJudgment = "draw-2-rejected: invented evidence"
	return output
}

func stringsRepeatA(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// TestSynthesizeAnswerResynthesizesOnRejectionUntilSuccess is CHAOS-5655's
// own EXECUTE-THE-CLAIM: the validator rejects the first two draws (two
// DIFFERENT rejections, so the test cannot pass by coincidentally reusing
// one cached decision), the third validates, and the call must succeed --
// serving the THIRD draft, never one of the two rejected ones -- with every
// draw's digest and clause observable on the decision line.
func TestSynthesizeAnswerResynthesizesOnRejectionUntilSuccess(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	valid := validSynthesisOutput()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),    // draw 1: driver_invalid, clause-bearing
		invalidEvidenceSynthesisOutput(), // draw 2: evidence_unknown, no clause
		valid,                            // draw 3: validates
	}}
	// MaxSynthesisResynthesisAttempts is exactly the ceiling (3): the
	// dedicated success-break kill-check
	// (TestSynthesizeAnswerStopsDrawingImmediatelyOnSuccess, below) uses a
	// bound WIDER than its own fixture to catch a missing break by call
	// count; this test's own 3-output fixture already spans the whole
	// ceiling, so it cannot also do that here (see the ceiling's own doc
	// comment on why 3 is a hard limit, not merely "low").
	runtime := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: 3})

	draft, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want success on the third draw", err)
	}
	if receipt.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", receipt.Outcome)
	}
	if gen.calls != 3 {
		t.Fatalf("generator.calls = %d, want exactly 3 -- the loop must stop the moment a draw validates, never drawing a fourth time", gen.calls)
	}
	// codex r1 finding 4: every draw must see the IDENTICAL encoded prompt --
	// CHAOS-5655 re-sends the same synthesis prompt, it never re-assembles
	// one per draw. Asserted structurally here, not only by reading the
	// loop's source.
	if len(gen.requests) != 3 {
		t.Fatalf("generator saw %d requests, want 3", len(gen.requests))
	}
	for i, req := range gen.requests[1:] {
		if req.Prompt != gen.requests[0].Prompt || req.System != gen.requests[0].System || req.Model != gen.requests[0].Model {
			t.Fatalf("draw %d's request = %+v, want byte-identical to draw 1's %+v -- CHAOS-5655 must never re-assemble the prompt per draw", i+2, req, gen.requests[0])
		}
	}
	// The SERVED draft must be the third (valid) draw's, never one of the two
	// rejected drafts -- the ADR 0008 invariant this whole feature exists to
	// preserve ("one model sample is not the verdict", but a REJECTED sample
	// is never served regardless of how many were drawn).
	wantDraft, _ := valid.toDomain()
	if draft.DirectJudgment != wantDraft.DirectJudgment {
		t.Fatalf("draft.DirectJudgment = %q, want the THIRD draw's own content %q -- a rejected earlier draft must never be served", draft.DirectJudgment, wantDraft.DirectJudgment)
	}
	// codex r1 finding 4: DirectJudgment now differs on every one of the
	// three outputs (see invalidTitleSynthesisOutput/
	// invalidEvidenceSynthesisOutput), so this is a genuinely discriminating
	// negative check, not merely the same value compared to itself three
	// ways.
	if draft.DirectJudgment == "draw-1-rejected: title overrun" || draft.DirectJudgment == "draw-2-rejected: invented evidence" {
		t.Fatalf("draft.DirectJudgment = %q, a REJECTED draw's own content was served", draft.DirectJudgment)
	}

	events := handler.decisionEvents()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want exactly 1", len(events))
	}
	attrs := events[0].Attrs
	if got := attrInt(t, attrs, "draws_total"); got != 3 {
		t.Fatalf("draws_total = %d, want 3", got)
	}
	if got := attrInt(t, attrs, "draws_retried"); got != 2 {
		t.Fatalf("draws_retried = %d, want 2", got)
	}
	if got := attrString(t, attrs, "draw_outcomes"); got != "1:invalid_output,2:invalid_output,3:success" {
		t.Fatalf("draw_outcomes = %q, want %q", got, "1:invalid_output,2:invalid_output,3:success")
	}
	if got := attrString(t, attrs, "draw_rejected_clauses"); got != "1:driver.title_length,2:none,3:none" {
		t.Fatalf("draw_rejected_clauses = %q, want %q -- draw 1's clause-bearing rejection, draw 2's non-clause rejection, and the success draw must all be visible", got, "1:driver.title_length,2:none,3:none")
	}
	digests := attrString(t, attrs, "draw_output_digests")
	if digests == "" {
		t.Fatalf("draw_output_digests is empty, want three index-prefixed digests")
	}
	// The whole measurement instrument this ticket needed: three draws must
	// carry three DISTINCT digests, proving each draw was a genuine
	// re-sample and not the harness replaying one cached call.
	parts := splitCommaList(digests)
	if len(parts) != 3 {
		t.Fatalf("draw_output_digests = %q, want 3 comma-separated entries", digests)
	}
	if parts[0] == parts[1] || parts[1] == parts[2] || parts[0] == parts[2] {
		t.Fatalf("draw_output_digests = %q, want three DISTINCT digests -- identical digests would mean no real resampling occurred", digests)
	}
	// The final receipt's own OutputDigest must describe the SERVED (third)
	// draw EXACTLY -- not merely be non-empty. Both are computed from the
	// same `output` value by two separate call sites (the loop's own success
	// append, and SynthesizeAnswer's existing post-loop stamp); a mutation
	// turning either one into a constant breaks this equality even though a
	// mere non-empty check would not catch it.
	if receipt.OutputDigest == "" {
		t.Fatalf("receipt.OutputDigest is empty on a successful call")
	}
	if want := "3:" + receipt.OutputDigest; parts[2] != want {
		t.Fatalf("draw_output_digests' third entry = %q, want %q -- the served draw's digest must match receipt.OutputDigest exactly", parts[2], want)
	}
	// Each draw is its own full, separately billable model call --
	// drawSequenceGenerator reports {20,8,28} on every call, so three draws
	// must sum to {60,24,84}, not report only the served (third) draw's own
	// usage. A rejected draw's real cost must not go invisible.
	if want := (contextfabric.ModelUsage{InputTokens: 60, OutputTokens: 24, TotalTokens: 84}); receipt.Usage != want {
		t.Fatalf("receipt.Usage = %+v, want %+v (summed across all three draws)", receipt.Usage, want)
	}
}

// TestSynthesizeAnswerStopsDrawingImmediatelyOnSuccess isolates the loop's
// own success-break, split out from
// TestSynthesizeAnswerResynthesizesOnRejectionUntilSuccess because that
// test's own fixture already spans the whole ceiling (3) and so cannot also
// prove the bound is wider than the fixture. Here the bound (3) IS wider
// than the fixture (2 outputs): if the success break were ever lost,
// drawSequenceGenerator would keep returning the clamped LAST (valid)
// output for a third draw, and gen.calls would read 3 instead of 2.
func TestSynthesizeAnswerStopsDrawingImmediatelyOnSuccess(t *testing.T) {
	t.Parallel()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		validSynthesisOutput(),
	}}
	runtime := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: MaxSynthesisResynthesisAttemptsCeiling})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want success on the second draw", err)
	}
	if receipt.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", receipt.Outcome)
	}
	if gen.calls != 2 {
		t.Fatalf("generator.calls = %d, want exactly 2 -- the loop must stop the instant a draw validates, never drawing a third time even though the bound (%d) allows it", gen.calls, MaxSynthesisResynthesisAttemptsCeiling)
	}
}

// TestSynthesizeAnswerFailsClosedAfterExhaustingResynthesisBudget is the
// STOP-AND-REPORT counterpart: every draw within the N=3 budget is rejected,
// so the call must still fail closed with the existing 422 classification --
// ADR 0008's invariant is unchanged, only the number of draws before it
// applies.
func TestSynthesizeAnswerFailsClosedAfterExhaustingResynthesisBudget(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		invalidEvidenceSynthesisOutput(),
		invalidTitleSynthesisOutput(),
	}}
	runtime := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: 3})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil || !errors.Is(err, contextfabric.ErrSynthesisRejected) || !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("SynthesizeAnswer() error = %v, want both ErrSynthesisRejected and ErrModelOutput after exhausting the resynthesis budget", err)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want invalid_output", receipt.Outcome)
	}
	if gen.calls != 3 {
		t.Fatalf("generator.calls = %d, want exactly 3 -- the loop must stop at the configured bound, never drawing a fourth time with no fallback configured", gen.calls)
	}
	var violation *contextfabric.ModelBoundViolation
	if !errors.As(err, &violation) {
		t.Fatalf("SynthesizeAnswer() error = %v, want a ModelBoundViolation from the THIRD (last) draw's own rejection", err)
	}

	events := handler.decisionEvents()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want exactly 1", len(events))
	}
	attrs := events[0].Attrs
	if got := attrInt(t, attrs, "draws_total"); got != 3 {
		t.Fatalf("draws_total = %d, want 3", got)
	}
	if got := attrString(t, attrs, "draw_outcomes"); got != "1:invalid_output,2:invalid_output,3:invalid_output" {
		t.Fatalf("draw_outcomes = %q, want all three draws rejected", got)
	}
	// The route-level rejection_reason/rejected_clause must describe the
	// LAST draw (title overrun), not the first -- the same "never a maximum
	// over the draft, never an earlier evaluated statement" discipline
	// ValidateAgainst's own short-circuit already requires.
	if got := attrString(t, attrs, "rejection_reason"); got != string(contextfabric.RejectionReasonDriverInvalid) {
		t.Fatalf("rejection_reason = %q, want %q (the LAST draw's own rejection)", got, contextfabric.RejectionReasonDriverInvalid)
	}
	// receipt.OutputDigest and the LAST draw's own digest are both computed
	// from the same `output` value by two separate call sites (the loop's
	// own rejected-draw append, and SynthesizeAnswer's existing pre-5655
	// rejected-digest stamp) -- they must match EXACTLY, not merely both be
	// non-empty, or a mutation turning either into a constant survives.
	digests := splitCommaList(attrString(t, attrs, "draw_output_digests"))
	if len(digests) != 3 {
		t.Fatalf("draw_output_digests has %d entries, want 3", len(digests))
	}
	if want := "3:" + receipt.OutputDigest; digests[2] != want {
		t.Fatalf("draw_output_digests' third entry = %q, want %q -- the reported (last) draw's digest must match receipt.OutputDigest exactly", digests[2], want)
	}
	// Same accounting requirement as the success case: three rejected draws
	// are three real billable calls, so their usage must sum, not report
	// only the last one's.
	if want := (contextfabric.ModelUsage{InputTokens: 60, OutputTokens: 24, TotalTokens: 84}); receipt.Usage != want {
		t.Fatalf("receipt.Usage = %+v, want %+v (summed across all three draws)", receipt.Usage, want)
	}
}

// TestSynthesizeAnswerFallbackRunsOnceAfterResynthesisBudgetExhausted proves
// the ratified ordering: primary re-samples FIRST, fallback runs ONCE after
// the bound is exhausted -- never interleaved with the primary's draws, and
// never itself re-sampled.
func TestSynthesizeAnswerFallbackRunsOnceAfterResynthesisBudgetExhausted(t *testing.T) {
	t.Parallel()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		invalidTitleSynthesisOutput(),
		invalidTitleSynthesisOutput(),
	}}
	fallback := &trackedErroringFallback{
		err:     contextfabric.ClassifySynthesisRejection(validDraft(), validSynthesisInput(), errors.New("fallback also rejected")),
		receipt: validReceipt(contextfabric.ModelOperationSynthesize),
	}
	fallback.receipt.Outcome = "invalid_output"
	runtime := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: 3, Fallback: fallback})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil {
		t.Fatalf("SynthesizeAnswer() error = nil, want the fallback's own rejection once both legs have failed")
	}
	if gen.calls != 3 {
		t.Fatalf("generator.calls (primary) = %d, want exactly 3 -- the fallback must never be tried mid-budget", gen.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback.calls = %d, want exactly 1 -- the fallback leg itself must never be re-sampled by this loop", fallback.calls)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want the fallback leg's own outcome", receipt.Outcome)
	}
}

// rejectThenBlockUntilCanceledGenerator (codex r1 finding 2, 2026-09-13) is
// the generator for TestBoundedResynthesisCanSuppressAConfiguredFallback:
// draw 1 rejects immediately (a real, fast validator rejection, ctx still
// alive); draw 2 blocks until the caller's ctx is done, simulating a draw
// that runs into the caller's own request deadline mid-flight, then reports
// ctx.Err() -- exactly the shape a slow model call takes when it is still
// running as the deadline lands.
type rejectThenBlockUntilCanceledGenerator struct {
	calls   int
	started chan struct{}
}

func (g *rejectThenBlockUntilCanceledGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("rejectThenBlockUntilCanceledGenerator.Interpret is unused")
}

func (g *rejectThenBlockUntilCanceledGenerator) Synthesize(ctx context.Context, _ generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	g.calls++
	if g.calls == 1 {
		return invalidTitleSynthesisOutput(), contextfabric.ModelUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}, nil
	}
	close(g.started)
	<-ctx.Done()
	return synthesisOutput{}, contextfabric.ModelUsage{}, ctx.Err()
}

func (g *rejectThenBlockUntilCanceledGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("rejectThenBlockUntilCanceledGenerator.Phrase is unused")
}

// TestBoundedResynthesisWithNoDeadlineStillFollowsPreExistingFallbackComposition
// is codex round 1's P1 finding (2026-09-13). The reservation guard added
// below it (see SynthesizeAnswer's own comment, and
// TestBoundedResynthesisStopsDrawingWhenDeadlineCannotAffordAnotherDraw)
// FIXES this composition for a context that carries a deadline -- which
// every real deployment's ACR_REQUEST_TIMEOUT middleware does. It cannot
// fix it for a context that carries NO deadline at all (only a cancel
// func, as this test uses): ctx.Deadline() reports ok=false, there is no
// remaining-budget number to compare against, and the guard is correctly a
// no-op -- there is nothing to reserve against. In that residual case, both
// pieces of behavior this composes remain independently correct --
// fallbackWouldSeeADeadContext deliberately refuses to place a fallback
// call against an already-dead context (CHAOS-5577), and this ticket's own
// loop deliberately keeps drawing on a validator rejection -- and their
// composition still means a draw still in flight when a bare cancel lands
// suppresses a fallback that a faster rejection would have reached. This
// is now a narrower, still-real, still-disclosed (RISK-NOTES) residual,
// not a regression the guard could have closed.
func TestBoundedResynthesisWithNoDeadlineStillFollowsPreExistingFallbackComposition(t *testing.T) {
	t.Parallel()
	gen := &rejectThenBlockUntilCanceledGenerator{started: make(chan struct{})}
	fallback := &trackedErroringFallback{
		err:     errors.New("fallback was never supposed to be reachable in this repro"),
		receipt: validReceipt(contextfabric.ModelOperationSynthesize),
	}
	runtime := mustRuntime(t, gen, Config{MaxAttempts: 1, MaxSynthesisResynthesisAttempts: MaxSynthesisResynthesisAttemptsCeiling, Fallback: fallback})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInput())
		close(done)
	}()
	select {
	case <-gen.started:
		// draw 2 is now in flight -- this is the moment a real deployment's
		// request-timeout middleware would fire. Cancel to simulate it.
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("draw 2 was never reached -- cannot exercise the in-flight-cancellation case")
	}
	<-done

	if err == nil {
		t.Fatal("SynthesizeAnswer() error = nil, want the primary's own in-flight cancellation surfaced")
	}
	if gen.calls != 2 {
		t.Fatalf("generator.calls = %d, want exactly 2 (draw 1 rejected, draw 2 in flight when canceled)", gen.calls)
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback.calls = %d, want 0 -- draw 2 genuinely contacted the provider before the in-flight cancellation, so fallbackWouldSeeADeadContext correctly refuses to place a doomed fallback call. This is the documented composition risk, not a bug to fix here.", fallback.calls)
	}
}

// TestBoundedResynthesisStopsDrawingWhenDeadlineCannotAffordAnotherDraw is
// the 4452 vol.1 §10 D5 fix itself: chris's ruling on codex r1 finding 2
// ("the reserved synthesis deadline ships in the same slice ... or the
// terminal case is a 504 regardless") requires a redraw to check the
// caller's remaining request deadline first. Draw 1 rejects almost
// instantly; the context's own deadline (40ms) leaves far less remaining
// than Config.Timeout (1s, mustRuntime's default) -- nowhere near enough
// for a full second attempt -- so the loop must stop BEFORE calling the
// generator a second time and fall through to the existing fallback
// leg immediately, while the deadline still has room left for it. This is
// the fallback that codex r1's own repro found could be suppressed; this
// test proves the SAME class of scenario (a bound wider than 1, a
// context with a real deadline) now reaches it.
func TestBoundedResynthesisStopsDrawingWhenDeadlineCannotAffordAnotherDraw(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		validSynthesisOutput(), // must NEVER be reached -- the guard must stop before draw 2
	}}
	fallback := &trackedErroringFallback{
		err:     errors.New("fallback reached, as this test requires"),
		receipt: validReceipt(contextfabric.ModelOperationSynthesize),
	}
	runtime := mustRuntime(t, gen, Config{Logger: logger, MaxAttempts: 1, MaxSynthesisResynthesisAttempts: MaxSynthesisResynthesisAttemptsCeiling, Fallback: fallback})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	_, _, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil {
		t.Fatal("SynthesizeAnswer() error = nil, want the fallback's own error surfaced")
	}
	if gen.calls != 1 {
		t.Fatalf("generator.calls = %d, want exactly 1 -- the deadline guard must stop the loop BEFORE a second call, not merely record that one is unaffordable", gen.calls)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback.calls = %d, want exactly 1 -- stopping the resynthesis loop early must leave the fallback leg its chance to run, not suppress it the way an unguarded loop would", fallback.calls)
	}

	attrs := onlyDecisionEvent(t, handler).Attrs
	if got, ok := attrs["resynthesis_stopped_for_deadline"].(bool); !ok || !got {
		t.Fatalf("resynthesis_stopped_for_deadline = %#v, want true", attrs["resynthesis_stopped_for_deadline"])
	}
	if _, ok := attrs["resynthesis_deadline_remaining_ms"]; !ok {
		t.Fatalf("decision line missing resynthesis_deadline_remaining_ms: %#v", attrs)
	}
	if _, ok := attrs["resynthesis_deadline_reserved_ms"]; !ok {
		t.Fatalf("decision line missing resynthesis_deadline_reserved_ms: %#v", attrs)
	}
}

// TestBoundedResynthesisProceedsWhenDeadlineHasRoom is the guard's own
// negative case: a context WITH a deadline that comfortably covers another
// attempt must not be refused just because a deadline exists at all -- only
// an insufficient one stops the loop.
func TestBoundedResynthesisProceedsWhenDeadlineHasRoom(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	valid := validSynthesisOutput()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		valid,
	}}
	// Config.Timeout (the minimum newWithGenerator allows, 1s) is the
	// reservation; the context's own deadline (30s) leaves vastly more than
	// that after an instant draw 1, so the guard must allow draw 2.
	runtime := mustRuntime(t, gen, Config{Logger: logger, Timeout: time.Second, MaxSynthesisResynthesisAttempts: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, receipt, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want success on the second draw", err)
	}
	if receipt.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", receipt.Outcome)
	}
	if gen.calls != 2 {
		t.Fatalf("generator.calls = %d, want exactly 2 -- a deadline with ample room must not refuse the second draw", gen.calls)
	}

	attrs := onlyDecisionEvent(t, handler).Attrs
	if got, ok := attrs["resynthesis_stopped_for_deadline"].(bool); !ok || got {
		t.Fatalf("resynthesis_stopped_for_deadline = %#v, want false -- the deadline had room", attrs["resynthesis_stopped_for_deadline"])
	}
}

// TestBoundedResynthesisReservationIsTheWholeTimeoutNotAFraction pins the
// guard's own threshold: the reservation is a FULL Config.Timeout, not some
// fraction of it. Remaining budget after draw 1 (~800ms) sits strictly
// BETWEEN half of Config.Timeout (500ms) and the whole of it (1s) -- a
// guard checking only a fraction would wrongly let draw 2 proceed here.
func TestBoundedResynthesisReservationIsTheWholeTimeoutNotAFraction(t *testing.T) {
	t.Parallel()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		validSynthesisOutput(), // must NEVER be reached
	}}
	runtime := mustRuntime(t, gen, Config{Timeout: time.Second, MaxSynthesisResynthesisAttempts: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	_, _, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil {
		t.Fatal("SynthesizeAnswer() error = nil, want the draw-1 rejection surfaced (no fallback configured)")
	}
	if gen.calls != 1 {
		t.Fatalf("generator.calls = %d, want exactly 1 -- ~800ms remaining is less than the full 1s reservation, even though it is more than half of it", gen.calls)
	}
}

// TestSynthesizeAnswerResynthesisUnsetBehavesLikeSingleDraw is the
// backward-compatibility pin: a Config that never sets
// MaxSynthesisResynthesisAttempts (every caller/test that predates
// CHAOS-5655, and the fallback runtime -- see runtimeConfigWithPhrasing's
// own doc comment) must reject on the FIRST draw exactly as before this
// ticket, never drawing a second time.
func TestSynthesizeAnswerResynthesisUnsetBehavesLikeSingleDraw(t *testing.T) {
	t.Parallel()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		invalidTitleSynthesisOutput(),
		validSynthesisOutput(), // must NEVER be reached
	}}
	runtime := mustRuntime(t, gen, Config{})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil || !errors.Is(err, contextfabric.ErrSynthesisRejected) {
		t.Fatalf("SynthesizeAnswer() error = %v, want ErrSynthesisRejected on the first draw", err)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want invalid_output", receipt.Outcome)
	}
	if gen.calls != 1 {
		t.Fatalf("generator.calls = %d, want exactly 1 -- an unset MaxSynthesisResynthesisAttempts must behave exactly as it did before CHAOS-5655", gen.calls)
	}
}

// TestSynthesizeAnswerOmitsDrawFieldsOnAPureTransportFailure pins
// drawLogFields's own gating: a call whose FIRST draw never even reaches the
// validator (a transport failure) must carry NO draws_total/draw_outcomes/
// draw_output_digests/draw_rejected_clauses fields at all on the decision
// line -- not zero values, which the corpus-safety convention this codebase
// already follows (attemptsRetried's own doc comment) reserves for "reached
// and measured zero", a different state from "never reached the draw loop's
// judgment".
func TestSynthesizeAnswerOmitsDrawFieldsOnAPureTransportFailure(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	gen := &generatorStub{synthesisErr: errors.New("503 unavailable")}
	runtime := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: 3})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil {
		t.Fatalf("SynthesizeAnswer() error = nil, want a transport failure")
	}
	if receipt.Outcome == "invalid_output" || receipt.Outcome == "success" {
		t.Fatalf("receipt.Outcome = %q, want a transport classification, not a validator one", receipt.Outcome)
	}
	if len(gen.requests) != 1 {
		t.Fatalf("generator saw %d requests, want exactly 1 -- the draw loop must stop on a transport failure, never re-drawing after one", len(gen.requests))
	}

	events := handler.decisionEvents()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want exactly 1", len(events))
	}
	for _, key := range drawLogFieldKeys {
		if _, ok := events[0].Attrs[key]; ok {
			t.Fatalf("decision event carries %q = %#v on a pure transport failure, want it entirely absent", key, events[0].Attrs[key])
		}
	}
}

// TestNewWithGeneratorSynthesisResynthesisAttemptsDomain is the construction-
// time INPUT DOMAIN for Config.MaxSynthesisResynthesisAttempts: zero
// (defaults), the ceiling (accepted), one past the ceiling (rejected), and a
// negative value (rejected) -- every cell newWithGenerator's own guard can
// reach.
func TestNewWithGeneratorSynthesisResynthesisAttemptsDomain(t *testing.T) {
	t.Parallel()
	baseConfig := func() Config {
		return Config{
			Provider: "test-provider", Model: "test/model", Timeout: 1000000000, MaxAttempts: 1,
		}
	}
	cases := []struct {
		name    string
		value   int
		wantErr bool
		wantVal int
	}{
		{"zero defaults to one", 0, false, 1},
		{"one is the minimum, accepted", 1, false, 1},
		{"the documented default is accepted", DefaultMaxSynthesisResynthesisAttempts, false, DefaultMaxSynthesisResynthesisAttempts},
		{"the ceiling itself is accepted", MaxSynthesisResynthesisAttemptsCeiling, false, MaxSynthesisResynthesisAttemptsCeiling},
		{"one past the ceiling is rejected", MaxSynthesisResynthesisAttemptsCeiling + 1, true, 0},
		{"negative is rejected", -1, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := baseConfig()
			config.MaxSynthesisResynthesisAttempts = tc.value
			runtime, err := newWithGenerator(config, &generatorStub{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("newWithGenerator(%d) error = nil, want an error", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("newWithGenerator(%d) error = %v, want success", tc.value, err)
			}
			if runtime.config.MaxSynthesisResynthesisAttempts != tc.wantVal {
				t.Fatalf("config.MaxSynthesisResynthesisAttempts = %d, want %d", runtime.config.MaxSynthesisResynthesisAttempts, tc.wantVal)
			}
		})
	}
}

// TestSynthesisDrawClauseOmittedFieldsSpellNoneNotBlank pins
// formatSynthesisDrawClauses's own contract at the type level, independent
// of the full Runtime plumbing above: a draw with ContextFabricClauseNone
// renders the literal word "none", never an empty component, so the
// index-aligned lists in drawLogFields can never desynchronize on a blank
// entry.
func TestSynthesisDrawClauseOmittedFieldsSpellNoneNotBlank(t *testing.T) {
	t.Parallel()
	draws := []synthesisDraw{
		{Index: 1, Outcome: "invalid_output", OutputDigest: "digest-1", Clause: contractsv1.ContextFabricClauseNone},
		{Index: 2, Outcome: "success", OutputDigest: "digest-2", Clause: contractsv1.ContextFabricClauseNone},
	}
	if got := formatSynthesisDrawClauses(draws); got != "1:none,2:none" {
		t.Fatalf("formatSynthesisDrawClauses() = %q, want %q", got, "1:none,2:none")
	}
	if got := drawsRetried(draws); got != 1 {
		t.Fatalf("drawsRetried() = %d, want 1", got)
	}
	if got := drawsRetried(nil); got != 0 {
		t.Fatalf("drawsRetried(nil) = %d, want 0", got)
	}
}

// TestFormatSynthesisDrawDigestsFitsTheLogSanitizerBudget is codex round 1's
// own P1 finding (2026-09-13), reproduced as a permanent pin rather than a
// one-off probe: contextfabric.SanitizeLogAttr truncates every decision-line
// field at 256 RUNES, and draw_output_digests is the one field whose
// per-entry cost (a 64-hex-char digest) makes that bound reachable. At
// MaxSynthesisResynthesisAttemptsCeiling+1 draws the rendered string would
// already be truncated mid-digest; AT the ceiling it must not be. This is
// why the ceiling is 3, not merely "low" -- see its own doc comment.
func TestFormatSynthesisDrawDigestsFitsTheLogSanitizerBudget(t *testing.T) {
	t.Parallel()
	digest := func(b byte) string {
		out := make([]byte, 64)
		for i := range out {
			out[i] = b
		}
		return string(out)
	}
	drawsN := func(n int) []synthesisDraw {
		draws := make([]synthesisDraw, n)
		for i := range draws {
			draws[i] = synthesisDraw{Index: i + 1, Outcome: "invalid_output", OutputDigest: digest(byte('a' + i)), Clause: contractsv1.ContextFabricClauseNone}
		}
		return draws
	}

	// AT the ceiling: sanitizing must be a no-op -- every digest survives
	// whole, at its full 64-char length, untruncated.
	rendered := formatSynthesisDrawDigests(drawsN(MaxSynthesisResynthesisAttemptsCeiling))
	sanitized := contextfabric.SanitizeLogAttr(rendered)
	if sanitized != rendered {
		t.Fatalf("at the ceiling (%d draws): sanitized = %q, want it UNTRUNCATED (identical to the unsanitized render) %q", MaxSynthesisResynthesisAttemptsCeiling, sanitized, rendered)
	}
	parts := splitCommaList(sanitized)
	if len(parts) != MaxSynthesisResynthesisAttemptsCeiling {
		t.Fatalf("at the ceiling: got %d comma-separated entries, want %d", len(parts), MaxSynthesisResynthesisAttemptsCeiling)
	}
	for i, part := range parts {
		want := fmt.Sprintf("%d:%s", i+1, digest(byte('a'+i)))
		if part != want {
			t.Fatalf("draw %d's rendered entry = %q, want %q -- the full 64-char digest must survive untruncated", i+1, part, want)
		}
	}

	// One past the ceiling is exactly the shape the finding named: this
	// documents the failure mode as a REGRESSION TEST for the ceiling
	// itself, not a claim about production behavior (construction already
	// refuses a value above the ceiling; nothing in production can reach
	// this many draws).
	overCeiling := contextfabric.SanitizeLogAttr(formatSynthesisDrawDigests(drawsN(MaxSynthesisResynthesisAttemptsCeiling + 1)))
	if len([]rune(overCeiling)) != 256 {
		t.Fatalf("one past the ceiling: sanitized length = %d, want exactly 256 (truncated) -- if this ever stops truncating, the ceiling may be safe to raise", len([]rune(overCeiling)))
	}
}

// splitCommaList is a tiny local helper (this file has no other need for
// strings.Split) so the digest-distinctness assertion above reads plainly.
func splitCommaList(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
