package v1

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// CHAOS-12499.

var causalWord = regexp.MustCompile(`(?i)\b(cause[sd]?|causing|because|due to|leads? to|led to|results? in|resulted in|trigger(s|ed)?|drives?|drove|responsible|root|broke|breaks)\b`)

func relationshipTypeEntries(t *testing.T) []vocabularyEntry {
	t.Helper()
	entries := contextFabricVocabularyEntries(ContextFabricVocabularyRelationshipType)
	if vocabulary := ContextFabricRelationshipTypeVocabulary(); len(entries) != len(vocabulary) {
		t.Fatalf("%d words for %d relationship types", len(entries), len(vocabulary))
	}
	return entries
}

// An edge says two things are linked, never that one caused the other (North
// Star check 7).
func TestNoEdgeWordSaysCause(t *testing.T) {
	for _, entry := range relationshipTypeEntries(t) {
		if causalWord.MatchString(entry.label) {
			t.Errorf("%s reads %q, which says cause", entry.value, entry.label)
		}
	}
}

// The guard itself: each of these would read as cause on a hop.
func TestTheCauseGuardFails(t *testing.T) {
	for _, word := range []string{"caused incident", "causes", "led to incident", "results in", "triggered", "due to", "root cause of", "drives"} {
		if !causalWord.MatchString(word) {
			t.Errorf("%q passed the guard", word)
		}
	}
}

func TestNoEdgeWordIsARawToken(t *testing.T) {
	for _, entry := range relationshipTypeEntries(t) {
		if strings.Contains(entry.label, "_") || entry.label != strings.ToLower(entry.label) || strings.Contains(entry.label, entry.value) {
			t.Errorf("%s reads %q, which is a raw token", entry.value, entry.label)
		}
	}
}

// The list, the word table and the published schema name one set. The general
// totality tests take a union of sources, so a member dropped from the list alone
// would still pass them.
func TestTheListTheTableAndTheSchemaNameOneSet(t *testing.T) {
	relationships := ContextFabricRelationshipTypeVocabulary()
	listed := slices.Sorted(slices.Values(closedVocabularyStrings(relationships[:])))
	schema := schemaDescribedValues(t, commonSchemaDefs(t), "RelationshipEdge", "type")
	slices.Sort(schema)
	var worded []string
	for _, entry := range contextFabricVocabularyEntries(ContextFabricVocabularyRelationshipType) {
		worded = append(worded, entry.value)
	}
	slices.Sort(worded)
	if !slices.Equal(listed, schema) || !slices.Equal(listed, worded) {
		t.Fatalf("list %v\nschema %v\nwords %v", listed, schema, worded)
	}
}

func TestTheValidatorReadsTheOneList(t *testing.T) {
	for _, relationship := range ContextFabricRelationshipTypeVocabulary() {
		if !ValidContextFabricRelationshipType(relationship) {
			t.Errorf("%q is listed and refused", relationship)
		}
	}
	if ValidContextFabricRelationshipType("A_FUTURE_EDGE") {
		t.Error("a value off the list was accepted")
	}
}
