package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type ctxHonoringSink struct{ recorded []ModelExecutionReceipt }

func (s *ctxHonoringSink) RecordModelExecution(ctx context.Context, _ storage.Principal, r ModelExecutionReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.recorded = append(s.recorded, r)
	return nil
}

type blockingSink struct{ sawDeadline bool }

func (s *blockingSink) RecordModelExecution(ctx context.Context, _ storage.Principal, _ ModelExecutionReceipt) error {
	_, s.sawDeadline = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}

type cancellingRuntime struct {
	cancel  context.CancelFunc
	receipt ModelExecutionReceipt
}

func (c cancellingRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	c.cancel()
	return InterpretedQuestion{}, c.receipt, ErrModelUnavailable
}

func (c cancellingRuntime) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	c.cancel()
	return SynthesisDraft{}, c.receipt, ErrModelUnavailable
}

func (c cancellingRuntime) PhraseStructureOffers(context.Context, storage.Principal, StructureOfferPhrasingInput) (StructureOfferPhrasingDraft, ModelExecutionReceipt, error) {
	c.cancel()
	return StructureOfferPhrasingDraft{}, c.receipt, errors.New("provider unavailable")
}

func requireRecordedOnce(t *testing.T, sink *ctxHonoringSink, op ModelOperation, err error) {
	t.Helper()
	if errors.Is(err, ErrModelReceiptUnrecorded) {
		t.Fatalf("receipt reported unrecorded: %v", err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Operation != op {
		t.Fatalf("recorded = %#v, want exactly one %s receipt persisted although the caller context was cancelled", sink.recorded, op)
	}
}

func TestInterpretReceiptSurvivesCallerCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	sink := &ctxHonoringSink{}
	rec := validModelReceiptFixture(ModelOperationInterpret)
	rec.Outcome = "unavailable"
	interpreter := RuntimeQuestionInterpreter{Runtime: cancellingRuntime{cancel: cancel, receipt: rec}, Sink: sink}
	_, _, err := interpreter.Interpret(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err == nil {
		t.Fatal("want the model failure returned")
	}
	requireRecordedOnce(t, sink, ModelOperationInterpret, err)
}

func TestSynthesizeReceiptSurvivesCallerCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	sink := &ctxHonoringSink{}
	rec := validModelReceiptFixture(ModelOperationSynthesize)
	rec.Outcome = "unavailable"
	synthesizer := RuntimeAnswerSynthesizer{Runtime: cancellingRuntime{cancel: cancel, receipt: rec}, Sink: sink}
	_, err := synthesizer.Synthesize(ctx, storage.Principal{OrgID: "org_1"}, validSynthesisInputFixture())
	if err == nil {
		t.Fatal("want the model failure returned")
	}
	requireRecordedOnce(t, sink, ModelOperationSynthesize, err)
}

func TestOfferPhrasingReceiptSurvivesCallerCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	sink := &ctxHonoringSink{}
	rec := validModelReceiptFixture(ModelOperationPhraseOffers)
	rec.Outcome = "provider_error"
	phraser := RuntimeOfferPhraser{Runtime: cancellingRuntime{cancel: cancel, receipt: rec}, Sink: sink}
	result := phraser.Phrase(ctx, storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions()})
	if result.Outcome != OfferPhrasingCallFailed {
		t.Fatalf("Outcome = %q, want %q (a persisted receipt must not relabel the call)", result.Outcome, OfferPhrasingCallFailed)
	}
	requireRecordedOnce(t, sink, ModelOperationPhraseOffers, nil)
}

// The client-supplied synthesis arm reaches the sink through the same
// helper; the helper is pinned directly so that arm is covered by the shared
// property, not by a fourth composition.
func TestRecordModelReceiptSurvivesCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &ctxHonoringSink{}
	err := recordModelReceipt(ctx, storage.Principal{OrgID: "org_1"}, sink, validModelReceiptFixture(ModelOperationSynthesize))
	requireRecordedOnce(t, sink, ModelOperationSynthesize, err)
}

// Not parallel: it shortens the package timeout and restores it before
// returning, so no parallel test ever observes the change.
func TestRecordModelReceiptIsBoundedWhenTheSinkHangs(t *testing.T) {
	old := receiptRecordTimeout
	receiptRecordTimeout = 50 * time.Millisecond
	defer func() { receiptRecordTimeout = old }()
	sink := &blockingSink{}
	done := make(chan error, 1)
	go func() {
		done <- recordModelReceipt(context.Background(), storage.Principal{OrgID: "org_1"}, sink, validModelReceiptFixture(ModelOperationSynthesize))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrModelReceiptUnrecorded) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want ErrModelReceiptUnrecorded wrapping the deadline (existing fail-closed contract kept)", err)
		}
		if !sink.sawDeadline {
			t.Fatal("the sink context carried no deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recordModelReceipt did not return: the detached write is unbounded")
	}
}
