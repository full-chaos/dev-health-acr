package contextfabric

import (
	"regexp"
	"slices"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// legacyModelPathStatusSentences are the status sentences statusSentence wrote
// before CHAOS-12286. They restated the answer status in its machine word
// (complete, partial, degraded), which a person is never shown (chris
// 2026-10-31). Answers stored before the change carry them in their persisted
// DirectJudgment and DeterministicAnswer, so every served copy recognizes them
// and states the served status's current sentence in their place (none, for
// complete); nothing writes them again. FROZEN: they are the stored texts, so
// an edit here stops a stored answer's sentence from being found.
var legacyModelPathStatusSentences = []string{
	"This investigation is complete.",
	"This investigation is partial: some canonical or graph coverage was unavailable.",
	"This investigation is degraded: coverage was limited.",
}

// limitedReadPrefix opens composeCurrentState's clause naming the drivers whose
// source read was truncated or is out of date. legacyLimitedReadPrefix is the
// completeness word it used before CHAOS-12286, FROZEN for stored answers; the
// two are the same length, so restating one as the other keeps the field
// inside its bound.
const (
	limitedReadPrefix       = " Limited read: "
	legacyLimitedReadPrefix = " Not complete: "
)

// restateServedStatusHead makes the lead prose of a served copy state
// result.Status, the status the document is served with after every server
// downgrade, in ACR's current wording: it runs on every serving point (the
// completeness authority's apply step: fresh, reused and read-by-id answers)
// whether or not the status changed.
//
// Only a head that carries one of the model-path status sentences (current or
// legacy) at a sentence boundary is restated: the composer states it first, or
// after the quick answer that leads the head (a count sentence, a principal
// driver; see composeLeadProse). The rest of the head (quick answer, principal
// drivers, cardinality sentence) is kept. Every other head -- clarification,
// no_match, refusal, a client-written answer -- is composed by its own renderer
// and is left alone.
//
// A complete answer states no status sentence (CHAOS-12286). When that leaves
// its head empty, the head is current_state's first sentence as composed
// (leadProseFallback); when current_state is empty too, the head is served as
// stored, unchanged (LeadProseServedAsStored counts that case). The stored
// document is never rewritten: this is the served copy.
func restateServedStatusHead(result InvestigationResult) InvestigationResult {
	result = restateLegacySourceStateLabels(result)
	if !statusAnswers(result.Status) {
		return result
	}
	fallback := leadProseFallback(result.CurrentState)
	result.DirectJudgment = servedLeadField(result, result.DirectJudgment, fallback, "Principal driver: ", directJudgmentMaxLength)
	result.DeterministicAnswer = servedLeadField(result, result.DeterministicAnswer, fallback, "Principal driver(s): ", deterministicAnswerMaxLength)
	return result
}

// servedLeadField is one lead prose field of a served copy: its status
// sentence restated, a fallback sentence that other content now leads given
// way, and an empty field filled with the fallback -- or, with no fallback,
// the field as stored.
//
// A head with no status sentence was composed for complete, which states none
// (CHAOS-12286): when the served status is lower (a budget lever, the
// completeness authority), the head is recomposed from complete, so the served
// status's sentence stands where the composer states it and the count before
// it is kept. A head that is not a composition of complete is left alone, as is
// the client synthesis text, which states its own status.
func servedLeadField(result InvestigationResult, head, fallback, label string, maxLength int) string {
	want := statusSentence(result.Status, SubjectResolution{})
	lead := quickAnswerSentence(result)
	served := head
	if _, stale := firstStatusSentence(head, afterLead(head, lead), modelPathStatusSentences()); stale != "" {
		served = restateHeadSentence(head, want, lead, maxLength)
	} else if want != "" && head != contractsv1.ContextFabricClientSynthesisAnswer {
		if recomposed, ok := recomposeComposedLead(head, fallback, InvestigationComplete, result.Status, result.Drivers, result.Cohort, result.SubjectResolution, label, maxLength); ok {
			served = recomposed
		}
	}
	served = withoutLeadProseFallback(served, fallback)
	if strings.TrimSpace(served) != "" {
		return served
	}
	if fallback == "" {
		return head
	}
	return truncateAtSentenceBoundary(fallback, maxLength)
}

// LeadProseServedAsStored reports whether a served copy still carries a legacy
// status sentence: an answer whose head had nothing to state in its place (no
// other content, and an empty current_state), served as stored rather than as
// an invalid document. No producing path composes such an answer; the
// completeness authority line counts it (lead_prose_served_as_stored).
func LeadProseServedAsStored(result InvestigationResult) bool {
	if !statusAnswers(result.Status) {
		return false
	}
	lead := quickAnswerSentence(result)
	for _, head := range []string{result.DirectJudgment, result.DeterministicAnswer} {
		if _, stale := firstStatusSentence(head, afterLead(head, lead), legacyModelPathStatusSentences); stale != "" {
			return true
		}
	}
	return false
}

// serverLeadSentences are the sentences the server puts before the composed
// lead prose of an answer: the count sentence (cardinalityAnswerSentence) and
// a single subject's period totals, absences and period scope
// (PeriodTotalSentences, PeriodScopeSentence).
var serverLeadSentences = []*regexp.Regexp{
	regexp.MustCompile(`^Counted (?:at least )?\d+ [^.]*\.$`),
	regexp.MustCompile(`^(?:Partial total|Total) of .+ over the period: \d+, summed from .+\.$`),
	regexp.MustCompile(`^No stored .+ row exists for any day of the period, so no total is stated\.$`),
	regexp.MustCompile(`^The stored .+ rows for the period (?:reached the limit of|could not be read as) .+\.$`),
	regexp.MustCompile(`^Some stored .+ rows were withheld from this caller, so no total is stated\.$`),
	regexp.MustCompile(`^The period is (?:longer than the|the) .+\.$`),
}

// serverLeadSentencesOnly reports whether head is empty or every sentence of
// it is one of serverLeadSentences: the head of a complete answer with no
// principal driver, as the server composes it.
func serverLeadSentencesOnly(head string) bool {
	for rest := strings.TrimSpace(head); rest != ""; {
		sentence := leadProseFallback(rest)
		matched := false
		for _, pattern := range serverLeadSentences {
			if pattern.MatchString(sentence) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
		rest = strings.TrimSpace(strings.TrimPrefix(rest, sentence))
	}
	return true
}

// leadProseFallback is current_state's first sentence, as composed: the head
// of a complete answer that states nothing else. A provider's fact value in it
// ("status: Complete") is evidence and is kept as it is.
func leadProseFallback(currentState string) string {
	text := strings.TrimSpace(currentState)
	for i := 0; i+1 < len(text); i++ {
		if (text[i] == '.' || text[i] == '?' || text[i] == '!') && text[i+1] == ' ' {
			return text[:i+1]
		}
	}
	return text
}

// withoutLeadProseFallback drops the fallback sentence from a head that other
// content now leads (a reused answer gains its count in front of it), and reads
// a head that is only the fallback as empty.
func withoutLeadProseFallback(head, fallback string) string {
	if fallback == "" {
		return head
	}
	if head == fallback {
		return ""
	}
	if strings.HasSuffix(head, " "+fallback) {
		return strings.TrimSpace(strings.TrimSuffix(head, fallback))
	}
	return head
}

// legacyTruncatedQualifier, legacyTruncatedNote and
// legacyTruncatedSummaryLabel are the words driverSourceStateCause gave a
// truncated source read before CHAOS-12289 ("partial", ACR's own state word),
// as the qualifier after a driver title or category, the server note on the
// driver, and the project status summary label. FROZEN: they are the stored
// texts.
const (
	legacyTruncatedQualifier    = " (partial: the source read was truncated)"
	legacyTruncatedNote         = "The service marks this driver as partial: the source read was truncated."
	legacyTruncatedSummaryLabel = legacyTruncatedQualifier + "."
)

// limitedReadClause is the clause composeCurrentState closes current_state
// with, and only that clause: the last sentence, " Limited read: " (or the
// legacy " Not complete: ") and then the qualified driver categories joined by
// "; ", each ending in a source-state qualifier (the current ones, or the
// legacy truncated one). The same words inside a provider's fact value are
// evidence and never match: anywhere but this closing form, they are left as
// they are.
var limitedReadClause = func() *regexp.Regexp {
	qualifiers := []string{regexp.QuoteMeta(strings.TrimSpace(legacyTruncatedQualifier))}
	for _, state := range []SourceState{SourceTruncated, SourceStale} {
		qualifiers = append(qualifiers, regexp.QuoteMeta(strings.TrimSpace(driverSourceStateQualifier(state))))
	}
	qualifier := `(?:` + strings.Join(qualifiers, `|`) + `)`
	item := `[^;().]+ ` + qualifier // a driver category has no "."
	prefix := `(?:` + regexp.QuoteMeta(legacyLimitedReadPrefix) + `|` + regexp.QuoteMeta(limitedReadPrefix) + `)`
	return regexp.MustCompile(`\.` + prefix + `(` + item + `(?:; ` + item + `)*)\.$`)
}()

// restateLimitedReadClause states a stored current_state's closing
// limited-read clause in the current words: the prefix, and the truncated
// qualifier of each item.
func restateLimitedReadClause(currentState string) string {
	match := limitedReadClause.FindStringSubmatchIndex(currentState)
	if match == nil {
		return currentState
	}
	items := strings.ReplaceAll(currentState[match[2]:match[3]], legacyTruncatedQualifier, driverSourceStateQualifier(SourceTruncated))
	return currentState[:match[0]] + "." + limitedReadPrefix + items + "."
}

// restateLegacySourceStateLabels states, on a served copy, the truncated-read
// words a stored answer carries in what the composers write now, and only
// where the server wrote them: each anchored on a driver whose source read
// was cut, by its own title -- the lead sentence and the principal driver
// list (leadDriverSentence, principalDriverTitles), the project status
// summary (its title and label, exactly), and the server note (exactly) --
// and the closing clause of current_state. Text that is not that, such as a
// title or provider value that carries the same words, is left as it is. The
// stored document is not rewritten: the drivers are copied.
func restateLegacySourceStateLabels(result InvestigationResult) InvestigationResult {
	result.CurrentState = restateLimitedReadClause(result.CurrentState)
	current := driverSourceStateQualifier(SourceTruncated)
	if current == legacyTruncatedQualifier {
		return result
	}
	if !slices.ContainsFunc(result.Drivers, func(driver DriverJudgment) bool { return driver.SourceState == SourceTruncated }) {
		return result
	}
	// A copy, so the stored document's drivers are not rewritten. Clone keeps
	// an empty list non-nil, as the contract requires.
	result.Drivers = slices.Clone(result.Drivers)
	for index := range result.Drivers {
		driver := &result.Drivers[index]
		if driver.SourceState != SourceTruncated {
			continue
		}
		if driver.SourceStateNote == legacyTruncatedNote {
			driver.SourceStateNote = driverSourceStateNote(SourceTruncated)
		}
		title := strings.TrimSpace(driver.Title)
		if title == "" {
			continue
		}
		if driver.Summary == title+legacyTruncatedSummaryLabel {
			driver.Summary = title + projectStatusStateLabel(SourceTruncated)
		}
	}
	result.DirectJudgment = restateLegacyLeadFragments(result.DirectJudgment, result.Drivers, result.Cohort, "Principal driver: ")
	result.DeterministicAnswer = restateLegacyLeadFragments(result.DeterministicAnswer, result.Drivers, result.Cohort, "Principal driver(s): ")
	return result
}

// LegacySourceStateWordsServed reports whether a served copy still carries
// the legacy truncated-read words in its lead prose, current_state, or a
// driver's summary or note: text the restatement left as it was, a provider's
// evidence with the same words, or a stored form it does not know. The
// completeness authority line counts it (legacy_source_state_words_served),
// so a missed rewrite is visible at Info.
func LegacySourceStateWordsServed(result InvestigationResult) bool {
	carries := func(text string) bool {
		return strings.Contains(text, legacyTruncatedQualifier) || strings.Contains(text, legacyTruncatedNote)
	}
	if carries(result.DirectJudgment) || carries(result.DeterministicAnswer) || carries(result.CurrentState) {
		return true
	}
	return slices.ContainsFunc(result.Drivers, func(driver DriverJudgment) bool {
		return carries(driver.Summary) || carries(driver.SourceStateNote)
	})
}

// restateLegacyLeadFragments restates the two places composeLeadProse put a
// driver's qualifier, at their composed position and nowhere else: the quick
// answer (the first principal driver in answer order, leadDriverSentence)
// and the details list (label, the other principal drivers' titles joined by
// "; ", principalDriverTitles). Each is rebuilt from the drivers in answer
// order with the legacy qualifier and replaced, once, by what the composers
// write now. A list is tried both after a quick answer and as a whole, so a
// head composed under either form is found. A title that merely carries the
// same words is never touched, because only the whole composed fragment
// matches.
func restateLegacyLeadFragments(head string, drivers []DriverJudgment, cohort *Cohort, label string) string {
	ordered := principalDriversInAnswerOrder(drivers, cohort)
	if len(ordered) == 0 {
		return head
	}
	legacyQualifier := func(driver DriverJudgment) string {
		if driver.SourceState == SourceTruncated {
			return legacyTruncatedQualifier
		}
		return driverSourceStateQualifier(driver.SourceState)
	}
	if lead := ordered[0]; lead.SourceState == SourceTruncated {
		title := strings.TrimRight(strings.TrimSpace(lead.Title), ".")
		if legacy := title + legacyQualifier(lead) + "."; title != "" {
			head = strings.Replace(head, legacy, leadDriverSentence(lead), 1)
		}
	}
	for _, list := range [][]DriverJudgment{ordered[1:], ordered} {
		if !slices.ContainsFunc(list, func(driver DriverJudgment) bool { return driver.SourceState == SourceTruncated }) {
			continue
		}
		legacy := make([]string, 0, len(list))
		for _, driver := range list {
			if title := strings.TrimSpace(driver.Title); title != "" {
				legacy = append(legacy, title+legacyQualifier(driver))
			}
		}
		from := label + strings.Join(legacy, "; ") + "."
		if strings.Contains(head, from) {
			return strings.Replace(head, from, label+strings.Join(principalDriverTitles(list), "; ")+".", 1)
		}
	}
	return head
}

// quickAnswerSentence is the driver sentence the composer leads an answer's
// head with (composeLeadProse), or "" when it leads with none.
func quickAnswerSentence(result InvestigationResult) string {
	return quickAnswerOf(result.Drivers, result.Cohort)
}

func quickAnswerOf(drivers []DriverJudgment, cohort *Cohort) string {
	ordered := principalDriversInAnswerOrder(drivers, cohort)
	if len(ordered) == 0 {
		return ""
	}
	return leadDriverSentence(ordered[0])
}

// modelPathStatusSentences are every status sentence a head can carry: the
// current partial and degraded ones, and the legacy three.
func modelPathStatusSentences() []string {
	sentences := append([]string{}, legacyModelPathStatusSentences...)
	for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded} {
		if sentence := statusSentence(status, SubjectResolution{}); sentence != "" {
			sentences = append(sentences, sentence)
		}
	}
	return sentences
}

// afterLead is where a head's status sentence is searched from: after lead,
// the quick answer the composer put first, when head carries it. A driver
// title that reads like a status sentence is the answer, not the status.
func afterLead(head, lead string) int {
	if lead != "" {
		if index := strings.Index(head, lead); index >= 0 {
			return index + len(lead)
		}
	}
	return 0
}

// restateHeadSentence replaces the first model-path status sentence that
// stands at a sentence boundary of head (the head's start, or after a sentence
// that ends in ". ", "? " or "! ") with want, searching after lead. An empty
// want removes the sentence. A head that carries no such sentence is returned
// unchanged.
func restateHeadSentence(head, want, lead string, maxLength int) string {
	at, stale := firstStatusSentence(head, afterLead(head, lead), modelPathStatusSentences())
	if stale == "" || stale == want {
		return head
	}
	prefix, rest := head[:at]+want, head[at+len(stale):]
	if want == "" {
		prefix = strings.TrimRight(prefix, " ")
		if prefix == "" {
			rest = strings.TrimLeft(rest, " ")
		}
	}
	return fitRestatedHead(prefix, rest, maxLength)
}

// firstStatusSentence finds the first of sentences at a sentence boundary of
// head, from start: its byte offset and its text, or "" for none.
func firstStatusSentence(head string, start int, sentences []string) (int, string) {
	best, found := -1, ""
	for _, sentence := range sentences {
		for from := start; from <= len(head); {
			index := strings.Index(head[from:], sentence)
			if index < 0 {
				break
			}
			index += from
			if index == 0 || strings.HasSuffix(head[:index], ". ") || strings.HasSuffix(head[:index], "? ") || strings.HasSuffix(head[:index], "! ") {
				if best < 0 || index < best {
					best, found = index, sentence
				}
				break
			}
			from = index + 1
		}
	}
	return best, found
}

var cardinalityTailPattern = regexp.MustCompile(` Counted (?:at least )?\d+ [^.]*\.$`)

// fitRestatedHead joins the head up to and including the served sentence to
// the rest of the head inside maxLength. The restated sentence can be longer
// than the one it replaced. A count sentence now leads the head
// (leadWithCountSentence), so truncating from the end keeps it; a stored head
// composed before the count led ends with it, and then the count sentence is
// the part that survives and the earlier prose is truncated to make room.
func fitRestatedHead(want, rest string, maxLength int) string {
	joined := want + rest
	if len([]rune(joined)) <= maxLength {
		return joined
	}
	if tail := cardinalityTailPattern.FindString(rest); tail != "" {
		room := maxLength - len([]rune(tail))
		if room > 0 {
			return truncateAtSentenceBoundary(want+strings.TrimSuffix(rest, tail), room) + tail
		}
	}
	return truncateAtSentenceBoundary(joined, maxLength)
}
