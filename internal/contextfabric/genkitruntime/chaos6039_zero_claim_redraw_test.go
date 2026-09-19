package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// claimedSynthesisOutput is validSynthesisOutput restating the readiness fact
// its input carries, the shape of an answer that claims a fact.
func claimedSynthesisOutput() synthesisOutput {
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(validSynthesisInput())}
	return output
}

// scriptedGenerator returns one scripted (output, error) per Synthesize call.
type scriptedGenerator struct {
	steps    []scriptedStep
	calls    int
	requests []generationRequest
}

type scriptedStep struct {
	output synthesisOutput
	err    error
}

func (g *scriptedGenerator) Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("scriptedGenerator.Interpret is unused")
}

func (g *scriptedGenerator) Synthesize(_ context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	g.requests = append(g.requests, request)
	step := g.steps[min(g.calls, len(g.steps)-1)]
	g.calls++
	return step.output, contextfabric.ModelUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}, step.err
}

func (g *scriptedGenerator) Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("scriptedGenerator.Phrase is unused")
}

func steps(outputs ...synthesisOutput) []scriptedStep {
	out := make([]scriptedStep, len(outputs))
	for i, o := range outputs {
		out[i] = scriptedStep{output: o}
	}
	return out
}

func digestOf(t *testing.T, output synthesisOutput) string {
	t.Helper()
	b, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	return contextfabric.DigestModelValue(b)
}

type redrawRun struct {
	draft   contextfabric.SynthesisDraft
	receipt contextfabric.ModelExecutionReceipt
	err     error
	line    certify.Line
}

// runRedraw drives SynthesizeAnswer through the real runtime and a real slog
// JSON handler and returns the parsed synthesis-input line.
func runRedraw(t *testing.T, ctx context.Context, gen *scriptedGenerator, override Config, input contextfabric.SynthesisInput) redrawRun {
	t.Helper()
	var buf bytes.Buffer
	override.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	runtime := mustRuntime(t, gen, override)
	draft, receipt, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, input)
	parsed, parseErr := certify.Parse(buf.Bytes())
	if parseErr != nil {
		t.Fatalf("certify.Parse: %v", parseErr)
	}
	lines := parsed.LinesWithMsg(eventspec.SynthesisInput.Msg)
	if len(lines) != 1 {
		t.Fatalf("synthesis input lines = %d, want 1: %s", len(lines), buf.String())
	}
	return redrawRun{draft: draft, receipt: receipt, err: err, line: lines[0]}
}

func (r redrawRun) certify(t *testing.T, want map[string]any) {
	t.Helper()
	want["request_id"] = "request_12345678"
	if _, ok := want["input_digest"]; !ok {
		want["input_digest"] = r.line["input_digest"]
	}
	want["model_id"] = r.line["model_id"]
	var buf bytes.Buffer
	line, _ := json.Marshal(r.line)
	buf.Write(line)
	buf.WriteByte('\n')
	parsed, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.SynthesisInput, Want: want}); err != nil {
		t.Fatalf("certify synthesis input line: %v", err)
	}
}

// TestZeroClaimDrawIsDrawnOnceMoreAndTheClaimingDraftServed: the first draft
// validates but claims nothing although the input's fact carries a reference;
// one more draw of the SAME prompt claims a fact and is the draft served.
// The default draw bound (1) is why the extra draw must come from the rule,
// not from the operator's re-synthesis setting.
func TestZeroClaimDrawIsDrawnOnceMoreAndTheClaimingDraftServed(t *testing.T) {
	t.Parallel()
	zero, claimed := validSynthesisOutput(), claimedSynthesisOutput()
	gen := &scriptedGenerator{steps: steps(zero, claimed)}
	run := runRedraw(t, context.Background(), gen, Config{}, validSynthesisInput())
	if run.err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", run.err)
	}
	if gen.calls != 2 {
		t.Fatalf("generator.calls = %d, want 2", gen.calls)
	}
	if gen.requests[1] != gen.requests[0] {
		t.Fatalf("the extra draw must send the identical request, got %+v then %+v", gen.requests[0], gen.requests[1])
	}
	if len(run.draft.ClaimedFacts) != 1 {
		t.Fatalf("served claims = %d, want the second draft's 1", len(run.draft.ClaimedFacts))
	}
	if digestOf(t, zero) == digestOf(t, claimed) {
		t.Fatal("fixture: the two drafts must differ")
	}
	run.certify(t, map[string]any{
		"outcome":             "success",
		"draws_total":         2,
		"draw_outcomes":       "1:success,2:success",
		"draw_claims":         "1:0,2:1",
		"draw_output_digests": fmt.Sprintf("1:%s,2:%s", digestOf(t, zero), digestOf(t, claimed)),
		"claims":              1,
		"zero_claim_redraw":   eventspec.SynthesisZeroClaimRedrawRecovered,
	})
	if run.receipt.Usage.TotalTokens != 56 {
		t.Fatalf("receipt usage total = %d, want both draws' 56", run.receipt.Usage.TotalTokens)
	}
}

// TestZeroClaimRedrawHappensExactlyOnce: two zero-claim drafts end the call at
// two draws although the operator bound (3) would allow a third; the second
// zero draft is served, honestly claiming nothing.
func TestZeroClaimRedrawHappensExactlyOnce(t *testing.T) {
	t.Parallel()
	gen := &scriptedGenerator{steps: steps(validSynthesisOutput(), validSynthesisOutput(), claimedSynthesisOutput())}
	run := runRedraw(t, context.Background(), gen, Config{MaxSynthesisResynthesisAttempts: 3}, validSynthesisInput())
	if run.err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", run.err)
	}
	if gen.calls != 2 {
		t.Fatalf("generator.calls = %d, want exactly 2 -- one extra draw, never a third", gen.calls)
	}
	if len(run.draft.ClaimedFacts) != 0 {
		t.Fatalf("served claims = %d, want the honest zero", len(run.draft.ClaimedFacts))
	}
	run.certify(t, map[string]any{
		"outcome": "success", "draws_total": 2, "draw_claims": "1:0,2:0", "claims": 0,
		"zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawStillZero,
	})
}

// TestClaimingFirstDraftIsNotRedrawn and the no-reference case: neither owes
// an extra draw.
func TestZeroClaimRedrawNotOwed(t *testing.T) {
	t.Parallel()
	t.Run("claims on the first draw", func(t *testing.T) {
		t.Parallel()
		gen := &scriptedGenerator{steps: steps(claimedSynthesisOutput(), claimedSynthesisOutput())}
		run := runRedraw(t, context.Background(), gen, Config{MaxSynthesisResynthesisAttempts: 3}, validSynthesisInput())
		if gen.calls != 1 {
			t.Fatalf("generator.calls = %d, want 1", gen.calls)
		}
		run.certify(t, map[string]any{"draws_total": 1, "claims": 1, "zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawNotNeeded})
	})
	t.Run("input facts carry no references", func(t *testing.T) {
		t.Parallel()
		input := validSynthesisInput()
		input.Facts.Facts[0].EvidenceRefIDs = nil
		gen := &scriptedGenerator{steps: steps(validSynthesisOutput(), claimedSynthesisOutput())}
		run := runRedraw(t, context.Background(), gen, Config{MaxSynthesisResynthesisAttempts: 3}, input)
		if run.err != nil {
			t.Fatalf("SynthesizeAnswer() error = %v", run.err)
		}
		if gen.calls != 1 {
			t.Fatalf("generator.calls = %d, want 1 -- nothing to claim from, so an empty claim list is honest", gen.calls)
		}
		run.certify(t, map[string]any{"draws_total": 1, "fact_evidence_refs": 0, "claims": 0, "zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawNotNeeded})
	})
}

// TestFailedZeroClaimRedrawServesTheEarlierValidDraft: the extra draw is
// rejected, or fails in transport; the call still answers with the valid
// zero-claim draft it already had, never with the failure.
func TestFailedZeroClaimRedrawServesTheEarlierValidDraft(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		step scriptedStep
		want map[string]any
	}{
		{"rejected", scriptedStep{output: invalidTitleSynthesisOutput()}, map[string]any{"draw_outcomes": "1:success,2:invalid_output", "draws_total": 2}},
		{"transport failure", scriptedStep{err: errors.New("provider down")}, map[string]any{"draw_outcomes": "1:success", "draws_total": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			zero := validSynthesisOutput()
			gen := &scriptedGenerator{steps: []scriptedStep{{output: zero}, tc.step}}
			run := runRedraw(t, context.Background(), gen, Config{}, validSynthesisInput())
			if run.err != nil {
				t.Fatalf("SynthesizeAnswer() error = %v, want the earlier valid draft served", run.err)
			}
			if run.receipt.Outcome != "success" {
				t.Fatalf("receipt.Outcome = %q, want success", run.receipt.Outcome)
			}
			if run.receipt.OutputDigest != digestOf(t, zero) {
				t.Fatalf("receipt.OutputDigest = %q, want the served (first) draft's %q", run.receipt.OutputDigest, digestOf(t, zero))
			}
			if run.receipt.Attempts != 1 {
				t.Fatalf("receipt.Attempts = %d, want the served draw's own 1", run.receipt.Attempts)
			}
			want := tc.want
			want["outcome"] = "success"
			want["claims"] = 0
			want["zero_claim_redraw"] = eventspec.SynthesisZeroClaimRedrawFailed
			run.certify(t, want)
		})
	}
}

// TestZeroClaimRedrawDeclinedWhenTheDeadlineCannotCoverIt: a remaining
// deadline below one attempt budget refuses the extra draw and serves the
// zero-claim draft, saying why.
func TestZeroClaimRedrawDeclinedWhenTheDeadlineCannotCoverIt(t *testing.T) {
	t.Parallel()
	gen := &scriptedGenerator{steps: steps(validSynthesisOutput(), claimedSynthesisOutput())}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	run := runRedraw(t, ctx, gen, Config{Timeout: time.Second}, validSynthesisInput())
	if run.err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", run.err)
	}
	if gen.calls != 1 {
		t.Fatalf("generator.calls = %d, want 1", gen.calls)
	}
	run.certify(t, map[string]any{"draws_total": 1, "claims": 0, "zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawDeclinedDeadline})
}

// TestZeroClaimRedrawDeclinedAtTheDrawCeiling: a call that already spent every
// draw the ceiling allows draws no more, and the digest lists stay within the
// log sanitizer's budget.
func TestZeroClaimRedrawDeclinedAtTheDrawCeiling(t *testing.T) {
	t.Parallel()
	gen := &scriptedGenerator{steps: steps(invalidTitleSynthesisOutput(), invalidEvidenceSynthesisOutput(), validSynthesisOutput(), claimedSynthesisOutput())}
	run := runRedraw(t, context.Background(), gen, Config{MaxSynthesisResynthesisAttempts: MaxSynthesisResynthesisAttemptsCeiling}, validSynthesisInput())
	if run.err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", run.err)
	}
	if gen.calls != MaxSynthesisResynthesisAttemptsCeiling {
		t.Fatalf("generator.calls = %d, want the ceiling %d", gen.calls, MaxSynthesisResynthesisAttemptsCeiling)
	}
	run.certify(t, map[string]any{
		"draws_total": 3, "draw_outcomes": "1:invalid_output,2:invalid_output,3:success", "claims": 0,
		"zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawDeclinedCeiling,
	})
}

// TestZeroClaimRedrawBelowTheCeilingBorrowsTheExtraDraw: a rejected first draw
// and a valid zero-claim second draw leave one draw under the ceiling, which
// the rule uses.
func TestZeroClaimRedrawBelowTheCeilingBorrowsTheExtraDraw(t *testing.T) {
	t.Parallel()
	gen := &scriptedGenerator{steps: steps(invalidTitleSynthesisOutput(), validSynthesisOutput(), claimedSynthesisOutput())}
	run := runRedraw(t, context.Background(), gen, Config{MaxSynthesisResynthesisAttempts: 2}, validSynthesisInput())
	if run.err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", run.err)
	}
	if gen.calls != 3 {
		t.Fatalf("generator.calls = %d, want 3 (bound 2 plus the one extra draw)", gen.calls)
	}
	run.certify(t, map[string]any{
		"draws_total": 3, "draw_outcomes": "1:invalid_output,2:success,3:success", "claims": 1,
		"zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawRecovered,
	})
}

// TestNoValidDrawLeavesTheRedrawRuleUnevaluated: when no draw validates the
// rule never ran, which is not the same as "not needed".
func TestNoValidDrawLeavesTheRedrawRuleUnevaluated(t *testing.T) {
	t.Parallel()
	gen := &scriptedGenerator{steps: steps(invalidTitleSynthesisOutput())}
	run := runRedraw(t, context.Background(), gen, Config{}, validSynthesisInput())
	if run.err == nil {
		t.Fatal("SynthesizeAnswer() must fail when no draw validates")
	}
	run.certify(t, map[string]any{"draws_total": 1, "zero_claim_redraw": eventspec.SynthesisZeroClaimRedrawNotEvaluated})
}
