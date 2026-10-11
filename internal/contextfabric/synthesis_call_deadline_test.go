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
func TestEveryTransientCallEndIsServedUnderOneClassAndNothingElseIs(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    SynthesisFailureClass
		degrade bool
	}{
		{"rate limited", fmt.Errorf("%w: x", ErrModelRateLimited), SynthesisFailureCallNotCompleted, true},
		{"unavailable", fmt.Errorf("%w: x", ErrModelUnavailable), SynthesisFailureCallNotCompleted, true},
		{"cancelled", fmt.Errorf("%w: x", ErrModelCancelled), SynthesisFailureCallNotCompleted, true},
		{"deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), SynthesisFailureCallNotCompleted, true},
		{"call context cancelled", fmt.Errorf("call: %w", context.Canceled), SynthesisFailureCallNotCompleted, true},
		{"invalid output", fmt.Errorf("%w: x", ErrModelOutput), SynthesisFailureModelOutputInvalid, true},
		{"rejected", fmt.Errorf("%w: %w", ErrSynthesisRejected, ErrModelOutput), SynthesisFailureRejected, true},
		{"input too large", fmt.Errorf("%w: x", ErrModelInputTooLarge), "", false},
		{"input overflow", &ModelInputOverflow{Bytes: 9, MaxBytes: 1}, "", false},
		{"a bare error", errors.New("generator closed"), "", false},
		{"a transient class beside the input-too-large sentinel", errors.Join(ErrModelUnavailable, ErrModelInputTooLarge), "", false},
		{"a transient class beside an overflow", errors.Join(ErrModelUnavailable, &ModelInputOverflow{Bytes: 9, MaxBytes: 1}), "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			class, degrade := synthesisFailureDegradeClass(context.Background(), testCase.err, nil)
			if degrade != testCase.degrade || class != testCase.want {
				t.Fatalf("class = %q degrade = %v, want %q %v", class, degrade, testCase.want, testCase.degrade)
			}
		})
	}
}

// A caller that is gone is never served an answer, whatever the call ended with.
func TestACallerThatIsGoneIsNeverServedADegradedAnswer(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{ErrModelUnavailable, ErrModelOutput, context.DeadlineExceeded, nil} {
		if class, degrade := synthesisFailureDegradeClass(gone, err, fmt.Errorf("%w: x", ErrModelReceiptUnrecorded)); degrade {
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
	_, err := RuntimeAnswerSynthesizer{Runtime: scriptedFailingModel{err: ErrModelUnavailable}}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(1))
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
