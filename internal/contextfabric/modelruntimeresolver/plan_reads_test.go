package modelruntimeresolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type planningRuntime struct {
	fakeRuntime
	calls int
}

func (p *planningRuntime) PlanReads(context.Context, storage.Principal, contextfabric.ReadPlanningInput) (contextfabric.ReadPlan, contextfabric.ModelExecutionReceipt, error) {
	p.calls++
	return contextfabric.ReadPlan{}, contextfabric.ModelExecutionReceipt{}, nil
}

// The read planner resolves like every other call: an organization's own
// model first; a withheld turn without one never reaches the deployment
// default.
func TestPlanReadsResolvesThroughTheOrganizationFirstAndHonorsAWithheldTurn(t *testing.T) {
	deployment := &planningRuntime{fakeRuntime: fakeRuntime{name: "deployment"}}
	own := &planningRuntime{fakeRuntime: fakeRuntime{name: "own"}}
	build := func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return own, nil
	}
	principal := storage.Principal{OrgID: "org_1"}
	withheld := contextfabric.WithPlatformModelWithheld(context.Background())

	none := modelruntimeresolver.New(deployment, noOrgModelConfig{}, build)
	if _, _, err := none.PlanReads(withheld, principal, contextfabric.ReadPlanningInput{}); !errors.Is(err, contextfabric.ErrPlatformModelWithheld) || deployment.calls != 0 {
		t.Fatalf("withheld, no own model: err = %v deployment calls = %d, want the withheld class and 0 calls", err, deployment.calls)
	}
	if _, _, err := none.PlanReads(context.Background(), principal, contextfabric.ReadPlanningInput{}); err != nil || deployment.calls != 1 {
		t.Fatalf("not withheld, no own model: err = %v deployment calls = %d, want nil and 1", err, deployment.calls)
	}
	byo := modelruntimeresolver.New(deployment, storedOrgModelConfig{}, build)
	if _, _, err := byo.PlanReads(withheld, principal, contextfabric.ReadPlanningInput{}); err != nil || own.calls != 1 || deployment.calls != 1 {
		t.Fatalf("withheld, own model: err = %v own calls = %d deployment calls = %d, want nil, 1, 1", err, own.calls, deployment.calls)
	}
}

func TestPlanReadsWithARuntimeThatCannotPlanIsUnavailable(t *testing.T) {
	resolver := modelruntimeresolver.New(&fakeRuntime{name: "deployment"}, nil, nil)
	if _, _, err := resolver.PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.ReadPlanningInput{}); !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("err = %v, want model unavailable", err)
	}
	var typedNil *planningRuntime
	if _, _, err := modelruntimeresolver.New(typedNil, nil, nil).PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.ReadPlanningInput{}); !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("typed nil: err = %v, want model unavailable without a panic", err)
	}
}
