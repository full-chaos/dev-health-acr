package directread

import (
	"slices"
	"testing"
)

// The catalogue lists the backlogState path that makes capacityForecast and capacityForecasts
// declared_partial in disclosure_fields (CHAOS-14560), and every other operation lists exactly its
// policy Disclosure paths, in order, as before.
func TestDataCatalogDisclosureFieldsNameTheBacklogStateRule(t *testing.T) {
	cat := catalogFor(t, ClassUnrestricted, true, true)
	want := map[string][]string{
		"capacityForecast":  {"capacityForecast.backlogState"},
		"capacityForecasts": {"capacityForecasts.edges[*].node.backlogState"},
	}
	policy := loadDefault(t)
	seen := 0
	for _, entry := range cat.Operations.Operations {
		if fields, ok := want[entry.Name]; ok {
			seen++
			if !slices.Equal(entry.DisclosureFields, fields) {
				t.Errorf("%s disclosure_fields = %v, want %v", entry.Name, entry.DisclosureFields, fields)
			}
			continue
		}
		op, _ := policy.Lookup(entry.Name)
		paths := []string{}
		for _, d := range op.Disclosure {
			paths = append(paths, d.Path)
		}
		if !slices.Equal(entry.DisclosureFields, paths) {
			t.Errorf("%s disclosure_fields = %v, want its policy disclosure %v", entry.Name, entry.DisclosureFields, paths)
		}
	}
	if seen != len(want) {
		t.Errorf("catalogue lists %d of the %d backlogState operations", seen, len(want))
	}
}
