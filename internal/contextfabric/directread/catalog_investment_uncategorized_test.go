package directread

import (
	"slices"
	"testing"
)

func TestCatalogInvestmentKindListsTheUncategorizedCoverageFields(t *testing.T) {
	t.Parallel()
	catalog := factsCatalogFor(ClassUnrestricted, productionCapabilities(), true)
	if catalog.Facts == nil {
		t.Fatal("catalog has no facts section")
	}
	for _, entry := range catalog.Facts.Kinds {
		if entry.Kind != "investment" {
			continue
		}
		for _, field := range []string{"work_unit_count", "uncategorized_unit_count", "uncategorized_unit_share"} {
			if !slices.Contains(entry.Fields, field) {
				t.Errorf("investment fields %v lack %s", entry.Fields, field)
			}
		}
		return
	}
	t.Fatal("catalog lists no investment kind")
}
