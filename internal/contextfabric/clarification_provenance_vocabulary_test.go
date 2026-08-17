package contextfabric

import (
	"regexp"
	"sort"
	"testing"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// provenanceVocabularyCheckPattern locates migration 0016's
// ck_acr_cf_clarification_selections_provenance_vocabulary CHECK and
// captures the quoted values inside its IN (...) list. It is deliberately
// loose about whitespace/comments between "selection_provenance IN (" and
// the closing paren, but exact about the column name, so a rename of the
// column or the constraint drops this test to zero matches (a loud
// failure) rather than silently matching nothing.
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
		t.Fatal("migration 0016 no longer contains a `selection_provenance IN (...)` CHECK -- this test, contextfabric's provenance constants, and pgclarification's knownSelectionProvenanceValues must all change together or not at all")
	}
	var values []string
	for _, v := range provenanceVocabularyValuePattern.FindAllSubmatch(match[1], -1) {
		values = append(values, string(v[1]))
	}
	return values
}

// TestClarificationSelectionProvenanceVocabulary_PinnedToMigration0016 is
// sol review NEW-1's enum-drift guard, contextfabric's half: the closed
// provenance vocabulary is deliberately duplicated in THREE places (Go
// constants here, pgclarification's own knownSelectionProvenanceValues
// map, and migration 0016's CHECK constraint) rather than shared/imported
// -- each one's own doc comment explains why (independent validation that
// does not silently drift even if another layer's shape changes). That
// independence is only safe if something keeps them in sync; this test
// (and pgclarification's own mirror of it) is that something. Any of the
// three sets going out of sync with migration 0016 -- the single SQL
// source of truth an actual INSERT is checked against -- fails here.
func TestClarificationSelectionProvenanceVocabulary_PinnedToMigration0016(t *testing.T) {
	fromMigration := mustReadProvenanceVocabularyFromMigration(t)
	fromGo := []string{
		clarificationProvenanceWebAssertion, clarificationProvenanceMCP,
		clarificationProvenanceWorkbench, clarificationProvenanceOther,
	}
	sort.Strings(fromMigration)
	sort.Strings(fromGo)
	if len(fromMigration) == 0 {
		t.Fatal("parsed zero values out of migration 0016's provenance CHECK -- the regex itself is broken, not the vocabulary")
	}
	if len(fromMigration) != len(fromGo) {
		t.Fatalf("migration 0016 CHECK lists %v, contextfabric's clarificationProvenance* constants list %v -- these must match exactly (sol review NEW-1)", fromMigration, fromGo)
	}
	for i := range fromMigration {
		if fromMigration[i] != fromGo[i] {
			t.Fatalf("migration 0016 CHECK lists %v, contextfabric's clarificationProvenance* constants list %v -- these must match exactly (sol review NEW-1)", fromMigration, fromGo)
		}
	}
}
