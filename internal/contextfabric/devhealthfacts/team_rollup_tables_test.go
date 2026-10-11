package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A read over several teams carries each team's totals and counts only, so an
// answer about many teams stays bounded; a read of one team still carries the
// per-repository tables a client continues from. Each fact says which it is.
func TestTeamRollupTablesServeASingleTeamReadOnly(t *testing.T) {
	for _, c := range t4TeamRollupCases() {
		c := c
		t.Run(c.name, func(t *testing.T) {
			read := func(subjects []contextfabric.SubjectRef) contextfabric.FactProviderResult {
				tables := make([]fakeTable, 0, len(c.tables))
				for _, table := range c.tables {
					if table.match == "GROUP BY team_id, repo_key" {
						rows := append([][]any{}, table.rows...)
						for _, row := range table.rows {
							rows = append(rows, []any{"OTHER", row[1], row[2]})
						}
						table.rows = rows
					}
					tables = append(tables, table)
				}
				provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), c.kind)
				result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
					Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: c.kind, Subjects: subjects,
				})
				if err != nil {
					t.Fatalf("ReadFacts: %v", err)
				}
				return result
			}
			one := read(c.subjects)
			if len(one.Facts) != 1 {
				t.Fatalf("single-team read: %d facts, want 1", len(one.Facts))
			}
			if got := stringField(t, one.Facts[0], "repository_tables_basis"); got != "included" {
				t.Fatalf("single-team repository_tables_basis = %q, want included", got)
			}
			for _, name := range []string{"owned_repositories", "repository_breakdown"} {
				if _, ok := one.Facts[0].Fields[name]; !ok {
					t.Fatalf("single-team read lost %s", name)
				}
			}
			many := read(append(append([]contextfabric.SubjectRef{}, c.subjects...), teamSubject("OTHER")))
			if len(many.Facts) != 2 {
				t.Fatalf("multi-team read: %d facts, want 2", len(many.Facts))
			}
			for _, fact := range many.Facts {
				if got := stringField(t, fact, "repository_tables_basis"); got != "omitted_multi_team_read" {
					t.Fatalf("multi-team repository_tables_basis = %q, want omitted_multi_team_read", got)
				}
				for _, name := range []string{"owned_repositories", "repository_breakdown", "owned_repositories_omitted_count", "repository_breakdown_omitted_count"} {
					if _, ok := fact.Fields[name]; ok {
						t.Fatalf("multi-team read carries %s: the per-repository tables must be left out", name)
					}
				}
				if value, ok := fact.Fields["owned_repository_count"]; !ok || value.Integer == nil || *value.Integer == 0 {
					t.Fatalf("multi-team read lost owned_repository_count: %#v", fact.Fields["owned_repository_count"])
				}
			}
		})
	}
}
