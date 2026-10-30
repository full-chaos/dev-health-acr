package contextfabric

import (
	"regexp"
	"strings"
)

// restateServedStatusHead makes the first sentence of DirectJudgment and
// DeterministicAnswer state result.Status, the status the document is served
// with after every server downgrade, instead of the status the model draft
// carried when the head was composed.
//
// Only a head that carries one of the three model-path status sentences
// (complete, partial, degraded) at a sentence boundary is touched: the
// composer states it first, or after the quick answer that leads the head (a
// count sentence, a principal driver; see composeLeadProse). Every other head
// -- clarification, no_match, refusal, a client-written answer -- is composed
// by its own renderer from its own terminal status and is left alone. The rest
// of the head (quick answer, principal drivers, cardinality sentence) is kept.
func restateServedStatusHead(result InvestigationResult) InvestigationResult {
	want, ok := modelPathStatusSentence(result.Status)
	if !ok {
		return result
	}
	result.DirectJudgment = restateHeadSentence(result.DirectJudgment, want, directJudgmentMaxLength)
	result.DeterministicAnswer = restateHeadSentence(result.DeterministicAnswer, want, deterministicAnswerMaxLength)
	return result
}

func modelPathStatusSentence(status InvestigationStatus) (string, bool) {
	switch status {
	case InvestigationComplete, InvestigationPartial, InvestigationDegraded:
		return statusSentence(status, SubjectResolution{}), true
	}
	return "", false
}

// restateHeadSentence replaces the first model-path status sentence that
// stands at a sentence boundary of head (the head's start, or after a sentence
// that ends in ". ") with want. A head whose first such sentence is already
// want, or that carries none, is returned unchanged.
func restateHeadSentence(head, want string, maxLength int) string {
	at, stale := firstModelPathStatusSentence(head)
	if stale == "" {
		return head
	}
	return fitRestatedHead(head[:at]+want, head[at+len(stale):], maxLength)
}

// firstModelPathStatusSentence finds the first model-path status sentence at a
// sentence boundary of head: its byte offset and its text, or "" for none.
func firstModelPathStatusSentence(head string) (int, string) {
	best, found := -1, ""
	for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded} {
		sentence, _ := modelPathStatusSentence(status)
		for from := 0; from <= len(head); {
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
