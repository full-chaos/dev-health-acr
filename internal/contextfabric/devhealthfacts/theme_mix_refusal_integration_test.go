package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// A statement over its read budget is refused by name, with the cap and what
// the server had read, never as a bare "query failed".
func TestThemeMixRefusalNamesTheBudgetAndMeasuredFactsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	const capBytes = uint64(1 << 20)
	query, direct := newScopedCHAOS7257Client(t, func(o *runtimeclickhouse.Options) {
		limit := capBytes
		o.MaxBytesToRead = &limit
	})
	createCHAOS7257Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(devhealthfacts.NewMeasuredQueryClient(query, capBytes)), contextfabric.FactInvestment)
	const orgID = "org-refusal"
	seedBudgetFixtureOrg(t, ctx, direct, orgID, budgetFixtureUnits, false)

	_, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: budgetFixtureSubjects(),
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("ReadFacts error = %v, want a FactReadFailure", err)
	}
	for _, want := range []string{"query repository theme mix", "exceeded the read budget", fmt.Sprintf("limit %d bytes", capBytes), "narrow the window or the subject"} {
		if !strings.Contains(failure.Reason, want) {
			t.Errorf("refusal reason %q does not contain %q", failure.Reason, want)
		}
	}
	if strings.HasSuffix(failure.Reason, " failed") {
		t.Errorf("refusal reason %q is the bare failure text", failure.Reason)
	}
}
