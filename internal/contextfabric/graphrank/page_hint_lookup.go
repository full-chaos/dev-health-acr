package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// pageHintLookupTally counts, for one resolveSubjects call, how each Ask Dev
// page-context subject hint resolved (CHAOS-12230): hints whose source is
// hintsource.AskDevPageContext, which web builds in acr's canonical form from
// natural keys. Only an exact canonical id commits on the caller's identity;
// any other id silently falls back to label search, so this is the one
// operator-visible signal that web's id builder and acr's disagree.
//
// Every page hint takes exactly one outcome, decided when the line is emitted:
//
//   - exact_hit: ExactHint found its node (whether or not the principal may
//     then see it -- the lookup, not authorization, is what is measured);
//   - label_fallback: it was not found, it carries a label, and the call's
//     hybrid search ran, so the label was one of the searched terms;
//   - unresolved: it was not found, and either no search ran (the caller-hint
//     short circuit, an inactive-team answer, an error) or it has no label.
//
// A hint the loop never looked up (no id: web sends none for a key whose id
// would exceed the producer's byte bound) is not an exact hit, so it takes one
// of the other two outcomes.
type pageHintLookupTally struct {
	requestID      string
	hints          []pageHintOutcome
	byIndex        map[int]int
	searchFallback bool
}

type pageHintOutcome struct {
	kind     string
	labelled bool
	exactHit bool
}

// newPageHintLookupTally records the request's page hints by their position in
// RequestedScope.SubjectHints, so the hint loop can mark each one it finds.
func newPageHintLookupTally(request contextfabric.InvestigationRequest) *pageHintLookupTally {
	tally := &pageHintLookupTally{requestID: request.RequestID, byIndex: map[int]int{}}
	for index, hint := range request.RequestedScope.SubjectHints {
		if hint.Source != hintsource.AskDevPageContext {
			continue
		}
		tally.byIndex[index] = len(tally.hints)
		tally.hints = append(tally.hints, pageHintOutcome{kind: string(hint.Kind), labelled: strings.TrimSpace(hint.Label) != ""})
	}
	return tally
}

// exactHit marks the hint at position index of the request's hint list as
// found by the exact lookup. A hint of another source is not counted.
func (t *pageHintLookupTally) exactHit(index int) {
	if slot, ok := t.byIndex[index]; ok {
		t.hints[slot].exactHit = true
	}
}

// searchFallbackRan records that the call went on to hybrid search.
func (t *pageHintLookupTally) searchFallbackRan() { t.searchFallback = true }

// event folds the tally into the one page_hint_lookup event, or reports false
// when the request carries no page hint.
func (t *pageHintLookupTally) event() (ResolutionTraceEvent, bool) {
	if len(t.hints) == 0 {
		return ResolutionTraceEvent{}, false
	}
	event := ResolutionTraceEvent{
		RequestID: t.requestID, Stage: "page_hint_lookup",
		PageHintCount: len(t.hints), PageHintSearchFallback: t.searchFallback,
		PageHintExactHitByKind: map[string]int{}, PageHintLabelFallbackByKind: map[string]int{}, PageHintUnresolvedByKind: map[string]int{},
	}
	for _, hint := range t.hints {
		switch {
		case hint.exactHit:
			event.PageHintExactHit++
			event.PageHintExactHitByKind[hint.kind]++
		case t.searchFallback && hint.labelled:
			event.PageHintLabelFallback++
			event.PageHintLabelFallbackByKind[hint.kind]++
		default:
			event.PageHintUnresolved++
			event.PageHintUnresolvedByKind[hint.kind]++
		}
	}
	return event, true
}

// emit traces the event. resolveSubjects defers it once the hint loop is
// reached, so every later return -- the short circuit, the inactive-team
// answer, the identity-question exit, an error, the ordinary end -- emits it.
func (t *pageHintLookupTally) emit(tracer ResolutionTracer) {
	if tracer == nil {
		return
	}
	if event, ok := t.event(); ok {
		tracer.Trace(event)
	}
}
