package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file pins the DEFENSIVE RE-VALIDATION adapter --
// RuntimeQuestionInterpreter.Interpret -- which builds and mutates its OWN
// ModelExecutionReceipt and persists it. A review round found it recording a
// rejection whose error named the rule while the durable receipt named
// nothing, and it was missed by an earlier sweep that enumerated "functions
// that canonicalize" rather than "every site that constructs or mutates a
// receipt on a rejection path".
//
// It fires only for a ModelRuntime that returns an invalid question with a
// NIL error, which no production implementation does -- unreachable in
// practice, which is not the same as exempt.
type revalidationRuntimeStub struct{ receipt ModelExecutionReceipt }

func (r revalidationRuntimeStub) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	// err == nil with an INVALID question: the shape the adapter's
	// defensive re-validation exists for.
	return InterpretedQuestion{
		Shape: "not_a_real_shape", RequestedJudgment: "x",
		TimeContext: TimeContext{Axis: TemporalCurrent},
	}, r.receipt, nil
}
func (r revalidationRuntimeStub) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	return SynthesisDraft{}, r.receipt, nil
}

type revalidationReceiptSink struct{ recorded []ModelExecutionReceipt }

func (s *revalidationReceiptSink) RecordModelExecution(_ context.Context, _ storage.Principal, r ModelExecutionReceipt) error {
	s.recorded = append(s.recorded, r)
	return nil
}

func TestDefensiveRevalidationNamesTheRuleOnItsOwnReceipt(t *testing.T) {
	started := time.Now().UTC()
	base := ModelExecutionReceipt{
		Operation: ModelOperationInterpret, Provider: "openai", Model: "m", ModelVersion: "v1",
		PromptVersion: "p1", SchemaVersion: "s1", EvaluatorVersion: "e1",
		InputDigest: strings.Repeat("a", 64), Outcome: "pending_validation",
		StartedAt: started, CompletedAt: started.Add(time.Second), Attempts: 1,
	}
	sink := &revalidationReceiptSink{}
	interp := RuntimeQuestionInterpreter{Runtime: revalidationRuntimeStub{receipt: base}, Sink: sink}

	_, _, err := interp.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, InvestigationRequest{Question: "q"})
	if err == nil {
		t.Fatal("want a rejection")
	}
	gotErrReason := InterpretationRejectionReasonOf(err)
	if gotErrReason != contractsv1.ContextFabricInterpretationRejectionShapeInvalid {
		t.Fatalf("fixture is wrong: returned error reason = %q", gotErrReason)
	}
	if len(sink.recorded) != 1 {
		t.Fatalf("recorded %d receipts", len(sink.recorded))
	}
	gotReceipt := sink.recorded[0].InterpretationRejectionReason
	if gotReceipt != contractsv1.ContextFabricInterpretationRejectionShapeInvalid {
		t.Fatalf("the returned error names %q but the DURABLE receipt persisted %q -- this adapter must copy the reason onto the receipt it owns", gotErrReason, gotReceipt)
	}
}
