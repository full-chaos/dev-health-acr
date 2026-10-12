package devhealthfacts_test

import (
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func declaredStringFieldNames(providers []contextfabric.FactProvider) map[string][]string {
	declared := map[string][]string{}
	note := func(name string, kind contextfabric.FactKind) {
		declared[name] = append(declared[name], string(kind))
	}
	for _, provider := range providers {
		capability := provider.Capability()
		for _, field := range capability.Fields {
			if field.Type == contextfabric.FactFieldString {
				note(field.Name, capability.Kind)
			}
			for _, column := range field.Columns {
				if column.Type == contextfabric.FactFieldString {
					note(column.Name, capability.Kind)
				}
			}
		}
	}
	return declared
}

func TestEveryDeclaredStringFieldHasAProseClass(t *testing.T) {
	declared := declaredStringFieldNames(devhealthfacts.NewProviders(&fakeClient{}))
	if len(declared) < 80 {
		t.Fatalf("census saw %d declared string fields, want the whole catalogue", len(declared))
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := contractsv1.ContextFabricStringFieldClassOf(name); !ok {
			t.Errorf("declared string field %q (kinds %v) has no prose class: classify it as text, token, numeric_in_string or never_in_prose in contractsv1", name, declared[name])
		}
	}
}
