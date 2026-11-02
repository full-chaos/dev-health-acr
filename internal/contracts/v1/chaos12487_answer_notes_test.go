package v1

import (
	"maps"
	"strings"
	"testing"
)

// CHAOS-12487.

// allAnswerWords is every word the ticket adds, by key, at a count that composes.
func allAnswerWords(t *testing.T) map[string]string {
	t.Helper()
	words := map[string]string{}
	for _, note := range ContextFabricAnswerNoteNames() {
		word, ok := ContextFabricAnswerNoteWord(note)
		switch note {
		case ContextFabricAnswerNoteLimitationsDisplaced:
			word, ok = ContextFabricLimitationsDisplacedNote(2)
		case ContextFabricAnswerNotePopulation:
			word, ok = ContextFabricCohortPopulationNote(240, true)
		case ContextFabricAnswerNoteTemporal:
			word, ok = ContextFabricTemporalNote(temporalNoteRow{requested: "2026-03-01", effective: "2026-02-28", grain: ContextFabricGrainDay}.label(t))
		}
		if !ok {
			t.Fatalf("note %q has no word", note)
		}
		words[note.Key()] = word
	}
	for _, vocabulary := range []ContextFabricVocabulary{ContextFabricVocabularyDriverSourceState, ContextFabricVocabularyCohortScoreMeaning} {
		for _, entry := range contextFabricVocabularyEntries(vocabulary) {
			words[ContextFabricVocabularyLabelKey(vocabulary, entry.value)] = entry.label
		}
	}
	return words
}

// rawAnswerTokens is what a reader must never see in these words: each field
// name on the wire and each value of the two vocabularies. The match is exact,
// so a sentence may begin with a plain word that a value also spells.
func rawAnswerTokens() []string {
	tokens := []string{"source_state", "score_meaning", "population_lower_bound", "subject_resolution", "cohort.", "drivers["}
	for _, note := range ContextFabricAnswerNoteNames() {
		tokens = append(tokens, string(note))
	}
	for _, vocabulary := range []ContextFabricVocabulary{ContextFabricVocabularyDriverSourceState, ContextFabricVocabularyCohortScoreMeaning} {
		for _, entry := range contextFabricVocabularyEntries(vocabulary) {
			if entry.value != entry.label {
				tokens = append(tokens, entry.value)
			}
		}
	}
	return tokens
}

func TestNoRawFieldNameOrVocabularyValueIsInAServedWord(t *testing.T) {
	words := allAnswerWords(t)
	if len(words) < 18 {
		t.Fatalf("%d words: this measured too little", len(words))
	}
	for key, word := range words {
		if strings.Contains(word, "_") {
			t.Errorf("%s reads %q, which holds a raw token", key, word)
		}
		for _, token := range rawAnswerTokens() {
			if strings.Contains(word, token) {
				t.Errorf("%s reads %q, which holds the raw token %q", key, word, token)
			}
		}
	}
}

func TestEveryNoteHasTheServedKeyShapeAndIsNoTable(t *testing.T) {
	names := ContextFabricVocabularyNames()
	for _, note := range ContextFabricAnswerNoteNames() {
		key := note.Key()
		if !vocabularyKeyShape.MatchString(key) || len(key) > ContextFabricVocabularyLabelKeyMaxLength {
			t.Errorf("key %q does not have the served shape", key)
		}
		for _, name := range names {
			if string(name) == string(note) {
				t.Errorf("%q is a vocabulary name, but a note is no table row", note)
			}
		}
	}
}

func TestEveryNoteWordIsAValidLabel(t *testing.T) {
	for key, word := range allAnswerWords(t) {
		if strings.TrimSpace(word) != word || !stringLengthBetween(word, 1, ContextFabricCoverageDetailLabelMaxLength) {
			t.Errorf("%s: %q is not a valid label", key, word)
		}
	}
}

func TestACountThatSaysNothingServesNoNote(t *testing.T) {
	for _, count := range []int{0, -1} {
		if word, ok := ContextFabricLimitationsDisplacedNote(count); ok {
			t.Errorf("%d limitations read %q", count, word)
		}
		for _, lowerBound := range []bool{false, true} {
			if word, ok := ContextFabricCohortPopulationNote(count, lowerBound); ok {
				t.Errorf("a population of %d (lower bound %v) reads %q", count, lowerBound, word)
			}
		}
	}
}

// A lower bound is never an exact count, at any size, and an exact count is
// never a lower bound.
func TestALowerBoundNeverReadsAsAnExactCount(t *testing.T) {
	for _, population := range []int{1, 2, 240} {
		bounded, _ := ContextFabricCohortPopulationNote(population, true)
		exact, _ := ContextFabricCohortPopulationNote(population, false)
		if !strings.Contains(bounded, "at least") || strings.Contains(exact, "at least") {
			t.Errorf("population %d: bounded %q, exact %q", population, bounded, exact)
		}
	}
}

func TestTheStatusTierKeepsTheJudgmentMismatchAndTemporalNotes(t *testing.T) {
	mismatch, temporal := ContextFabricAnswerNoteJudgmentMismatch.Key(), ContextFabricAnswerNoteTemporal.Key()
	labels := map[string]string{"answer_status:partial": "Partial answer", mismatch: "A sentence.", temporal: "A time.", ContextFabricAnswerNoteReused.Key(): "Another."}
	want := map[string]string{"answer_status:partial": "Partial answer", mismatch: "A sentence.", temporal: "A time."}
	if got := ContextFabricStatusVocabularyLabels(labels); !maps.Equal(got, want) {
		t.Fatalf("the status tier holds %v, want %v", got, want)
	}
}
