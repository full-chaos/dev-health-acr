package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type loaderProviderStub struct {
	factProviderStub
	loadHasDeadline, readHasDeadline bool
	loads                            int
}

func (p *loaderProviderStub) LoadPrerequisites(ctx context.Context, _ storage.Principal, _ FactQuery) (context.Context, error) {
	_, p.loadHasDeadline = ctx.Deadline()
	p.loads++
	return ctx, nil
}

func (p *loaderProviderStub) ReadFacts(ctx context.Context, principal storage.Principal, query FactQuery) (FactProviderResult, error) {
	_, p.readHasDeadline = ctx.Deadline()
	return p.factProviderStub.ReadFacts(ctx, principal, query)
}

func TestPrerequisiteLoaderRunsOutsideTheCapabilityClock(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_x", Label: "X"}
	stub := &loaderProviderStub{factProviderStub: factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
		result:     FactProviderResult{State: SourceNoData, Reason: "none", Version: "v1"},
	}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{stub}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus)); err != nil {
		t.Fatal(err)
	}
	if stub.loads != 1 {
		t.Fatalf("loads = %d, want 1: measurement did not happen", stub.loads)
	}
	if !stub.readHasDeadline {
		t.Fatal("the provider read carries no capability deadline: measurement did not happen")
	}
	if stub.loadHasDeadline {
		t.Fatal("LoadPrerequisites ran inside the capability clock")
	}
}
