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
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// timedClient logs the wall time of every statement, labelled by branch, so a
// run's artifact carries the measured cold durations.
type timedClient struct {
	inner contextpacket.ClickHouseQueryClient
	t     *testing.T
	t0    time.Time
}

func (c *timedClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	label := "other"
	switch {
	case strings.Contains(statement, "groupUniqArray(100001)(work_unit_id)"):
		label = "scope_units_load"
	case strings.Contains(statement, "FROM work_unit_membership_runs"):
		label = "scope_run_id"
	case strings.Contains(statement, "min(span_from) AS span_from"):
		label = "phase0"
	}
	began := time.Now()
	scanner, err := c.inner.Query(ctx, statement, bindings)
	c.t.Logf("statement=%s start_ms=%d duration_ms=%d", label, began.Sub(c.t0).Milliseconds(), time.Since(began).Milliseconds())
	return scanner, err
}

func TestFirstProjectInvestmentReadOnAColdScopeIsNotATimeoutAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const org = "org-cold-first-read"
	seedBudgetFixtureOrg(t, ctx, direct, org, budgetFixtureUnits, true)

	timed := &timedClient{inner: query, t: t}
	var provider contextfabric.FactProvider
	for _, p := range devhealthfacts.NewProviders(timed) {
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
		timed.t0 = began
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
