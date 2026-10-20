package modelruntimeresolver_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A turn the gate admitted on the deployment model is served by it on every
// call, even for an organization whose configuration exists by then.
func TestRuntimeFor_deploymentBoundTurnIgnoresTheOrgConfiguration(t *testing.T) {
	configs := newOrgConfigStub(nil)
	if _, err := configs.UpsertOrgModelConfig(context.Background(), storage.Principal{OrgID: "org-1"}, writeRequest("m1")); err != nil {
		t.Fatal(err)
	}
	resolver := modelruntimeresolver.New(&fakeRuntime{name: "default"}, configs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		t.Fatal("no organization runtime may be built for a deployment-bound turn")
		return nil, nil
	})
	ctx := contextfabric.WithDeploymentModelBound(context.Background())
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationRequest{})
	if err == nil || runtimeName(err) != "fake runtime default answered" {
		t.Fatalf("err = %v, want the deployment default to have answered", err)
	}
}
