package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// withoutHintsOfAnotherKind drops each caller hint of a kind the question does not
// ask for, before the hint loop and before a hint's label becomes a search term. A
// page sends the organization beside the repository it shows: if the repository
// hint is lost, the organization must not answer in its place (CHAOS-12555). The
// turn then resolves on its own terms and asks, or finds nothing, as it would with
// no hint.
//
// Two hints stay. A candidate the caller picked from an offer is the caller's own
// answer, whatever kind it is. A hint that anchors the handle the question names
// stays too, because it says where that handle is
// (committedScopeAnchorShadowsHandle). Every other source takes the rule: a hint's
// source is a wire string, so an exemption by source is one a caller can claim, and
// the engine's own reuse and group rechecks resolve with no frame, which asks for no
// kind.
func withoutHintsOfAnotherKind(request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind) (contextfabric.InvestigationRequest, []contextfabric.SubjectHint) {
	asked := contextfabric.AskedSubjectKind(frame, confirmedKind)
	if asked == "" {
		return request, nil
	}
	kept := make([]contextfabric.SubjectHint, 0, len(request.RequestedScope.SubjectHints))
	var dropped []contextfabric.SubjectHint
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind == "" || hint.Kind == asked || hint.Source == string(hintsource.ConfirmedCandidateReceipt) ||
			committedScopeAnchorShadowsHandle(request.Question, []contextfabric.SubjectRef{{Kind: hint.Kind}}, frame) {
			kept = append(kept, hint)
			continue
		}
		dropped = append(dropped, hint)
	}
	if len(dropped) > 0 {
		request.RequestedScope.SubjectHints = kept
	}
	return request, dropped
}
