package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func derivationInput() (contextfabric.SynthesisInput, string, string) {
	input := validSynthesisInput()
	subject := input.Facts.Facts[0].Subject
	input.Facts.Facts = []contextfabric.CanonicalFact{
		{Kind: contextfabric.FactReadiness, Subject: subject, Fields: map[string]contextfabric.FactValue{"open_items": contextfabric.IntegerFactValue(7)}, EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: contextfabric.SourceAvailable},
		{Kind: contextfabric.FactWork, Subject: subject, Fields: map[string]contextfabric.FactValue{"closed_items": contextfabric.IntegerFactValue(3)}, EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: contextfabric.SourceAvailable},
	}
	contextfabric.AssignFactIDs(input.Facts.Facts)
	return input, input.Facts.Facts[0].ID, input.Facts.Facts[1].ID
}

func derivedOutput(subject contextfabric.SubjectRef, value int64, inputs ...contractsv1.ContextFabricDerivationInput) synthesisOutput {
	out := validSynthesisOutput()
	out.ClaimedFacts = []contextfabric.ClaimedFact{{
		ClaimID: "claim_total", Kind: contextfabric.FactReadiness, Subject: subject, Field: "total_items",
		Value: contextfabric.ScalarValue{Integer: &value}, DerivedFrom: inputs, Operation: contractsv1.ContextFabricDerivationSum,
	}}
	return out
}

// A derivation over two served facts is admitted on the first draw: one
// model call, and the served claim carries its inputs.
func TestAGroundedDerivationIsServedOnTheFirstDrawWithItsInputs(t *testing.T) {
	t.Parallel()
	input, open, closed := derivationInput()
	subject := input.Facts.Facts[0].Subject
	good := derivedOutput(subject, 10, contractsv1.ContextFabricDerivationInput{FactID: open, Field: "open_items"}, contractsv1.ContextFabricDerivationInput{FactID: closed, Field: "closed_items"})
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{good, validSynthesisOutput()}}
	runtime := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: 3})
	draft, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}
	if gen.calls != 1 {
		t.Fatalf("model calls = %d, want 1: a grounded derivation was rejected and redrawn", gen.calls)
	}
	if len(draft.ClaimedFacts) != 1 || len(draft.ClaimedFacts[0].DerivedFrom) != 2 || draft.ClaimedFacts[0].Operation != contractsv1.ContextFabricDerivationSum {
		t.Fatalf("served claims = %+v, want the derivation with both inputs", draft.ClaimedFacts)
	}
}

// An ungrounded derivation stays rejected, every redraw stays inside the
// bound, and each rejected draw's reason is on the decision line.
func TestAnUngroundedDerivationStaysRejectedWithinTheBoundAndLogsEachReason(t *testing.T) {
	t.Parallel()
	handler, logger := newCaptureLogger()
	input, open, closed := derivationInput()
	subject := input.Facts.Facts[0].Subject
	wrongValue := derivedOutput(subject, 11, contractsv1.ContextFabricDerivationInput{FactID: open, Field: "open_items"}, contractsv1.ContextFabricDerivationInput{FactID: closed, Field: "closed_items"})
	unknownInput := derivedOutput(subject, 10, contractsv1.ContextFabricDerivationInput{FactID: open, Field: "open_items"}, contractsv1.ContextFabricDerivationInput{FactID: "fact_missing", Field: "closed_items"})
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{wrongValue, unknownInput, wrongValue, wrongValue}}
	runtime := mustRuntime(t, gen, Config{Logger: logger, MaxSynthesisResynthesisAttempts: 3})
	_, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err == nil {
		t.Fatal("ungrounded derivations were served")
	}
	if gen.calls != 3 {
		t.Fatalf("model calls = %d, want exactly the bound of 3", gen.calls)
	}
	events := handler.decisionEvents()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want 1", len(events))
	}
	attrs := events[0].Attrs
	want := "1:claim_derivation_value_mismatch,2:claim_derivation_input_unknown,3:claim_derivation_value_mismatch"
	if got := attrString(t, attrs, "draw_rejected_reasons"); got != want {
		t.Fatalf("draw_rejected_reasons = %q, want %q", got, want)
	}
}
