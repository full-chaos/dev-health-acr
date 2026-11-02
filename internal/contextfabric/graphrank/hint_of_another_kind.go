package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// withoutHintsOfAnotherKind drops each caller hint of a kind the question does not
// ask for, before the hint loop and before a hint's label becomes a search term. A
// page sends the organization beside the repository it shows: if the repository
// hint is lost, the organization must not answer in its place (CHAOS-12555). The
// turn then resolves on its own terms and asks, or finds nothing, as it would with
// no hint.
//
// Two hints stay. The subject this turn's redeemed candidate receipt picked is the
// caller's own answer, whatever its kind; the engine records it on ctx after the
// redemption, so a source string, which the wire carries, can never claim it. A hint
// that anchors the handle the question names stays too, because it says where that
// handle is (committedScopeAnchorShadowsHandle). Every other hint takes the rule.
// The engine's own reuse and group rechecks resolve with no frame, which asks for
// no kind.
func withoutHintsOfAnotherKind(ctx context.Context, request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind) (contextfabric.InvestigationRequest, []contextfabric.SubjectHint) {
	asked := contextfabric.AskedSubjectKind(frame, confirmedKind)
	if asked == "" {
		return request, nil
	}
	kept := make([]contextfabric.SubjectHint, 0, len(request.RequestedScope.SubjectHints))
	var dropped []contextfabric.SubjectHint
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind == "" || hint.Kind == asked || contextfabric.IsConfirmedCandidatePick(ctx, hint) ||
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

// boundedFrameTerms is the frame's own terms, cut to the bound a term list keeps.
func boundedFrameTerms(frame *contextfabric.QuestionFrame) []string {
	terms := contextfabric.FrameSubjectTerms(frame)
	if len(terms) > contractsv1.ContextFabricSubjectTermsMaxCount {
		terms = terms[:contractsv1.ContextFabricSubjectTermsMaxCount]
	}
	return terms
}

// comparisonAsksForTeams keeps the request's team hints only when a side of the
// comparison asks for a team: an inactive team hint is an answer about that team,
// and on a comparison of other kinds it would answer in place of both sides
// (CHAOS-12555).
func comparisonAsksForTeams(comparison contextfabric.ComparisonOperands, request contextfabric.InvestigationRequest) contextfabric.InvestigationRequest {
	for _, slot := range comparison.Slots {
		if slot.Kind == contractsv1.ContextFabricSubjectTeam {
			return request
		}
	}
	kept := make([]contextfabric.SubjectHint, 0, len(request.RequestedScope.SubjectHints))
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind != contractsv1.ContextFabricSubjectTeam {
			kept = append(kept, hint)
		}
	}
	request.RequestedScope.SubjectHints = kept
	return request
}
