package devhealthfacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestChaos5405_EveryTypedFactScopeFailureIsProducedByTheRealExpander drives
// the production expander into each typed failure the resolver classifies by
// errors.Is, so a sentinel with no production producer cannot stay declared.
func TestChaos5405_EveryTypedFactScopeFailureIsProducedByTheRealExpander(t *testing.T) {
	t.Parallel()
	origin := chaos5405ProjectOrigin(t)
	request := func(axis contractsv1.ContextFabricTemporalAxis) contextfabric.FactScopeExpansionRequest {
		return contextfabric.FactScopeExpansionRequest{
			Principal:       orgWidePrincipal(),
			RequirementKind: contextfabric.FactStatus,
			Origins:         []contextfabric.SubjectRef{origin},
			Policy:          contextfabric.FactScopePolicyProjectWorkItemStatus,
			TargetKind:      contextfabric.SubjectWorkItem,
			TimeContext:     contextfabric.TimeContext{Axis: axis},
			Limit:           10,
		}
	}
	budget := &proto.Exception{Code: 158, Name: "TOO_MANY_ROWS", Message: "Limit for rows or bytes to read exceeded"}
	cases := []struct {
		name   string
		client *fakeClient
		axis   contractsv1.ContextFabricTemporalAxis
		want   error
	}{
		{"axis_unsupported", &fakeClient{}, contractsv1.ContextFabricTemporalValidTime, contextfabric.ErrFactScopeAxisUnsupported},
		{"read_limit_exceeded", &fakeClient{tables: []fakeTable{{match: "work_item", err: budget}}}, contractsv1.ContextFabricTemporalCurrent, contextfabric.ErrFactScopeReadLimitExceeded},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := devhealthfacts.NewScopeExpander(tc.client).ExpandFactScope(context.Background(), request(tc.axis))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}
