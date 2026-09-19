package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// redrawGenerator returns outputs[i] on call i (last one repeats) and records
// the decoding seed each call carried, so a test asserts the wire value.
type redrawGenerator struct {
	generatorStub
	outputs []interpretationOutput
	seeds   []int64
}

func (g *redrawGenerator) Interpret(_ context.Context, req generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	cfg, _ := req.Config.(map[string]any)
	seed, _ := cfg["seed"].(int64)
	g.seeds = append(g.seeds, seed)
	i := len(g.seeds) - 1
	if i >= len(g.outputs) {
		i = len(g.outputs) - 1
	}
	return g.outputs[i], contextfabric.ModelUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}, nil
}

func invalidInterpretationOutput() interpretationOutput {
	out := validInterpretationOutput()
	out.Shape = "not_a_shape"
	return out
}

func TestInterpretRedrawsWithNextSampleSeedAfterInvalidOutput(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
	rt := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: 3})
	req := validRequest()
	if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, req); err != nil {
		t.Fatalf("InterpretQuestion() error = %v, want the second draw to serve", err)
	}
	hash := contextfabric.QuestionHash(req.Question)
	want := []int64{chaos4631InterpretSeedFor(hash, 0), chaos4631InterpretSeedFor(hash, 1)}
	if len(gen.seeds) != 2 || gen.seeds[0] != want[0] || gen.seeds[1] != want[1] || want[0] == want[1] {
		t.Fatalf("seeds on the wire = %v, want %v (sample 0 then sample 1)", gen.seeds, want)
	}
	attrs := onlyDecisionEvent(t, handler).Attrs
	if got := attrInt(t, attrs, "redraws"); got != 1 {
		t.Fatalf("redraws = %d, want 1", got)
	}
	if got := attrInt(t, attrs, "initial_sample"); got != 0 {
		t.Fatalf("initial_sample = %d, want 0", got)
	}
	if got := attrInt(t, attrs, "sample"); got != 1 {
		t.Fatalf("sample = %d, want 1 (the serving draw)", got)
	}
}

func TestInterpretFinalRejectionAfterCeilingStaysAnError(t *testing.T) {
	t.Parallel()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput()}}
	rt := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: 3})
	if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the honest rejection after the ceiling")
	}
	if len(gen.seeds) != 3 {
		t.Fatalf("draws = %d, want exactly 3 (the ceiling)", len(gen.seeds))
	}
}

func TestInterpretDefaultCeilingOneNeverRedraws(t *testing.T) {
	t.Parallel()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput()}}
	rt := mustRuntime(t, gen, Config{})
	if _, _, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
		t.Fatal("want rejection")
	}
	if len(gen.seeds) != 1 {
		t.Fatalf("draws = %d, want 1", len(gen.seeds))
	}
}

func TestInterpretMeasurementEntryPointNeverRedraws(t *testing.T) {
	t.Parallel()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
	rt := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: 3})
	if _, _, err := rt.InterpretQuestionForSample(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest(), 4); err == nil {
		t.Fatal("want rejection: the sample-indexed entry point must keep one draw per sample")
	}
	if len(gen.seeds) != 1 {
		t.Fatalf("draws = %d, want 1", len(gen.seeds))
	}
}

func TestInterpretDoesNotRedrawOnDeadContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
	gen2 := &cancelingGen{redrawGenerator: gen, cancel: cancel}
	rt := mustRuntime(t, gen2, Config{MaxSynthesisResynthesisAttempts: 3})
	_, _, err := rt.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if contextfabric.InterpretationRejectionReasonOf(err) == contextfabric.InterpretationRejectionUnclassified {
		t.Fatalf("err = %v, want the first draw's own rejection, not a cancellation from a redraw", err)
	}
	if len(gen.seeds) != 1 {
		t.Fatalf("draws = %d, want 1 (deadline hit after first draw)", len(gen.seeds))
	}
}

type cancelingGen struct {
	*redrawGenerator
	cancel context.CancelFunc
}

func (g *cancelingGen) Interpret(ctx context.Context, req generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	out, u, err := g.redrawGenerator.Interpret(ctx, req)
	g.cancel()
	return out, u, err
}

func TestInterpretReceiptCountsEveryDrawsAttemptsAndUsage(t *testing.T) {
	t.Parallel()
	gen := &redrawGenerator{outputs: []interpretationOutput{invalidInterpretationOutput(), validInterpretationOutput()}}
	rt := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: 3})
	_, receipt, err := rt.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.Attempts != 2 {
		t.Fatalf("receipt.Attempts = %d, want 2 (one per draw)", receipt.Attempts)
	}
	if receipt.Usage.InputTokens != 20 || receipt.Usage.OutputTokens != 8 || receipt.Usage.TotalTokens != 28 {
		t.Fatalf("receipt usage = %+v, want the sum of both draws (20/8/28)", receipt.Usage)
	}
}
