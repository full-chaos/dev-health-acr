package directread

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The metrics kind lists commit_counts_basis in data_catalog, and the
// declaration scopes it to team and project facts: the repository fact is not
// share-weighted and does not carry it.
func TestCatalogListsCommitCountsBasisOnTheMetricsKindForTeamAndProjectOnly(t *testing.T) {
	capabilities := productionCapabilities()
	catalog := factsCatalogFor(ClassUnrestricted, capabilities, true)
	var fields []string
	for _, entry := range catalog.Facts.Kinds {
		if entry.Kind == string(contextfabric.FactMetrics) {
			fields = entry.Fields
		}
	}
	if !slices.Contains(fields, "commit_counts_basis") {
		t.Fatalf("metrics fields %v lack commit_counts_basis", fields)
	}
	seen := map[contextfabric.SubjectKind]bool{}
	for _, capability := range capabilities {
		if capability.Kind != contextfabric.FactMetrics {
			continue
		}
		for _, field := range capability.Fields {
			if field.Name != "commit_counts_basis" {
				continue
			}
			for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository} {
				if field.AppliesTo(kind) {
					seen[kind] = true
				}
			}
		}
	}
	if !seen[contextfabric.SubjectTeam] || !seen[contextfabric.SubjectProject] || seen[contextfabric.SubjectRepository] {
		t.Fatalf("commit_counts_basis declared for %v, want team and project only", seen)
	}
}
