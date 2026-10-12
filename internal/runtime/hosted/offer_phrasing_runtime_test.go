package hosted

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type plainDeploymentRuntime struct{}

func (*plainDeploymentRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, nil
}

func (*plainDeploymentRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, nil
}

type phrasingDeploymentRuntime struct{ plainDeploymentRuntime }

func (*phrasingDeploymentRuntime) PhraseStructureOffers(context.Context, storage.Principal, contextfabric.StructureOfferPhrasingInput) (contextfabric.StructureOfferPhrasingDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, nil
}

// Offer phrasing always resolves through a resolver, never straight to the
// deployment runtime, and a missing or typed-nil deployment runtime leaves it
// off.
func TestOfferPhrasingRuntimeForAlwaysResolvesThroughAResolver(t *testing.T) {
	var typedNil *phrasingDeploymentRuntime
	wired := modelruntimeresolver.New(nil, nil, nil)
	if got := offerPhrasingRuntimeFor(wired, nil); got != contextfabric.OfferPhrasingModelRuntime(wired) {
		t.Fatalf("with a resolver wired, got %T, want that resolver", got)
	}
	deployment := &phrasingDeploymentRuntime{}
	got := offerPhrasingRuntimeFor(nil, deployment)
	if _, ok := got.(*modelruntimeresolver.Resolver); !ok || got == contextfabric.OfferPhrasingModelRuntime(deployment) {
		t.Fatalf("without a resolver, got %T, want a resolver over the deployment runtime", got)
	}
	for name, runtime := range map[string]contextfabric.ModelRuntime{
		"nil":                 nil,
		"typed nil":           typedNil,
		"no phrasing support": &plainDeploymentRuntime{},
	} {
		if got := offerPhrasingRuntimeFor(nil, runtime); got != nil {
			t.Fatalf("%s deployment runtime: got %T, want nil", name, got)
		}
	}
}
