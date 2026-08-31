package devhealthfacts_test

import (
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// TestGeneratedDimensionFactKindRankingFamilyTableMatchesDoc is CHAOS-4468's
// third deliverable: "the dimension <-> FactKind <-> ranking-family mapping
// table in acr/docs" is GENERATED from the live registry (design doc §5.3),
// never hand-maintained beside it. This test is the thing that makes that
// true: it builds the table from devhealthfacts.NewProviders' own
// capabilities and fails CI the moment the checked-in doc drifts from what
// the registry actually declares.
func TestGeneratedDimensionFactKindRankingFamilyTableMatchesDoc(t *testing.T) {
	providers := devhealthfacts.NewProviders(nil)
	capabilities := make([]contextfabric.FactCapability, 0, len(providers))
	for _, provider := range providers {
		capabilities = append(capabilities, provider.Capability())
	}
	rows := contextfabric.GenerateDimensionFactKindRankingFamilyTable(capabilities)
	if len(rows) != 21 {
		t.Fatalf("got %d rows, want 21 (one per registered FactKind -- FactEvidence is deliberately unregistered)", len(rows))
	}
	for _, row := range rows {
		if !contextfabric.ValidHealthDimension(row.Dimension) {
			t.Errorf("FactKind %q has invalid dimension %q", row.Kind, row.Dimension)
		}
	}
	got := contextfabric.RenderDimensionFactKindRankingFamilyMarkdown(rows)
	want, err := os.ReadFile("../../../docs/design/context-fabric-dimension-factkind-ranking-family.md")
	if err != nil {
		t.Fatalf("reading checked-in doc: %v", err)
	}
	if got != tableSection(string(want)) {
		t.Fatalf("docs/design/context-fabric-dimension-factkind-ranking-family.md's table has drifted from the registry.\nGenerated:\n%s\nChecked in (table section):\n%s", got, tableSection(string(want)))
	}
}

// tableSection extracts the markdown table body (everything from the header
// row on) so the doc file may carry prose above the table without breaking
// the byte-for-byte comparison.
func tableSection(doc string) string {
	marker := "| dimension | fact kind | capability | ranking family |"
	idx := indexOf(doc, marker)
	if idx < 0 {
		return doc
	}
	return doc[idx:]
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
