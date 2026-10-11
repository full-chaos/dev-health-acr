package modelruntimeresolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type noOrgModelConfig struct{}

func (noOrgModelConfig) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	return contextfabric.ResolvedOrgModelConfig{}, false, nil
}

// A withheld turn never reaches the deployment default on any resolution
// branch that would otherwise return it: no per-organization support, no
// organization, or no stored configuration.
func TestWithheldTurnNeverResolvesTheDeploymentDefault(t *testing.T) {
	deployment := &fakeRuntime{name: "deployment"}
	build := func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		t.Error("no organization runtime is built here")
		return nil, errors.New("unexpected build")
	}
	withheld := contextfabric.WithPlatformModelWithheld(context.Background())
	for _, tc := range []struct {
		name     string
		resolver *modelruntimeresolver.Resolver
		org      string
	}{
		{name: "no per-organization support", resolver: modelruntimeresolver.New(deployment, nil, build), org: "org_1"},
		{name: "no organization", resolver: modelruntimeresolver.New(deployment, noOrgModelConfig{}, build), org: ""},
		{name: "no stored configuration", resolver: modelruntimeresolver.New(deployment, noOrgModelConfig{}, build), org: "org_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := storage.Principal{OrgID: tc.org}
			_, _, interpretErr := tc.resolver.InterpretQuestion(withheld, principal, contextfabric.InvestigationRequest{})
			_, _, synthesizeErr := tc.resolver.SynthesizeAnswer(withheld, principal, contextfabric.SynthesisInput{})
			_, _, sampleErr := tc.resolver.InterpretQuestionForSample(withheld, principal, contextfabric.InvestigationRequest{}, 0)
			for name, err := range map[string]error{"interpret": interpretErr, "synthesize": synthesizeErr, "sample": sampleErr} {
				if !errors.Is(err, contextfabric.ErrModelUnavailable) || runtimeName(err) == "fake runtime deployment answered" {
					t.Fatalf("%s error = %v, want model unavailable without the deployment runtime answering", name, err)
				}
			}
			_, _, err := tc.resolver.InterpretQuestion(context.Background(), principal, contextfabric.InvestigationRequest{})
			if err == nil || runtimeName(err) != "fake runtime deployment answered" {
				t.Fatalf("unwithheld interpret error = %v, want the deployment runtime to answer", err)
			}
		})
	}
}

type phrasingRuntime struct {
	fakeRuntime
	calls int
}

func (p *phrasingRuntime) PhraseStructureOffers(context.Context, storage.Principal, contextfabric.StructureOfferPhrasingInput) (contextfabric.StructureOfferPhrasingDraft, contextfabric.ModelExecutionReceipt, error) {
	p.calls++
	return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, nil
}

type storedOrgModelConfig struct{}

func (storedOrgModelConfig) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	return contextfabric.ResolvedOrgModelConfig{Provider: "p", Model: "m", Generation: 1}, true, nil
}

// Offer phrasing resolves like interpretation: a withheld turn without an own
// model never reaches the deployment default and reports the withheld class; a
// withheld turn with an own model runs on the own model.
func TestOfferPhrasingResolvesThroughTheOrganizationFirstAndHonorsAWithheldTurn(t *testing.T) {
	deployment := &phrasingRuntime{fakeRuntime: fakeRuntime{name: "deployment"}}
	own := &phrasingRuntime{fakeRuntime: fakeRuntime{name: "own"}}
	build := func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return own, nil
	}
	principal := storage.Principal{OrgID: "org_1"}
	withheld := contextfabric.WithPlatformModelWithheld(context.Background())

	none := modelruntimeresolver.New(deployment, noOrgModelConfig{}, build)
	if _, _, err := none.PhraseStructureOffers(withheld, principal, contextfabric.StructureOfferPhrasingInput{}); !errors.Is(err, contextfabric.ErrPlatformModelWithheld) || deployment.calls != 0 {
		t.Fatalf("withheld, no own model: err = %v deployment calls = %d, want the withheld class and 0 calls", err, deployment.calls)
	}
	if _, _, err := none.PhraseStructureOffers(context.Background(), principal, contextfabric.StructureOfferPhrasingInput{}); err != nil || deployment.calls != 1 {
		t.Fatalf("not withheld, no own model: err = %v deployment calls = %d, want nil and 1", err, deployment.calls)
	}
	byo := modelruntimeresolver.New(deployment, storedOrgModelConfig{}, build)
	if _, _, err := byo.PhraseStructureOffers(withheld, principal, contextfabric.StructureOfferPhrasingInput{}); err != nil || own.calls != 1 || deployment.calls != 1 {
		t.Fatalf("withheld, own model: err = %v own calls = %d deployment calls = %d, want nil, 1, 1", err, own.calls, deployment.calls)
	}
}
