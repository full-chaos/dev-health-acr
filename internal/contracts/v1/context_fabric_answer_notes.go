package v1

import (
	"strconv"
	"time"
)

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
	ContextFabricAnswerNoteTemporal          ContextFabricAnswerNote = "temporal"
)

var contextFabricAnswerNoteNames = [...]ContextFabricAnswerNote{
	ContextFabricAnswerNoteLimitationsDisplaced,
	ContextFabricAnswerNoteGeneratedAt,
	ContextFabricAnswerNoteReused,
	ContextFabricAnswerNoteRetrievalDegraded,
	ContextFabricAnswerNoteSummaryCorrected,
	ContextFabricAnswerNoteJudgmentMismatch,
	ContextFabricAnswerNotePopulation,
	ContextFabricAnswerNoteTemporal,
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

// ContextFabricTemporalNote states the time a past answer speaks for, from the
// label alone (CHAOS-12517), so it needs no help from the prose. ok is false for a
// label that names no past time: a current answer has no line.
//
// The sentence says only what the label proves:
//
//   - Grain none: no source spoke, so "this answer speaks for" would be false.
//   - A day-grain range that collapsed to zero width covers no full day of the
//     span asked about (effectiveTimeContext).
//   - Any other label states its effective time, says when that is not the time
//     asked about, and never reads as complete when coverage_complete is false.
func ContextFabricTemporalNote(label ContextFabricTemporalLabel) (string, bool) {
	requested, ok := contextFabricTimeContextWords(label.Requested, false)
	if !ok {
		return "", false
	}
	// Only an exact answer reads an exact midnight: at day grain it is the day.
	effective, ok := contextFabricTimeContextWords(label.Effective, label.Grain == ContextFabricGrainInstant)
	if !ok {
		return "", false
	}
	switch label.Grain {
	case ContextFabricGrainNone:
		return "No source could speak for " + requested + ".", true
	case ContextFabricGrainDay, ContextFabricGrainInstant:
	default:
		return "", false
	}
	if contextFabricRangeCollapsed(label) {
		return "No full day of " + requested + " could be read.", true
	}
	note := "This answer speaks for " + effective
	if !contextFabricSameTime(label.Requested, label.Effective) {
		note += ", not the " + requested + " you asked about"
	}
	if !label.CoverageComplete {
		note += ", and not every source covers it"
	}
	return note + ".", true
}

// contextFabricRangeCollapsed reports a range with no full day in it. Effective
// only collapses to zero width at day grain, so the width says it. A zero-width
// ask on a day boundary is that one whole day, and does not count.
func contextFabricRangeCollapsed(label ContextFabricTemporalLabel) bool {
	if label.Requested.Axis != ContextFabricTemporalRange || label.Effective.Axis != ContextFabricTemporalRange {
		return false
	}
	asked := *label.Requested.Start
	oneDay := asked.Equal(*label.Requested.End) && contextFabricIsDayBoundary(asked)
	return label.Effective.Start.Equal(*label.Effective.End) && !oneDay
}

// contextFabricSameTime compares the instants, never their words: two different
// times can print alike. It reads only contexts that already have their words.
func contextFabricSameTime(requested, effective ContextFabricTimeContext) bool {
	if requested.AsOf != nil && effective.AsOf != nil {
		return requested.AsOf.Equal(*effective.AsOf)
	}
	if requested.Start != nil && effective.Start != nil {
		return requested.Start.Equal(*effective.Start) && requested.End.Equal(*effective.End)
	}
	return false
}

// contextFabricTimeContextWords words a past time. exact reads an instant at a
// day boundary with its time. ok is false for the current axis, and for a context
// that lacks the instants its axis requires.
func contextFabricTimeContextWords(context ContextFabricTimeContext, exact bool) (string, bool) {
	switch context.Axis {
	case ContextFabricTemporalValidTime, ContextFabricTemporalObservedTime:
		if context.AsOf == nil {
			return "", false
		}
		return contextFabricInstantWords(*context.AsOf, exact), true
	case ContextFabricTemporalRange:
		if context.Start == nil || context.End == nil {
			return "", false
		}
		start := contextFabricInstantWords(*context.Start, exact)
		if context.Start.Equal(*context.End) {
			return start, true
		}
		return start + " to " + contextFabricInstantWords(*context.End, exact), true
	default:
		return "", false
	}
}

// contextFabricInstantWords reads a day as a date, unless exact, and any other
// instant with its time in UTC, to the second, so a day-grain answer never reads
// as more exact than it is and an exact one never as less.
func contextFabricInstantWords(instant time.Time, exact bool) string {
	utc := instant.UTC()
	switch {
	case !exact && contextFabricIsDayBoundary(utc):
		return utc.Format(time.DateOnly)
	case utc.Second() == 0 && utc.Nanosecond() == 0:
		return utc.Format("2006-01-02 15:04") + " UTC"
	}
	return utc.Format(time.DateTime) + " UTC"
}

func contextFabricIsDayBoundary(instant time.Time) bool {
	return instant.Equal(instant.Truncate(24 * time.Hour))
}
