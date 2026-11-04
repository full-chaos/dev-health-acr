package contextfabric

import (
	"regexp"
	"time"
)

// CHAOS-13010. The interpreter prompt copies only the request's own time bounds
// ("Never output a date or time that the request does not carry"), so a question
// that states its period as two ISO dates came back as time_context {axis: current}
// and the read ran on the current axis. The dates are in the question itself, so
// the engine reads them: a closed grammar, one span per question, nothing guessed.

var (
	questionISODate = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	// questionDateSpan is "<date> to|through|until|and|-|en dash|em dash <date>":
	// "between X and Y", "from X to Y" and "(X to Y)" all contain it.
	questionDateSpan = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\s*(?:to|through|until|and|-|\x{2013}|\x{2014})\s*(\d{4}-\d{2}-\d{2})\b`)
)

// explicitQuestionDateRange is the range a question states as two ISO dates:
// the first day from its start and the last day to its final second. ok is false
// for a question with no such span, with two different spans, with a date outside
// a span, with a date that does not exist, with an end before its start, or with
// a span wider than the time axis answers (maxHistoricalRangeDays): in each the
// question states no range and the interpretation stands.
func explicitQuestionDateRange(question string) (start, end time.Time, ok bool) {
	spans := questionDateSpan.FindAllStringSubmatch(question, -1)
	if len(spans) == 0 {
		return time.Time{}, time.Time{}, false
	}
	for _, span := range spans[1:] {
		if span[1] != spans[0][1] || span[2] != spans[0][2] {
			return time.Time{}, time.Time{}, false
		}
	}
	// Every ISO date of the question belongs to the span.
	if len(questionISODate.FindAllString(question, -1)) != 2*len(spans) {
		return time.Time{}, time.Time{}, false
	}
	first, err := time.Parse(time.DateOnly, spans[0][1])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	last, err := time.Parse(time.DateOnly, spans[0][2])
	if err != nil || last.Before(first) {
		return time.Time{}, time.Time{}, false
	}
	end = last.Add(24*time.Hour - time.Second)
	if end.Sub(first) > maxHistoricalRangeDays*24*time.Hour {
		return time.Time{}, time.Time{}, false
	}
	return first, end, true
}

// withQuestionDateRange puts the range a question states on an interpretation
// that read the question as current, when the caller's own time is current too.
// A range the model returned, an as-of axis and a caller's non-current axis all
// stand. applied reports the one case that changed.
func withQuestionDateRange(question string, interpretation InterpretedQuestion, requestAxis TemporalAxis) (InterpretedQuestion, bool) {
	if interpretation.TimeContext.Axis != TemporalCurrent || requestAxis != TemporalCurrent {
		return interpretation, false
	}
	start, end, ok := explicitQuestionDateRange(question)
	if !ok {
		return interpretation, false
	}
	interpretation.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &end}
	return interpretation, true
}
