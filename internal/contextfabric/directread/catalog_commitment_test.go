package directread

import (
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCatalogListsCommitmentForAProjectWithItsWindowAndNoJudgmentField(t *testing.T) {
	catalog := factsCatalogFor(ClassUnrestricted, productionCapabilities(), true)
	if catalog.Facts == nil {
		t.Fatal("catalog has no facts section")
	}
	for _, entry := range catalog.Facts.Kinds {
		if entry.Kind != string(contractsv1.ContextFabricFactCommitment) {
			continue
		}
		if !slices.Equal(entry.SubjectKinds, []string{"project"}) {
			t.Errorf("commitment subject kinds = %v, want [project]", entry.SubjectKinds)
		}
		if entry.MaxWindowDays != contractsv1.DefaultMaxRangeDays {
			t.Errorf("commitment max_window_days = %d, want %d", entry.MaxWindowDays, contractsv1.DefaultMaxRangeDays)
		}
		for _, field := range []string{"commitment_status", "target_date", "items_past_due_open", "next_sprint_end", "sprint_kind"} {
			if !slices.Contains(entry.Fields, field) {
				t.Errorf("commitment fields %v lack %q", entry.Fields, field)
			}
		}
		for _, field := range []string{"at_risk", "risk", "score", "verdict"} {
			if slices.Contains(entry.Fields, field) {
				t.Errorf("commitment fields carry the judgment field %q", field)
			}
		}
		return
	}
	t.Fatal("catalog does not list commitment")
}
