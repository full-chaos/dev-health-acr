package directread

import "testing"

func effectiveAccessCatalog(t *testing.T, caller CatalogCaller) DataCatalog {
	t.Helper()
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	sections, _ := ParseCatalogSections("operations")
	if caller.PrincipalClass == "" {
		caller.PrincipalClass = ClassUnrestricted
	}
	caller.Scopes = []string{"context:read", "evidence:read"}
	return BuildDataCatalog(cat, caller, sections)
}

func TestCatalogEffectiveAccessFollowsWhatRunOperationCanServe(t *testing.T) {
	base := CatalogCaller{DataRead: true, OperationsServable: true, GateComposed: true}
	cases := []struct {
		name   string
		mutate func(*CatalogCaller)
		access CatalogEffectiveAccess
		reason string
	}{
		{"data_read", func(*CatalogCaller) {}, EffectiveAccessDataRead, ""},
		{"no data:read scope", func(c *CatalogCaller) { c.DataRead = false }, EffectiveAccessNoDataRead, CatalogUnavailableScopeMissing},
		{"query service not configured", func(c *CatalogCaller) { c.OperationsServable = false }, EffectiveAccessNotServable, CatalogUnavailableQueryNotConfigure},
		{"subject gate missing", func(c *CatalogCaller) { c.GateComposed = false }, EffectiveAccessNotServable, CatalogUnavailableGateMissing},
	}
	for _, tc := range cases {
		caller := base
		tc.mutate(&caller)
		got := effectiveAccessCatalog(t, caller)
		if got.Caller.EffectiveAccess != tc.access || got.Operations.Reason != tc.reason {
			t.Errorf("%s: effective_access=%q reason=%q, want %q %q", tc.name, got.Caller.EffectiveAccess, got.Operations.Reason, tc.access, tc.reason)
		}
		// The same decision feeds both fields: available moves with access.
		if got.Operations.Available != (got.Caller.EffectiveAccess == EffectiveAccessDataRead) {
			t.Errorf("%s: operations.available=%v disagrees with effective_access=%q", tc.name, got.Operations.Available, got.Caller.EffectiveAccess)
		}
	}
}

func TestCatalogUnrestrictedGrantWithoutDataReadIsNotFullAccess(t *testing.T) {
	got := effectiveAccessCatalog(t, CatalogCaller{PrincipalClass: ClassUnrestricted, OperationsServable: true, GateComposed: true})
	if got.Caller.GrantClass != CallerUnrestricted || got.Caller.EffectiveAccess != EffectiveAccessNoDataRead {
		t.Fatalf("caller %+v", got.Caller)
	}
	if got.Operations.Available || got.Operations.Reason != CatalogUnavailableScopeMissing {
		t.Fatalf("operations %+v", got.Operations)
	}
}

func TestCatalogOperationsAndSchemaSectionsShareTheAccessDecision(t *testing.T) {
	policy := func() *GraphQLPolicy {
		p, err := DefaultGraphQLPolicy()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}()
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	sections, _ := ParseCatalogSections("operations,schema")
	base := CatalogCaller{PrincipalClass: ClassUnrestricted, Scopes: []string{"context:read"}, DataRead: true, OperationsServable: true, GraphQL: policy, GraphQLServable: true, GateComposed: true}
	for name, tc := range map[string]struct {
		mutate func(*CatalogCaller)
		reason string
	}{
		"data_read":    {func(*CatalogCaller) {}, ""},
		"no data:read": {func(c *CatalogCaller) { c.DataRead = false }, CatalogUnavailableScopeMissing},
		"no gate":      {func(c *CatalogCaller) { c.GateComposed = false }, CatalogUnavailableGateMissing},
	} {
		caller := base
		tc.mutate(&caller)
		got := BuildDataCatalog(cat, caller, sections)
		if got.Operations.Reason != tc.reason {
			t.Errorf("%s: operations reason %q, want %q", name, got.Operations.Reason, tc.reason)
		}
		if got.Operations.Available != got.Schema.Available || got.Operations.Reason != got.Schema.Reason {
			t.Errorf("%s: operations %v/%q, schema %v/%q", name, got.Operations.Available, got.Operations.Reason, got.Schema.Available, got.Schema.Reason)
		}
	}
}
