package v1

import "strconv"

// ContextFabricAnswerNote names a field a reader sees as one line, in the
// service's words (CHAOS-12487, CHAOS-4690). Like the correction note, a note is
// one word for one result and no table row: a consumer looks up Key, and a field
// with no key in this result shows nothing. The raw field stays in the fold.
type ContextFabricAnswerNote string

const (
	ContextFabricAnswerNoteLimitationsDisplaced ContextFabricAnswerNote = "limitations_displaced"
	// ContextFabricAnswerNoteGeneratedAt is the lead word of the date. The
	// consumer formats the date for its reader.
	ContextFabricAnswerNoteGeneratedAt       ContextFabricAnswerNote = "generated_at"
	ContextFabricAnswerNoteReused            ContextFabricAnswerNote = "reused"
	ContextFabricAnswerNoteRetrievalDegraded ContextFabricAnswerNote = "retrieval_degraded"
	ContextFabricAnswerNoteSummaryCorrected  ContextFabricAnswerNote = "summary_corrected"
	ContextFabricAnswerNoteJudgmentMismatch  ContextFabricAnswerNote = "judgment_mismatch"
	ContextFabricAnswerNotePopulation        ContextFabricAnswerNote = "population"
)

var contextFabricAnswerNoteNames = [...]ContextFabricAnswerNote{
	ContextFabricAnswerNoteLimitationsDisplaced,
	ContextFabricAnswerNoteGeneratedAt,
	ContextFabricAnswerNoteReused,
	ContextFabricAnswerNoteRetrievalDegraded,
	ContextFabricAnswerNoteSummaryCorrected,
	ContextFabricAnswerNoteJudgmentMismatch,
	ContextFabricAnswerNotePopulation,
}

// ContextFabricAnswerNoteNames returns the served notes in published order.
func ContextFabricAnswerNoteNames() [len(contextFabricAnswerNoteNames)]ContextFabricAnswerNote {
	return contextFabricAnswerNoteNames
}

// Key is the note's vocabulary_labels key.
func (note ContextFabricAnswerNote) Key() string {
	return string(note) + ":note"
}

// contextFabricAnswerNoteWords holds the notes whose words do not read a count.
// summary_corrected says what the field says: the service corrected the source
// state and the summary is never edited, so the summary is what may be out of
// step. judgment_mismatch reads no judgment: the question's own words are free
// text, and a word here must not restate them.
var contextFabricAnswerNoteWords = map[ContextFabricAnswerNote]string{
	ContextFabricAnswerNoteGeneratedAt:       "Answered",
	ContextFabricAnswerNoteReused:            "This is a stored answer, not a new investigation.",
	ContextFabricAnswerNoteRetrievalDegraded: "Subject matching ran with one method unavailable, so fewer subjects may have been considered.",
	ContextFabricAnswerNoteSummaryCorrected:  "The service corrected this driver's source state, so its summary may not reflect it.",
	ContextFabricAnswerNoteJudgmentMismatch:  "This ranking does not support the judgment you asked for.",
}

// ContextFabricAnswerNoteWord returns the words of a note that reads no count.
func ContextFabricAnswerNoteWord(note ContextFabricAnswerNote) (string, bool) {
	word, ok := contextFabricAnswerNoteWords[note]
	return word, ok
}

// ContextFabricLimitationsDisplacedNote states how many limitations an answer
// left out. ok is false for a count that says nothing was left out, so missing
// never reads as zero.
func ContextFabricLimitationsDisplacedNote(count int) (string, bool) {
	switch {
	case count < 1:
		return "", false
	case count == 1:
		return "1 limitation was left out of this answer.", true
	}
	return strconv.Itoa(count) + " limitations were left out of this answer.", true
}

// ContextFabricCohortPopulationNote states the size of the list a cohort was cut
// from. A lower bound reads "at least", never as an exact count. ok is false when
// the read measured no population.
func ContextFabricCohortPopulationNote(population int, lowerBound bool) (string, bool) {
	if population < 1 {
		return "", false
	}
	bound, noun := "", " members"
	if lowerBound {
		bound = contextFabricWorkItemListedAtLeast
	}
	if population == 1 {
		noun = " member"
	}
	return "The full list has " + bound + strconv.Itoa(population) + noun + ".", true
}
