package directread

import (
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// CHAOS-7148: data_catalog's facts section must describe exactly what
// read_facts serves. The capabilities are the PRODUCTION declarations, and
// the read_facts side is FactsReader.validate itself (the code that refuses
// a kind), so a drift in either direction fails here.

func productionCapabilities() []contextfabric.FactCapability {
	var out []contextfabric.FactCapability
	for _, provider := range devhealthfacts.NewProviders(nil) {
		out = append(out, provider.Capability())
	}
	return out
}

func factsCatalogFor(class PrincipalClass, capabilities []contextfabric.FactCapability, wired bool) DataCatalog {
	return BuildDataCatalog(nil, CatalogCaller{
		PrincipalClass: class, Scopes: []string{"context:read", "data:read"}, DataRead: true,
		FactsServable: wired, FactCapabilities: capabilities,
	}, []string{CatalogSectionFacts})
}

func TestChaos7148CatalogFactKindsEqualWhatReadFactsServes(t *testing.T) {
	capabilities := productionCapabilities()
	byKind := map[contextfabric.FactKind]contextfabric.FactCapability{}
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	catalog := factsCatalogFor(ClassUnrestricted, capabilities, true)
	if catalog.Facts == nil || !catalog.Facts.Served || catalog.Facts.Note != CatalogFactsServedNote {
		t.Fatalf("facts section %+v", catalog.Facts)
	}
	listed := map[string]bool{}
	for _, entry := range catalog.Facts.Kinds {
		listed[entry.Kind] = true
		if len(entry.Fields) == 0 || len(entry.SubjectKinds) == 0 {
			t.Fatalf("kind %s lists no fields or subject kinds: %+v", entry.Kind, entry)
		}
	}
	// read_facts side: ask its own validate about every registered kind.
	reader := &FactsReader{}
	served := map[string]bool{}
	for kind := range byKind {
		plan, err := reader.validate(FactsRequest{
			Kinds:    []string{string(kind)},
			Subjects: []RequestSubject{{Kind: "repository", CanonicalID: "repository:x"}},
		}, byKind)
		if err != nil {
			t.Fatalf("validate %s: %v", kind, err)
		}
		if len(plan.kinds) == 1 {
			served[string(kind)] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("read_facts serves no kind: the test measured nothing")
	}
	if len(served) < 21 {
		t.Fatalf("read_facts serves %d kinds, want at least the 9 aggregate + 12 entity kinds", len(served))
	}
	var missing, extra []string
	for kind := range served {
		if !listed[kind] {
			missing = append(missing, kind)
		}
	}
	for kind := range listed {
		if !served[kind] {
			extra = append(extra, kind)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("catalog and read_facts disagree: served but not listed %v; listed but not served %v", missing, extra)
	}
}

func TestChaos7148CatalogFactsSectionIsTheSameForEveryGrantClass(t *testing.T) {
	capabilities := productionCapabilities()
	unrestricted := factsCatalogFor(ClassUnrestricted, capabilities, true).Facts
	restricted := factsCatalogFor(ClassRestricted, capabilities, true).Facts
	if len(unrestricted.Kinds) == 0 || len(restricted.Kinds) != len(unrestricted.Kinds) {
		t.Fatalf("restricted %d kinds vs unrestricted %d", len(restricted.Kinds), len(unrestricted.Kinds))
	}
	for i := range restricted.Kinds {
		if restricted.Kinds[i].Kind != unrestricted.Kinds[i].Kind {
			t.Fatalf("kind %d differs", i)
		}
	}
}

func TestChaos7148CatalogSaysNotAvailableOnlyWhenReadFactsIsNotWired(t *testing.T) {
	catalog := factsCatalogFor(ClassUnrestricted, productionCapabilities(), false)
	if catalog.Facts.Served || catalog.Facts.Note != CatalogFactsNote || len(catalog.Facts.Kinds) != 0 {
		t.Fatalf("unwired: %+v", catalog.Facts)
	}
	empty := factsCatalogFor(ClassUnrestricted, nil, true)
	if !empty.Facts.Served {
		t.Fatalf("wired reader with an empty registry must still say served: %+v", empty.Facts)
	}
}
