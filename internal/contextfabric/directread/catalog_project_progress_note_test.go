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
