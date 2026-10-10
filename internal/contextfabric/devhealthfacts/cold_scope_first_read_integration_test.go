package devhealthfacts_test

// The first project investment read of a pod, on a cold membership scope, goes
// through the registry (the 5 s capability clock) against real ClickHouse and
// must not report a capability timeout. Wall time per read is logged.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestFirstProjectInvestmentReadOnAColdScopeIsNotATimeoutAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const org = "org-cold-first-read"
	seedBudgetFixtureOrg(t, ctx, direct, org, budgetFixtureUnits, true)

	var provider contextfabric.FactProvider
	for _, p := range devhealthfacts.NewProviders(query) {
		if p.Capability().Kind == contextfabric.FactInvestment {
			provider = p
		}
	}
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start, end := ts(2026, 8, 11, 0, 0, 0), ts(2026, 9, 10, 0, 0, 0)
	requirements := []contextfabric.FactRequirement{{Kind: contextfabric.FactInvestment, Parameters: map[string]string{}}}
	request := contextfabric.CanonicalFactRequest{
		Question: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, FactRequirements: requirements,
		},
		Subjects:     []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:PRJ", Label: "PRJ"}},
		Requirements: requirements,
	}
	for _, name := range []string{"first", "second"} {
		began := time.Now()
		bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: org}, request)
		t.Logf("%s read wall=%s", name, time.Since(began).Round(time.Millisecond))
		if err != nil {
			t.Fatalf("%s ReadFacts: %v", name, err)
		}
		if len(bundle.Coverage.Sources) == 0 {
			t.Fatalf("%s read: no coverage source: measurement did not happen", name)
		}
		for _, s := range bundle.Coverage.Sources {
			if strings.Contains(s.Reason, "timed out") {
				t.Errorf("%s read: %s state=%s reason=%q", name, s.Source, s.State, s.Reason)
			}
		}
	}
}
