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

// The project mixes apply the membership scope the way the repository mix
// does: the run's unit ids are read once, remembered, and bound to phase 0;
// no project statement reads work_unit_membership.

const projectScopePhaseZero = "min(span_from) AS span_from"

func projectScopeRead(t *testing.T, client *fakeClient, provider contextfabric.FactProvider, org string) {
	t.Helper()
	query := contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:jira:PRJ", Label: "PRJ"}},
	}
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: org}, query); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
}

func projectScopeFixture(t *testing.T) (*fakeClient, contextfabric.FactProvider) {
	client := &fakeClient{tables: []fakeTable{
		{match: "sum(cityHash64("},
		{match: projectScopePhaseZero},
		{match: scopeUnitsMatch, rows: [][]any{{[]string{"wu-1", "wu-2"}}}},
		{match: scopeRunMatch, rows: [][]any{{"run-1"}}},
	}}
	return client, findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
}

func phaseZeroStatements(client *fakeClient) []capturedQuery {
	var out []capturedQuery
	for _, q := range client.queries {
		if strings.Contains(q.statement, projectScopePhaseZero) {
			out = append(out, q)
		}
	}
	return out
}

func TestProjectMixPhaseZeroReadsTheRememberedScopeIdsNotTheMembershipTable(t *testing.T) {
	t.Parallel()
	client, provider := projectScopeFixture(t)
	projectScopeRead(t, client, provider, "org-project-scope-1")
	projectScopeRead(t, client, provider, "org-project-scope-1")

	phase0 := phaseZeroStatements(client)
	if len(phase0) < 2 {
		t.Fatalf("phase 0 statements = %d, want at least one per read", len(phase0))
	}
	for _, q := range phase0 {
		if strings.Contains(q.statement, "work_unit_membership") {
			t.Fatal("phase 0 still names work_unit_membership: the scope must come from the bound ids")
		}
		ids, ok := scopeIDsBinding(q)
		if !ok || strings.Join(ids, ",") != "wu-1,wu-2" {
			t.Fatalf("phase 0 scope_json = %v (bound %v), want [wu-1 wu-2]", ids, ok)
		}
	}
	if got := countStatements(client, "groupUniqArray(100001)(work_unit_id)"); got != 1 {
		t.Fatalf("membership unit reads = %d over two project reads, want 1", got)
	}
}

func TestProjectMixOfALegacyRunKeepsTheScopeSubqueries(t *testing.T) {
	t.Parallel()
	client, provider := projectScopeFixture(t)
	for i := range client.tables {
		if client.tables[i].match == scopeRunMatch {
			client.tables[i].rows = [][]any{{"__legacy__"}}
		}
	}
	projectScopeRead(t, client, provider, "org-project-scope-legacy")
	for _, q := range phaseZeroStatements(client) {
		if _, ok := scopeIDsBinding(q); ok {
			t.Fatal("a legacy run must keep the subqueries; ids were bound")
		}
		if !strings.Contains(q.statement, "work_unit_membership") {
			t.Fatal("a legacy run's phase 0 lost its scope subqueries")
		}
	}
}

func TestProjectMixWithNoCompleteRunFiltersNothing(t *testing.T) {
	t.Parallel()
	client, provider := projectScopeFixture(t)
	for i := range client.tables {
		if client.tables[i].match == scopeRunMatch {
			client.tables[i].rows = [][]any{{""}}
		}
	}
	projectScopeRead(t, client, provider, "org-project-scope-none")
	phase0 := phaseZeroStatements(client)
	if len(phase0) == 0 {
		t.Fatal("no phase 0 statement")
	}
	for _, q := range phase0 {
		if strings.Contains(q.statement, "work_unit_membership") || strings.Contains(q.statement, "scope_json") {
			t.Fatal("no complete run: phase 0 must carry no scope predicate")
		}
	}
}

func TestProjectMixScopeAboveTheBoundKeepsTheScopeSubqueries(t *testing.T) {
	t.Parallel()
	client, provider := projectScopeFixture(t)
	big := make([]string, 0, 100001)
	for i := 0; i < 100001; i++ {
		big = append(big, fmt.Sprintf("wu-%06d", i))
	}
	for i := range client.tables {
		if client.tables[i].match == scopeUnitsMatch {
			client.tables[i].rows = [][]any{{big}}
		}
	}
	projectScopeRead(t, client, provider, "org-project-scope-big")
	phase0 := phaseZeroStatements(client)
	if len(phase0) == 0 {
		t.Fatal("no phase 0 statement")
	}
	for _, q := range phase0 {
		if _, bound := scopeIDsBinding(q); bound {
			t.Fatal("a scope above the bound must not be bound as one array")
		}
		if !strings.Contains(q.statement, "work_unit_membership AS m") {
			t.Fatal("an oversize scope must keep the scope subqueries, so the answer is the same")
		}
	}
}
