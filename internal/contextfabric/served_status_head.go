package contextfabric

import "strings"

// restateServedStatusHead makes the first sentence of DirectJudgment and
// DeterministicAnswer state result.Status, the status the document is served
// with after every server downgrade, instead of the status the model draft
// carried when the head was composed.
//
// Only a head that opens with one of the three model-path status sentences
// (complete, partial, degraded) is touched. Every other head -- clarification,
// no_match, refusal, a client-written answer -- is composed by its own
// renderer from its own terminal status and is left alone. The rest of the
// head (principal driver, cardinality sentence) is kept.
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

func restateHeadSentence(head, want string, maxLength int) string {
	if strings.HasPrefix(head, want) {
		return head
	}
	for _, status := range []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded} {
		stale, _ := modelPathStatusSentence(status)
		if rest, found := strings.CutPrefix(head, stale); found {
			return truncateAtSentenceBoundary(want+rest, maxLength)
		}
	}
	return head
}
