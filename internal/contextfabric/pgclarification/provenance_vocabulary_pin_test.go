package pgclarification

import (
	"regexp"
	"sort"
	"testing"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// provenanceVocabularyCheckPattern mirrors contextfabric's own
// (independently written) copy of this pattern -- see this test's doc
// comment for why the duplication itself is intentional.
var provenanceVocabularyCheckPattern = regexp.MustCompile(`selection_provenance\s+IN\s*\(([^)]*)\)`)
var provenanceVocabularyValuePattern = regexp.MustCompile(`'([^']*)'`)

func mustReadProvenanceVocabularyFromMigration(t *testing.T) []string {
	t.Helper()
	content, err := migrations.Files.ReadFile("0016_context_fabric_clarification_selections.sql")
	if err != nil {
		t.Fatalf("read migration 0016: %v", err)
	}
	match := provenanceVocabularyCheckPattern.FindSubmatch(content)
	if match == nil {
		t.Fatal("migration 0016 no longer contains a `selection_provenance IN (...)` CHECK -- this test, pgclarification's knownSelectionProvenanceValues, and contextfabric's provenance constants must all change together or not at all")
	}
	var values []string
	for _, v := range provenanceVocabularyValuePattern.FindAllSubmatch(match[1], -1) {
		values = append(values, string(v[1]))
	}
	return values
}

// TestKnownSelectionProvenanceValues_PinnedToMigration0016 is sol review
// NEW-1's enum-drift guard, pgclarification's half: knownSelectionProvenanceValues
// is deliberately a hand-kept duplicate of migration 0016's CHECK, NOT
// imported from contextfabric (see its own doc comment) -- this test is
// what keeps that deliberate duplication honest. Combined with
// contextfabric's own TestClarificationSelectionProvenanceVocabulary_PinnedToMigration0016
// (which pins contextfabric's Go constants to the SAME migration file),
// the two together transitively pin all three copies of the vocabulary
// (contextfabric constants, this map, migration 0016's CHECK) to each
// other without either package needing to import the other's unexported
// symbols.
func TestKnownSelectionProvenanceValues_PinnedToMigration0016(t *testing.T) {
	fromMigration := mustReadProvenanceVocabularyFromMigration(t)
	fromMap := make([]string, 0, len(knownSelectionProvenanceValues))
	for value := range knownSelectionProvenanceValues {
		fromMap = append(fromMap, value)
	}
	sort.Strings(fromMigration)
	sort.Strings(fromMap)
	if len(fromMigration) == 0 {
		t.Fatal("parsed zero values out of migration 0016's provenance CHECK -- the regex itself is broken, not the vocabulary")
	}
	if len(fromMigration) != len(fromMap) {
		t.Fatalf("migration 0016 CHECK lists %v, knownSelectionProvenanceValues lists %v -- these must match exactly (sol review NEW-1)", fromMigration, fromMap)
	}
	for i := range fromMigration {
		if fromMigration[i] != fromMap[i] {
			t.Fatalf("migration 0016 CHECK lists %v, knownSelectionProvenanceValues lists %v -- these must match exactly (sol review NEW-1)", fromMigration, fromMap)
		}
	}
}
