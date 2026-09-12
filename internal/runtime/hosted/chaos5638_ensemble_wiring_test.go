package hosted

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// THE PRODUCTION DEFAULT IS ONE SAMPLE.
//
// This is the claim that keeps the ticket's yield honest: the ensemble is
// wired end to end and served behaviour does not move. The zero value of the
// option -- what every composition today passes, by never setting it -- must
// normalise to 1, which the interpreter treats as the pre-ensemble path.
func TestTheInterpretationEnsembleDefaultsToOneSample(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		configured int
		want       int
	}{
		{name: "unset zero value", configured: 0, want: 1},
		{name: "explicit one", configured: 1, want: 1},
		{name: "negative is the safe default, never an error", configured: -3, want: 1},
		{name: "a real ensemble is carried through", configured: 3, want: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := interpretationEnsembleSize(testCase.configured); got != testCase.want {
				t.Fatalf("interpretationEnsembleSize(%d) = %d, want %d", testCase.configured, got, testCase.want)
			}
		})
	}
}

// notSampledRuntime implements ModelRuntime and nothing more.
type notSampledRuntime struct{}

func (notSampledRuntime) InterpretQuestion(_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, nil
}

func (notSampledRuntime) SynthesizeAnswer(_ context.Context, _ storage.Principal, _ contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, nil
}

// canSampleRuntime implements the per-sample port as well as ModelRuntime.
type canSampleRuntime struct{ notSampledRuntime }

func (canSampleRuntime) InterpretQuestionForSample(_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, nil
}

// THE COMPOSITION'S OWN ASSERTION, BOTH WAYS.
//
// The negative arms alone are not enough and a mutation battery said so: with
// only "a non-sampled runtime yields nil", a build that returned nil for
// EVERYTHING passed, and the ensemble would then be permanently unreachable
// while every test stayed green. The positive arm is what makes the negative
// ones mean something.
func TestTheCompositionOnlyOffersARuntimeThatCanActuallySample(t *testing.T) {
	t.Parallel()
	if got := sampledModelRuntime(canSampleRuntime{}); got == nil {
		t.Fatal("sampledModelRuntime returned nil for a runtime that CAN sample -- the ensemble would be unreachable")
	}
	if got := sampledModelRuntime(notSampledRuntime{}); got != nil {
		t.Fatalf("sampledModelRuntime returned %T for a runtime with no per-sample method, want nil", got)
	}
	if got := sampledModelRuntime(nil); got != nil {
		t.Fatalf("sampledModelRuntime returned %T for a nil runtime, want nil", got)
	}
}
