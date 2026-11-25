package devhealthfacts

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// failingRoutes answers like routeClient and fails the statement that holds
// marker with err.
type failingRoutes struct {
	inner  *routeClient
	marker string
	err    error
}

func (c *failingRoutes) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, c.marker) {
		return nil, c.err
	}
	return c.inner.Query(ctx, statement, bindings)
}

// The investment watermark statement reads the organization's whole
// work_unit_investments for its newest computed_at: no window, no requested
// subject. A team with no mix reaches it; at the budget it must not tell the
// caller to narrow.
func TestTheInvestmentWatermarkStatementStoppedAtTheBudgetGivesNoNarrowingAdvice(t *testing.T) {
	t.Parallel()
	gatedOnly := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamPrefix + "T2"}
	client := &failingRoutes{
		marker: "SELECT toString(max(computed_at)), count() FROM work_unit_investments",
		err:    &BudgetExceededError{Code: 307, CapBytes: 1 << 20},
		inner: &routeClient{routes: []route{
			{marker: "AS sized_unit_hashes", rows: [][]any{
				uncategorizedRepoMixRow("r2", map[string]float64{"feature_delivery": 0}, 0, []uint64{}, []uint64{8, 9}),
			}},
			{marker: "FROM team_repo_ownership", rows: [][]any{{"T2", "r2"}}},
		}},
	}
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{newInvestmentProvider(client)}, contextfabric.FactRegistryOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.CanonicalFactRequest{
		Subjects:     []contextfabric.SubjectRef{gatedOnly},
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactInvestment}},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	var reason string
	for _, source := range bundle.Coverage.Sources {
		if source.Source == "canonical_fact:"+string(contextfabric.FactInvestment) {
			reason = source.Reason
		}
	}
	const want = "The investment read failed; the facts for it are not shown."
	if reason != want {
		t.Fatalf("reason %q, want %q", reason, want)
	}
}
