package devhealthfacts_test

// The project mixes scope their work units by the organization's latest
// complete membership run. Phase 0 must learn that scope from the remembered
// run ids: the first read loads work_unit_membership once, every later read of
// the same run touches it not at all. Measured from system.query_log; a
// measurement that did not happen fails.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestProjectMixMembershipScopeIsReadOnceAndRememberedAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	const org = "org-pms-scoped"
	seedBudgetFixtureOrg(t, ctx, direct, org, budgetFixtureUnits, true)

	beforePass := budgetQueryLogTotals(t, ctx, direct, "work_unit_membership")
	var distinct uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM (SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE org_id = ? AND run_id = 'run-1')`, org).Scan(&distinct); err != nil {
		t.Fatalf("membership pass: %v", err)
	}
	onePass := budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_membership"), beforePass).readBytes
	if onePass == 0 || distinct != budgetFixtureUnits {
		t.Fatalf("measurement did not happen: membership pass read %d bytes for %d units", onePass, distinct)
	}

	start, end := ts(2026, 8, 11, 0, 0, 0), ts(2026, 9, 10, 0, 0, 0)
	read := func() (investments, membership budgetReadTotals) {
		beforeInv := budgetQueryLogTotals(t, ctx, direct, "work_unit_investments")
		beforeMem := budgetQueryLogTotals(t, ctx, direct, "work_unit_membership")
		if _, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:PRJ", Label: "PRJ"}},
		}); err != nil {
			t.Fatalf("ReadFacts: %v\nserver: %s", err, lastServerException(ctx, direct))
		}
		investments = budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_investments"), beforeInv)
		membership = budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_membership"), beforeMem)
		if investments.statements == 0 {
			t.Fatalf("measurement did not happen: no work_unit_investments statement for the project read")
		}
		return investments, membership
	}

	_, first := read()
	_, second := read()
	t.Logf("membership one pass=%d first-read membership bytes=%d statements=%d second-read bytes=%d statements=%d", onePass, first.readBytes, first.statements, second.readBytes, second.statements)

	if first.statements == 0 {
		t.Fatal("the first read did not load the membership scope: measurement did not happen")
	}
	if float64(first.readBytes) > 1.3*float64(onePass) {
		t.Errorf("first project read scanned membership %d bytes, want <= 1.3 x one pass (%d)", first.readBytes, onePass)
	}
	if second.statements != 0 || second.readBytes != 0 {
		t.Errorf("second project read touched work_unit_membership (%d statements, %d bytes), want none: the scope is remembered", second.statements, second.readBytes)
	}
}
