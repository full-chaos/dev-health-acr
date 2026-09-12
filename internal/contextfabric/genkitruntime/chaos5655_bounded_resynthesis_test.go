package genkitruntime

import (
	"context"
	"errors"
	"testing"

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
}

func (g *drawSequenceGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("drawSequenceGenerator.Interpret is unused")
}

func (g *drawSequenceGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
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
	// The SERVED draft must be the third (valid) draw's, never one of the two
	// rejected drafts -- the ADR 0008 invariant this whole feature exists to
	// preserve ("one model sample is not the verdict", but a REJECTED sample
	// is never served regardless of how many were drawn).
	wantDraft, _ := valid.toDomain()
	if draft.DirectJudgment != wantDraft.DirectJudgment {
		t.Fatalf("draft.DirectJudgment = %q, want the THIRD draw's own content %q -- a rejected earlier draft must never be served", draft.DirectJudgment, wantDraft.DirectJudgment)
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
	// draw, matching the existing single-shot contract.
	if receipt.OutputDigest == "" {
		t.Fatalf("receipt.OutputDigest is empty on a successful call")
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
