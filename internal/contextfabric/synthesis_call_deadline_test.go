package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// One row per error the call can end with, the expected class written here.
// A transient end is served when the runtime says its attempts are used or the
// reserve ended the call; before that it keeps its error.
func TestEveryTransientCallEndIsServedUnderOneClassAndNothingElseIs(t *testing.T) {
	exhausted := func(err error) error { return fmt.Errorf("%w: %w", ErrModelRetriesExhausted, err) }
	cases := []struct {
		name    string
		err     error
		reserve bool
		want    SynthesisFailureClass
		degrade bool
	}{
		{"rate limited, retries exhausted", exhausted(fmt.Errorf("%w: x", ErrModelRateLimited)), false, SynthesisFailureCallNotCompleted, true},
		{"unavailable, retries exhausted", exhausted(fmt.Errorf("%w: x", ErrModelUnavailable)), false, SynthesisFailureCallNotCompleted, true},
		{"cancelled, retries exhausted", exhausted(fmt.Errorf("%w: x", ErrModelCancelled)), false, SynthesisFailureCallNotCompleted, true},
		{"deadline, retries exhausted", exhausted(fmt.Errorf("call: %w", context.DeadlineExceeded)), false, SynthesisFailureCallNotCompleted, true},
		{"call context cancelled, retries exhausted", exhausted(fmt.Errorf("call: %w", context.Canceled)), false, SynthesisFailureCallNotCompleted, true},
		{"rate limited on the first attempt", fmt.Errorf("%w: x", ErrModelRateLimited), false, "", false},
		{"unavailable on the first attempt", fmt.Errorf("%w: x", ErrModelUnavailable), false, "", false},
		{"cancelled on the first attempt", fmt.Errorf("%w: x", ErrModelCancelled), false, "", false},
		{"deadline on the first attempt", fmt.Errorf("call: %w", context.DeadlineExceeded), false, "", false},
		{"call context cancelled on the first attempt", fmt.Errorf("call: %w", context.Canceled), false, "", false},
		{"deadline ended by the reserve", fmt.Errorf("call: %w", context.DeadlineExceeded), true, SynthesisFailureCallNotCompleted, true},
		{"rate limited with the reserve fired", fmt.Errorf("%w: x", ErrModelRateLimited), true, SynthesisFailureCallNotCompleted, true},
		{"invalid output", fmt.Errorf("%w: x", ErrModelOutput), false, SynthesisFailureModelOutputInvalid, true},
		{"rejected", fmt.Errorf("%w: %w", ErrSynthesisRejected, ErrModelOutput), false, SynthesisFailureRejected, true},
		{"input too large", fmt.Errorf("%w: x", ErrModelInputTooLarge), false, "", false},
		{"input overflow", &ModelInputOverflow{Bytes: 9, MaxBytes: 1}, false, "", false},
		{"a bare error", errors.New("generator closed"), true, "", false},
		{"exhausted transient beside the input-too-large sentinel", exhausted(errors.Join(ErrModelUnavailable, ErrModelInputTooLarge)), true, "", false},
		{"exhausted transient beside an overflow", exhausted(errors.Join(ErrModelUnavailable, &ModelInputOverflow{Bytes: 9, MaxBytes: 1})), true, "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			class, degrade := synthesisFailureDegradeClass(context.Background(), testCase.err, nil, testCase.reserve)
			if degrade != testCase.degrade || class != testCase.want {
				t.Fatalf("class = %q degrade = %v, want %q %v", class, degrade, testCase.want, testCase.degrade)
			}
		})
	}
}

func TestTheReserveMustFitInTheRequestTimeout(t *testing.T) {
	if err := ValidateSynthesisCompositionReserve(synthesisCompositionReserve); err == nil {
		t.Fatal("a request timeout equal to the reserve was accepted")
	}
	if err := ValidateSynthesisCompositionReserve(0); err == nil {
		t.Fatal("a zero request timeout was accepted")
	}
	if err := ValidateSynthesisCompositionReserve(synthesisCompositionReserve + time.Second); err != nil {
		t.Fatalf("a request timeout above the reserve was refused: %v", err)
	}
}

// A caller that is gone is never served an answer, whatever the call ended with.
func TestACallerThatIsGoneIsNeverServedADegradedAnswer(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{ErrModelUnavailable, ErrModelOutput, context.DeadlineExceeded, nil} {
		if class, degrade := synthesisFailureDegradeClass(gone, err, fmt.Errorf("%w: x", ErrModelReceiptUnrecorded), true); degrade {
			t.Fatalf("err %v served as %q for a caller that is gone", err, class)
		}
	}
}

type blockingSynthesisModel struct{ ended chan error }

func (blockingSynthesisModel) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	return InterpretedQuestion{}, ModelExecutionReceipt{}, errors.New("not used")
}

func (m blockingSynthesisModel) SynthesizeAnswer(ctx context.Context, _ storage.Principal, _ SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	<-ctx.Done()
	m.ended <- ctx.Err()
	return SynthesisDraft{}, ModelExecutionReceipt{}, ctx.Err()
}

// A call under a request deadline ends the reserve before it, leaves the
// request context alive, and its failure is the one served.
func TestAModelCallEndsTheReserveBeforeTheRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), synthesisCompositionReserve+300*time.Millisecond)
	defer cancel()
	ended := make(chan error, 1)
	started := time.Now()

	_, err := RuntimeAnswerSynthesizer{Runtime: blockingSynthesisModel{ended: ended}}.Synthesize(ctx, storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(1))

	var failure *SynthesisFailure
	if !errors.As(err, &failure) || failure.Class != SynthesisFailureCallNotCompleted || !failure.ReserveFired {
		t.Fatalf("err = %v, want a call-not-completed failure with the reserve fired", err)
	}
	if got := time.Since(started); got > 2*time.Second {
		t.Fatalf("call lasted %v, want it ended near 300ms", got)
	}
	if ctx.Err() != nil {
		t.Fatalf("request context ended: %v, want it alive for composing", ctx.Err())
	}
	if !errors.Is(<-ended, context.DeadlineExceeded) {
		t.Fatal("model call did not end on a deadline")
	}
}

// A request with no deadline is not given one, and the reserve does not fire
// for a call that fails on its own.
func TestAModelCallWithoutARequestDeadlineIsNotBounded(t *testing.T) {
	callCtx, release, applied := boundSynthesisCallContext(context.Background())
	defer release()
	if _, ok := callCtx.Deadline(); ok || applied {
		t.Fatal("a context without a deadline was given one")
	}
	_, err := RuntimeAnswerSynthesizer{Runtime: scriptedFailingModel{err: fmt.Errorf("%w: %w", ErrModelRetriesExhausted, ErrModelUnavailable)}}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(1))
	var failure *SynthesisFailure
	if !errors.As(err, &failure) || failure.ReserveFired {
		t.Fatalf("err = %v, want a failure that did not fire the reserve", err)
	}
}

type scriptedFailingModel struct{ err error }

func (scriptedFailingModel) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	return InterpretedQuestion{}, ModelExecutionReceipt{}, errors.New("not used")
}

func (m scriptedFailingModel) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	return SynthesisDraft{}, ModelExecutionReceipt{}, m.err
}
