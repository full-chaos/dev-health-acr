package directread

import "testing"

func TestCatalogToolAccessStatesEachToolFromItsOwnDecision(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	sections, _ := ParseCatalogSections("operations")
	policy, err := DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	base := CatalogCaller{
		PrincipalClass: ClassUnrestricted, Scopes: []string{"context:read", "evidence:read"},
		GateComposed: true, OperationsServable: true, GraphQL: policy, GraphQLServable: true,
		SubjectsServable: true, RelationshipsServable: true,
	}
	cases := []struct {
		name   string
		mutate func(*CatalogCaller)
		want   map[string]CatalogToolAccess
	}{
		{"context-only credential", func(*CatalogCaller) {}, map[string]CatalogToolAccess{
			"run_operation": ToolAccessNoDataRead, "graphql_query": ToolAccessNoDataRead, "read_facts": ToolAccessNotServable,
			"find_subjects": ToolAccessAvailable, "read_relationships": ToolAccessAvailable,
		}},
		{"data:read credential", func(c *CatalogCaller) { c.DataRead = true }, map[string]CatalogToolAccess{
			"run_operation": ToolAccessAvailable, "graphql_query": ToolAccessAvailable, "read_facts": ToolAccessNotServable,
			"find_subjects": ToolAccessAvailable, "read_relationships": ToolAccessAvailable,
		}},
		{"nothing composed", func(c *CatalogCaller) {
			c.DataRead, c.GateComposed, c.OperationsServable, c.GraphQLServable, c.SubjectsServable, c.RelationshipsServable = true, false, false, false, false, false
		}, map[string]CatalogToolAccess{
			"run_operation": ToolAccessNotServable, "graphql_query": ToolAccessNotServable, "read_facts": ToolAccessNotServable,
			"find_subjects": ToolAccessNotServable, "read_relationships": ToolAccessNotServable,
		}},
	}
	for _, tc := range cases {
		caller := base
		tc.mutate(&caller)
		got := BuildDataCatalog(cat, caller, sections).Caller
		if len(got.ToolAccess) != len(tc.want) {
			t.Errorf("%s: tool_access %v", tc.name, got.ToolAccess)
		}
		for tool, want := range tc.want {
			if got.ToolAccess[tool] != want {
				t.Errorf("%s: %s = %q, want %q", tc.name, tool, got.ToolAccess[tool], want)
			}
		}
		if (got.ToolAccess["run_operation"] == ToolAccessAvailable) != (got.EffectiveAccess == EffectiveAccessDataRead) {
			t.Errorf("%s: run_operation tool_access %q disagrees with effective_access %q", tc.name, got.ToolAccess["run_operation"], got.EffectiveAccess)
		}
	}
}
