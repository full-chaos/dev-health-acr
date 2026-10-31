package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func projectOnlyFactPrincipal() storage.Principal {
	return storage.Principal{OrgID: "org-1", ProjectScopes: []string{"project.v2:linear:proj-1"}}
}

// CHAOS-12299 F5: a project-only caller reading a project's health fact gets
// no team-scoped and no repository-scoped row. Every field of that fact rests
// on those rows, none is intrinsic to the project, so no fact is served and
// none of those rows is read. Every other class is served as before.
func TestAProjectOnlyCallerIsServedNoTeamOrRepositoryRowOfAProjectHealthFact(t *testing.T) {
	t.Parallel()
	tables := func() []fakeTable {
		return []fakeTable{
			{match: healthProjectRollupMatch, rows: [][]any{
				healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "low", 0.05),
				healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "acme/api", "elevated", 0.4),
			}},
			{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", 2, 2, "repo", "repo-1", "elevated")}},
		}
	}
	read := func(principal storage.Principal) (contextfabric.FactProviderResult, *fakeClient) {
		client := &fakeClient{tables: tables()}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
		result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		return result, client
	}

	result, client := read(projectOnlyFactPrincipal())
	for _, fact := range result.Facts {
		if breakdown, ok := fact.Fields["risk_breakdown"]; ok && breakdown.Table != nil {
			for _, row := range breakdown.Table.Rows {
				t.Errorf("a project-only caller was served a %v row of a project health fact", row.Fields["scope"])
			}
		}
		t.Errorf("a project-only caller was served a project health fact resting on team and repository rows: %v", fact.Fields)
	}
	for _, query := range client.queries {
		if strings.Contains(query.statement, "team_project_ownership") {
			t.Errorf("a project-only caller's read joined team ownership: %.80s", query.statement)
		}
	}
	// Today the withheld subject carries no reason of its own (CHAOS-12330).
	if len(result.SubjectReasons) != 0 || len(result.NotApplicableSubjects) != 0 {
		t.Errorf("subject reasons %v, not applicable %v: a typed label arrived; update this pin", result.SubjectReasons, result.NotApplicableSubjects)
	}

	for _, principal := range []storage.Principal{
		{OrgID: "org-1"},
		{OrgID: "org-1", RepositoryScopes: []string{"*"}},
		{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}},
	} {
		result, _ := read(principal)
		if len(result.Facts) != 1 {
			t.Fatalf("%v: %d facts, want the project fact served as before", principal.RepositoryScopes, len(result.Facts))
		}
		if breakdown := result.Facts[0].Fields["risk_breakdown"]; breakdown.Table == nil || len(breakdown.Table.Rows) != 2 {
			t.Errorf("%v: risk_breakdown = %#v, want the team and repository rows as before", principal.RepositoryScopes, breakdown)
		}
	}
}

// The same withholding holds for every provider whose project fact rolls up
// through team ownership: a project-only caller's project read issues no
// statement and serves no fact; an unrestricted caller's still reads.
func TestEveryTeamRollupProjectFactIsWithheldFromAProjectOnlyCaller(t *testing.T) {
	t.Parallel()
	for _, kind := range []contextfabric.FactKind{
		contextfabric.FactHealth, contextfabric.FactFlow, contextfabric.FactLandscape,
		contextfabric.FactMetrics, contextfabric.FactReadiness, contextfabric.FactWorkload,
	} {
		read := func(principal storage.Principal) (facts, statements int) {
			client := &fakeClient{}
			provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
			result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: kind, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
			})
			if err != nil {
				t.Fatalf("%s: ReadFacts() error = %v", kind, err)
			}
			return len(result.Facts), len(client.queries)
		}
		if facts, statements := read(projectOnlyFactPrincipal()); facts != 0 || statements != 0 {
			t.Errorf("%s: a project-only caller's project read issued %d statements and served %d facts, want none", kind, statements, facts)
		}
		if _, statements := read(storage.Principal{OrgID: "org-1"}); statements == 0 {
			t.Errorf("%s: an unrestricted caller's project read issued no statement; this pin lost its control", kind)
		}
	}
}
