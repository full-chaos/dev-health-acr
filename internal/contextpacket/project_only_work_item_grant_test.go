package contextpacket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func projectOnlyPacketPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-fixture", ProjectScopes: []string{"project.v2:github:P-1"}}
}

// CHAOS-12299 r1 F2 at clickhouse_evidence.go: a project-only principal with
// an empty repository list reads no evidence reference on the production
// path; the fixture principal's same read makes one.
func TestAProjectOnlyPrincipalReadsNoEvidenceReference(t *testing.T) {
	for _, tc := range []struct {
		principal storage.Principal
		reads     int
	}{{projectOnlyPacketPrincipal(), 0}, {fixturePrincipal(), 1}} {
		client := &scriptedClient{gateIDs: []string{"PROJ-5"}}
		codec := fixtureEvidenceCodec(t)
		store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(contextpacket.NewCatalogClickHouseRows(client), contextpacket.EvidenceStoreOptions{Codec: codec})
		if err != nil {
			t.Fatal(err)
		}
		handle, err := codec.Encode("org-fixture", "20000000-0000-4000-8000-000000000002", "work_graph.v1", "acr:v1:work-graph:edge", false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.ResolveEvidence(context.Background(), tc.principal, handle)
		if len(client.resolved) != tc.reads {
			t.Errorf("%v: reference reads = %d, want %d", tc.principal.ProjectScopes, len(client.resolved), tc.reads)
		}
		if tc.reads == 0 && !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%v: ResolveEvidence = %v, want not found", tc.principal.ProjectScopes, err)
		}
	}
}

// CHAOS-12299 r1 F2 at read_adapter.go: a project-only principal with an empty
// repository list gets no read plan, so no packet statement runs; the fixture
// principal's plan carries its granted repository.
func TestAProjectOnlyPrincipalGetsNoReadPlan(t *testing.T) {
	request := fixtureRequest("req-project-only", "main", "")
	plan, err := contextpacket.BuildReadPlanV1(projectOnlyPacketPrincipal(), request)
	if err == nil {
		t.Fatalf("a project-only principal got a read plan with grant %+v", plan.WorkItemGrant)
	}
	plan, err = contextpacket.BuildReadPlanV1(fixturePrincipal(), request)
	if err != nil || len(plan.WorkItemGrant.ExactSlugs) != 1 {
		t.Fatalf("the fixture principal's plan = %+v, %v; want its one granted repository", plan.WorkItemGrant, err)
	}
}
