package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The membership scope of a repository mix read: the latest complete run is
// resolved per request; a real run's unit ids are read once, remembered per
// (organization, run) and bound to the mix statement; a legacy run keeps the
// scope subqueries; no run filters nothing.

const (
	scopeRunMatch   = "FROM work_unit_membership_runs WHERE org_id = {org_id:String}"
	scopeUnitsMatch = "FROM work_unit_membership WHERE org_id = {org_id:String} AND run_id = {scope_run:String}"
	scopeMixMatch   = "FROM work_unit_investments"
)

func scopeFixture(t *testing.T) (*fakeClient, contextfabric.FactProvider, contextfabric.FactQuery) {
	repo := repoUUID("scope-a")
	client := &fakeClient{tables: []fakeTable{
		{match: scopeMixMatch, rows: [][]any{
			{uint8(0), repo, map[string]float64{"feature_delivery": 4}, 0.0, uint64(2), clockSpanStart},
			{uint8(255), repo, map[string]float64{}, 0.0, uint64(2), clockSpanStart},
		}},
		{match: scopeUnitsMatch, rows: [][]any{{"wu-1"}, {"wu-2"}}},
		{match: scopeRunMatch, rows: [][]any{{"run-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	query := contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repo, Label: "scope-a"}},
	}
	return client, provider, query
}

func readScoped(t *testing.T, provider contextfabric.FactProvider, org string, query contextfabric.FactQuery) {
	t.Helper()
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: org}, query); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
}

func countStatements(client *fakeClient, match string) int {
	n := 0
	for _, q := range client.queries {
		if strings.Contains(q.statement, match) {
			n++
		}
	}
	return n
}

func lastMixStatement(t *testing.T, client *fakeClient) capturedQuery {
	t.Helper()
	for i := len(client.queries) - 1; i >= 0; i-- {
		if strings.Contains(client.queries[i].statement, scopeMixMatch) {
			return client.queries[i]
		}
	}
	t.Fatal("no mix statement was issued")
	return capturedQuery{}
}

func scopeIDsBinding(q capturedQuery) ([]string, bool) {
	for _, b := range q.bindings {
		if b.Name == "scope_ids" {
			ids, _ := b.Value.([]string)
			return ids, true
		}
	}
	return nil, false
}

func TestMembershipScopeOfARealRunIsReadOnceAndBoundToTheMixStatement(t *testing.T) {
	t.Parallel()
	client, provider, query := scopeFixture(t)
	readScoped(t, provider, "org-scope-1", query)
	readScoped(t, provider, "org-scope-1", query)
	readScoped(t, provider, "org-scope-1", query)

	if got := countStatements(client, "SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE"); got != 1 {
		t.Fatalf("membership unit reads = %d over three mix reads, want 1 (the run's scope is remembered)", got)
	}
	mix := lastMixStatement(t, client)
	if ids, ok := scopeIDsBinding(mix); !ok || strings.Join(ids, ",") != "wu-1,wu-2" {
		t.Fatalf("mix statement scope_ids = %v (bound %v), want [wu-1 wu-2]", ids, ok)
	}
	if strings.Contains(mix.statement, "work_unit_membership") {
		t.Fatal("the mix statement still names work_unit_membership: the scope must come from the bound ids")
	}
	if !strings.Contains(mix.statement, "work_unit_id IN {scope_ids:Array(String)}") {
		t.Fatal("the mix statement does not filter on the bound scope ids")
	}
}

func TestMembershipScopeIsKeyedByOrganizationAndReplacedWhenTheRunChanges(t *testing.T) {
	t.Parallel()
	client, provider, query := scopeFixture(t)
	readScoped(t, provider, "org-scope-a", query)
	readScoped(t, provider, "org-scope-b", query)
	if got := countStatements(client, "SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE"); got != 2 {
		t.Fatalf("unit reads = %d for two organizations, want 2", got)
	}
	// A new complete run for org-scope-a: its ids are read again, with the new run.
	for i := range client.tables {
		if client.tables[i].match == scopeRunMatch {
			client.tables[i].rows = [][]any{{"run-2"}}
		}
		if client.tables[i].match == scopeUnitsMatch {
			client.tables[i].rows = [][]any{{"wu-9"}}
		}
	}
	readScoped(t, provider, "org-scope-a", query)
	if got := countStatements(client, "SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE"); got != 3 {
		t.Fatalf("unit reads = %d after the run changed, want 3", got)
	}
	if ids, _ := scopeIDsBinding(lastMixStatement(t, client)); strings.Join(ids, ",") != "wu-9" {
		t.Fatalf("scope_ids after the run changed = %v, want [wu-9]: a stale run's ids must not be served", ids)
	}
}

func TestMembershipScopeOfALegacyRunKeepsTheScopeSubqueries(t *testing.T) {
	t.Parallel()
	client, provider, query := scopeFixture(t)
	for i := range client.tables {
		if client.tables[i].match == scopeRunMatch {
			client.tables[i].rows = [][]any{{"__legacy__"}}
		}
	}
	readScoped(t, provider, "org-scope-legacy", query)
	if got := countStatements(client, "SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE"); got != 0 {
		t.Fatalf("a legacy run read its unit ids %d times, want 0 (the subqueries keep its per-node rule)", got)
	}
	mix := lastMixStatement(t, client)
	if _, bound := scopeIDsBinding(mix); bound {
		t.Fatal("a legacy run must not bind scope_ids")
	}
	if !strings.Contains(mix.statement, "work_unit_membership AS m") {
		t.Fatal("a legacy run's mix statement lost the scope subqueries")
	}
}

func TestMembershipScopeWithNoCompleteRunFiltersNothing(t *testing.T) {
	t.Parallel()
	client, provider, query := scopeFixture(t)
	for i := range client.tables {
		if client.tables[i].match == scopeRunMatch {
			client.tables[i].rows = [][]any{{""}}
		}
	}
	readScoped(t, provider, "org-scope-none", query)
	mix := lastMixStatement(t, client)
	if strings.Contains(mix.statement, "work_unit_membership") || strings.Contains(mix.statement, "scope_ids") {
		t.Fatal("an organization with no complete run must read every work unit: no membership filter")
	}
}

func TestMembershipScopeTooLargeToRememberFallsBackToTheSubqueries(t *testing.T) {
	t.Parallel()
	client, provider, query := scopeFixture(t)
	big := make([][]any, 0, 100001)
	for i := 0; i < 100001; i++ {
		big = append(big, []any{fmt.Sprintf("wu-%06d", i)})
	}
	for i := range client.tables {
		if client.tables[i].match == scopeUnitsMatch {
			client.tables[i].rows = big
		}
	}
	readScoped(t, provider, "org-scope-big", query)
	mix := lastMixStatement(t, client)
	if _, bound := scopeIDsBinding(mix); bound {
		t.Fatal("a scope above the bound must not be bound as one array")
	}
	if !strings.Contains(mix.statement, "work_unit_membership AS m") {
		t.Fatal("an oversize scope must keep the scope subqueries, so the answer is the same")
	}
}
