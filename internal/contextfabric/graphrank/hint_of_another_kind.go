package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// askedSubjectKind is the one kind a named-subject question asks for: the kind
// the caller confirmed, or else the kind the frame states. A frame that names no
// subject, or states no kind, asks for none. A cohort asks for members under an
// anchor, so its anchor hint is a different kind by design.
func askedSubjectKind(frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind) contextfabric.SubjectKind {
	if frame == nil || frame.SubjectExpression.Kind != contextfabric.SubjectExpressionNamed {
		return ""
	}
	if confirmedKind != nil {
		return confirmedKind.Kind
	}
	if named := frame.SubjectExpression.Named; named != nil && named.ExpectedKind != nil {
		return *named.ExpectedKind
	}
	return ""
}

// withoutHintsOfAnotherKind drops each caller hint of a kind the question does not
// ask for, before the hint loop and before a hint's label becomes a search term. A
// page sends the organization beside the repository it shows: if the repository
// hint is lost, the organization must not answer in its place (CHAOS-12555). The
// turn then resolves on its own terms and asks, or finds nothing, as it would with
// no hint.
//
// Two hints stay. An engine-minted hint is this engine's own carry and has its own
// rules. A hint that anchors the handle the question names stays too, because it
// says where that handle is (committedScopeAnchorShadowsHandle).
func withoutHintsOfAnotherKind(request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind) (contextfabric.InvestigationRequest, []contextfabric.SubjectHint) {
	asked := askedSubjectKind(frame, confirmedKind)
	if asked == "" {
		return request, nil
	}
	kept := make([]contextfabric.SubjectHint, 0, len(request.RequestedScope.SubjectHints))
	var dropped []contextfabric.SubjectHint
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind == "" || hint.Kind == asked || hintsource.Lookup(hint.Source).EngineMinted ||
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
