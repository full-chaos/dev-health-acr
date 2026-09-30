package directread

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-7075 class sweep (pr2 r1 P1 at class level): EVERY data_catalog
// section whose tool is gated -- by a scope beyond the route's context:read
// or by a backing runner that may not be composed -- returns no detail for
// either reason, only its availability and why. One row per (section,
// reason). The ungated sections (subjects, relationships, limits) carry the
// closed contract vocabularies and bounds only; they are identical for
// every caller and hold no organization data, which the last loop pins.
func TestDataCatalogGatedSectionsEmitNoDetailWhenUnavailable(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []contextfabric.FactCapability{{Kind: "health", SupportedSubjectKinds: []contextfabric.SubjectKind{"repository"}, Fields: []contextfabric.FactFieldDeclaration{{Name: "severity"}}}}
	full := CatalogCaller{
		PrincipalClass: ClassUnrestricted, Scopes: []string{"context:read", "data:read"}, DataRead: true,
		OperationsServable: true, GraphQL: policy, GraphQLServable: true, GateComposed: true, FactsServable: true, FactCapabilities: capabilities,
	}
	rows := []struct {
		name    string
		section string
		edit    func(c *CatalogCaller)
		empty   func(d DataCatalog) (bool, string)
	}{
		{"operations/no data:read", CatalogSectionOperations, func(c *CatalogCaller) { c.DataRead = false }, operationsEmpty},
		{"operations/no runner", CatalogSectionOperations, func(c *CatalogCaller) { c.OperationsServable = false }, operationsEmpty},
		{"operations/no gate", CatalogSectionOperations, func(c *CatalogCaller) { c.GateComposed = false }, operationsEmpty},
		{"schema/no data:read", CatalogSectionSchema, func(c *CatalogCaller) { c.DataRead = false }, schemaEmpty},
		{"schema/no runner", CatalogSectionSchema, func(c *CatalogCaller) { c.GraphQLServable = false }, schemaEmpty},
		{"schema/no gate", CatalogSectionSchema, func(c *CatalogCaller) { c.GateComposed = false }, schemaEmpty},
		{"schema/no policy", CatalogSectionSchema, func(c *CatalogCaller) { c.GraphQL = nil }, schemaEmpty},
		{"facts/no reader", CatalogSectionFacts, func(c *CatalogCaller) { c.FactsServable = false }, factsEmpty},
		{"facts/reader serves nothing", CatalogSectionFacts, func(c *CatalogCaller) { c.FactCapabilities = nil }, factsEmpty},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			caller := full
			row.edit(&caller)
			d := BuildDataCatalog(cat, caller, []string{row.section})
			if ok, detail := row.empty(d); !ok {
				t.Fatalf("an unavailable section carries detail: %s", detail)
			}
		})
	}
	// The same sections DO carry detail when available (the rows above
	// measure a real difference).
	d := BuildDataCatalog(cat, full, []string{CatalogSectionOperations, CatalogSectionSchema, CatalogSectionFacts})
	if len(d.Operations.Operations) == 0 || len(d.Schema.Roots) == 0 || len(d.Facts.Kinds) == 0 {
		t.Fatal("the available sections carry no detail: the sweep measured nothing")
	}
	// Ungated sections: identical for a caller with and without data:read.
	for _, section := range []string{CatalogSectionSubjects, CatalogSectionRelationships, CatalogSectionLimits} {
		with := BuildDataCatalog(cat, full, []string{section})
		without := full
		without.DataRead, without.OperationsServable, without.GraphQLServable, without.GateComposed = false, false, false, false
		other := BuildDataCatalog(cat, without, []string{section})
		a, _ := json.Marshal([]any{with.Subjects, with.Relationships, with.Limits})
		b, _ := json.Marshal([]any{other.Subjects, other.Relationships, other.Limits})
		if string(a) != string(b) {
			t.Errorf("ungated section %s differs by caller gating", section)
		}
	}
}

func operationsEmpty(d DataCatalog) (bool, string) {
	o := d.Operations
	if o == nil {
		return false, "no operations section"
	}
	ok := !o.Available && o.Reason != "" && len(o.Operations) == 0 && len(o.NotServed) == 0 && len(o.RefusedShapes) == 0
	return ok, strings.Join([]string{"operations", itoa(len(o.Operations)), "not_served", itoa(len(o.NotServed)), "refused_shapes", itoa(len(o.RefusedShapes))}, " ")
}

func schemaEmpty(d DataCatalog) (bool, string) {
	s := d.Schema
	if s == nil {
		return false, "no schema section"
	}
	ok := !s.Available && s.Reason != "" && len(s.Roots) == 0 && s.SDL == "" && len(s.RefusedRootFields) == 0
	return ok, strings.Join([]string{"roots", itoa(len(s.Roots)), "sdl", itoa(len(s.SDL)), "refused", itoa(len(s.RefusedRootFields))}, " ")
}

func factsEmpty(d DataCatalog) (bool, string) {
	f := d.Facts
	if f == nil {
		return false, "no facts section"
	}
	return !f.Served && len(f.Kinds) == 0, "kinds " + itoa(len(f.Kinds))
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
