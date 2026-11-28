package directread

import (
	"strings"
	"testing"
)

func TestCatalogRefusalReasonsQualifyReadFactsWhenFactsAreUnavailable(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	caller := CatalogCaller{
		PrincipalClass: ClassUnrestricted, Scopes: []string{"context:read", "data:read"}, DataRead: true,
		GateComposed: true, OperationsServable: true, GraphQL: policy, GraphQLServable: true,
		FactsServable: false,
	}
	d := BuildDataCatalog(cat, caller, []string{CatalogSectionOperations})
	if d.Operations == nil {
		t.Fatal("no operations section")
	}
	seen := 0
	check := func(where, reason string) {
		if !strings.Contains(reason, "read_facts") {
			return
		}
		seen++
		if !strings.Contains(reason, "read_facts (if listed") {
			t.Errorf("%s names read_facts bare: %s", where, reason)
		}
	}
	for _, ns := range d.Operations.NotServed {
		check("not_served "+ns.Name, ns.Reason)
	}
	for _, rs := range d.Operations.RefusedShapes {
		check("refused_shape "+rs.Operation+" "+rs.Path+" "+rs.Value, rs.Reason)
	}
	if seen == 0 {
		t.Fatal("no catalogue reason names read_facts: the control found nothing")
	}
	if want := "team and repository investment comes from read_facts (if listed"; !strings.HasPrefix(BasisDependentShapeText, want) {
		t.Errorf("BasisDependentShapeText = %q, want prefix %q", BasisDependentShapeText, want)
	}
}
