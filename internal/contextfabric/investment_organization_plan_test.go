package contextfabric_test

import (
	"context"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// An investment requirement on an organization subject is planned and read by
// the real provider: the organization question gets the organization mix. This
// pins the intended surface, so removing the organization kind from the
// capability fails here rather than silently pruning the read.
func TestPlannerAdmitsInvestmentForTheOrganizationSubject(t *testing.T) {
	client := &memberStateClient{values: []any{
		map[string]float64{"feature_delivery": 6, "risk": 4}, 0.0, uint64(3), uint64(2), 10.0, 0.0, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}}
	registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	org := cf.SubjectRef{Kind: cf.SubjectOrganization, CanonicalID: "organization:org-1", Label: "org-1"}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, cf.CanonicalFactRequest{
		Subjects:     []cf.SubjectRef{org},
		Question:     cf.InterpretedQuestion{TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}},
		Requirements: []cf.FactRequirement{{Kind: cf.FactInvestment}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.reads != 1 || len(bundle.Facts) != 1 {
		t.Fatalf("reads=%d facts=%d, want the organization investment fact read once", client.reads, len(bundle.Facts))
	}
	if got := bundle.Facts[0].Fields["scope"].String; got == nil || *got != "organization" {
		t.Fatalf("scope = %v, want organization", got)
	}
}
