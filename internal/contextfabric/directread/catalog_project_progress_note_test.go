package directread

import (
	"strings"
	"testing"
)

func TestCatalogFactsNameProjectProgressForActualCompletion(t *testing.T) {
	for _, want := range []string{
		"actual_completion with project subjects answers project progress",
		"share of counted work items completed",
		"authorized to see",
		"current state only and not windowed",
	} {
		if !strings.Contains(CatalogFactsServedNote, want) {
			t.Fatalf("served note lacks %q", want)
		}
	}
}

// A deprecated table is said in the catalog: read_facts serves it until a
// contract version drops it (CHAOS-12533).
func TestTheCatalogSaysMapCoordinatesIsDeprecated(t *testing.T) {
	for _, want := range []string{"map_coordinates is deprecated", "area_breakdown", "team_breakdown"} {
		if !strings.Contains(CatalogFactsServedNote, want) {
			t.Fatalf("the note does not say %q: %s", want, CatalogFactsServedNote)
		}
	}
}
