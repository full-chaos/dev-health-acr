package modelruntimeresolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// sampledRuntime answers the per-sample interpretation port as well, so the
// ensemble path reaches a model.
type sampledRuntime struct{ phrasingRuntime }

func (s *sampledRuntime) InterpretQuestionForSample(context.Context, storage.Principal, contextfabric.InvestigationRequest, int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, nil
}

func newSampledRuntime(name string) *sampledRuntime {
	return &sampledRuntime{phrasingRuntime: phrasingRuntime{fakeRuntime: fakeRuntime{name: name}}}
}

func ownRuntimeBuild(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
	return newSampledRuntime("own"), nil
}

// The resolver records whose model a call runs on, at the one place it picks
// the runtime: the organization's own saved model, or the deployment default.
func TestTheResolverRecordsTheSourceOfEveryModelCall(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	calls := map[string]func(context.Context, *modelruntimeresolver.Resolver){
		"interpret": func(ctx context.Context, r *modelruntimeresolver.Resolver) {
			_, _, _ = r.InterpretQuestion(ctx, principal, contextfabric.InvestigationRequest{})
		},
		"synthesize": func(ctx context.Context, r *modelruntimeresolver.Resolver) {
			_, _, _ = r.SynthesizeAnswer(ctx, principal, contextfabric.SynthesisInput{})
		},
		"sample": func(ctx context.Context, r *modelruntimeresolver.Resolver) {
			_, _, _ = r.InterpretQuestionForSample(ctx, principal, contextfabric.InvestigationRequest{}, 0)
		},
	}
	for _, tc := range []struct {
		name     string
		resolver *modelruntimeresolver.Resolver
		ctx      func() context.Context
		want     contractsv1.ContextFabricModelSource
	}{
		{name: "own saved model", resolver: modelruntimeresolver.New(newSampledRuntime("deployment"), storedOrgModelConfig{}, ownRuntimeBuild), ctx: context.Background, want: contractsv1.ContextFabricModelSourceOrganization},
		{name: "no saved model", resolver: modelruntimeresolver.New(newSampledRuntime("deployment"), noOrgModelConfig{}, ownRuntimeBuild), ctx: context.Background, want: contractsv1.ContextFabricModelSourcePlatform},
		{name: "no per-organization support", resolver: modelruntimeresolver.New(newSampledRuntime("deployment"), nil, ownRuntimeBuild), ctx: context.Background, want: contractsv1.ContextFabricModelSourcePlatform},
		{name: "a turn bound to the deployment model", resolver: modelruntimeresolver.New(newSampledRuntime("deployment"), storedOrgModelConfig{}, ownRuntimeBuild), ctx: func() context.Context { return contextfabric.WithDeploymentModelBound(context.Background()) }, want: contractsv1.ContextFabricModelSourcePlatform},
	} {
		for name, call := range calls {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				ctx := contextfabric.WithModelSourceRecorder(tc.ctx())
				call(ctx, tc.resolver)
				got, err := contextfabric.ModelSourceOf(ctx)
				if err != nil || got != tc.want {
					t.Fatalf("model source = %q, %v, want %q", got, err, tc.want)
				}
			})
		}
	}

	t.Run("offer phrasing on the own model", func(t *testing.T) {
		resolver := modelruntimeresolver.New(newSampledRuntime("deployment"), storedOrgModelConfig{}, ownRuntimeBuild)
		ctx := contextfabric.WithModelSourceRecorder(context.Background())
		if _, _, err := resolver.PhraseStructureOffers(ctx, principal, contextfabric.StructureOfferPhrasingInput{}); err != nil {
			t.Fatal(err)
		}
		if got, err := contextfabric.ModelSourceOf(ctx); err != nil || got != contractsv1.ContextFabricModelSourceOrganization {
			t.Fatalf("model source = %q, %v, want organization", got, err)
		}
	})
}

// A call that runs no model records nothing: a withheld turn, a missing
// runtime and a broken organization configuration all return before a model is
// reached, and the turn stays unlabelled.
func TestNoModelCallRecordsNoSource(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	for _, tc := range []struct {
		name     string
		resolver *modelruntimeresolver.Resolver
		ctx      context.Context
	}{
		{name: "withheld turn", resolver: modelruntimeresolver.New(newSampledRuntime("deployment"), noOrgModelConfig{}, ownRuntimeBuild), ctx: contextfabric.WithPlatformModelWithheld(context.Background())},
		{name: "no runtime at all", resolver: modelruntimeresolver.New(nil, nil, ownRuntimeBuild), ctx: context.Background()},
		{name: "build failure", resolver: modelruntimeresolver.New(newSampledRuntime("deployment"), storedOrgModelConfig{}, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
			return nil, errors.New("no credential")
		}), ctx: context.Background()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := contextfabric.WithModelSourceRecorder(tc.ctx)
			_, _, _ = tc.resolver.InterpretQuestion(ctx, principal, contextfabric.InvestigationRequest{})
			_, _, _ = tc.resolver.SynthesizeAnswer(ctx, principal, contextfabric.SynthesisInput{})
			if got, err := contextfabric.ModelSourceOf(ctx); err != nil || got != "" {
				t.Fatalf("model source = %q, %v, want none", got, err)
			}
		})
	}
}

// One turn never mixes sources. Calls that land on both are reported as mixed,
// never as either one.
func TestATurnThatRanOnBothSourcesIsMixed(t *testing.T) {
	resolver := modelruntimeresolver.New(newSampledRuntime("deployment"), storedOrgModelConfig{}, ownRuntimeBuild)
	principal := storage.Principal{OrgID: "org_1"}
	ctx := contextfabric.WithModelSourceRecorder(context.Background())
	_, _, _ = resolver.InterpretQuestion(ctx, principal, contextfabric.InvestigationRequest{})
	_, _, _ = resolver.SynthesizeAnswer(contextfabric.WithDeploymentModelBound(ctx), principal, contextfabric.SynthesisInput{})
	if got, err := contextfabric.ModelSourceOf(ctx); !errors.Is(err, contextfabric.ErrModelSourceMixed) || got != "" {
		t.Fatalf("model source = %q, %v, want the mixed error and no label", got, err)
	}
}
