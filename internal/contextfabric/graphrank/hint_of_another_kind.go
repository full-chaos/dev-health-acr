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
// that anchors the handle the question binds stays too, because it says where that
// handle is (committedScopeAnchorShadowsHandle). bound is the turn's handles after
// the work item key gate (bindQuestionHandles), so a key the caller does not hold
// anchors nothing. Every other hint takes the rule.
// The engine's own reuse and group rechecks resolve with no frame, which asks for
// no kind.
func withoutHintsOfAnotherKind(ctx context.Context, request contextfabric.InvestigationRequest, bound []BoundHandle, frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind) (contextfabric.InvestigationRequest, []contextfabric.SubjectHint) {
	asked := askedKinds(frame, confirmedKind)
	if asked == nil {
		return request, nil
	}
	kept := make([]contextfabric.SubjectHint, 0, len(request.RequestedScope.SubjectHints))
	var dropped []contextfabric.SubjectHint
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind == "" || asked[hint.Kind] || contextfabric.IsConfirmedCandidatePick(ctx, hint) ||
			committedScopeAnchorShadowsHandle(bound, []contextfabric.SubjectRef{{Kind: hint.Kind}}, frame) {
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

// askedKinds is the kinds a question asks for, or nil when it states none. A named
// subject asks for its one kind (contextfabric.AskedSubjectKind). An explicit set
// asks for its own operands' kinds, and it reaches this path when the comparison
// cut does not take it; one operand with no stated kind leaves the set asking for
// none, as before.
func askedKinds(frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind) map[contextfabric.SubjectKind]bool {
	if kind := contextfabric.AskedSubjectKind(frame, confirmedKind); kind != "" {
		return map[contextfabric.SubjectKind]bool{kind: true}
	}
	if frame == nil || frame.SubjectExpression.Kind != contextfabric.SubjectExpressionExplicitSet {
		return nil
	}
	slots := contextfabric.ClassifyComparisonOperands(frame).Slots
	if len(slots) == 0 {
		return nil
	}
	kinds := make(map[contextfabric.SubjectKind]bool, len(slots))
	for _, slot := range slots {
		if slot.Kind == "" {
			return nil
		}
		kinds[slot.Kind] = true
	}
	return kinds
}

// boundedFrameTerms is the frame's own terms, cut to the bound a term list keeps.
func boundedFrameTerms(frame *contextfabric.QuestionFrame) []string {
	terms := contextfabric.FrameSubjectTerms(frame)
	if len(terms) > contractsv1.ContextFabricSubjectTermsMaxCount {
		terms = terms[:contractsv1.ContextFabricSubjectTermsMaxCount]
	}
	return terms
}

// comparisonRequest drops each hint of a kind no side of the comparison asks for.
// Such a hint answers for neither side: as an inactive team it would answer in
// place of both, and receipt binding counts it unbound, which holds a comparison
// whose sides both resolved (CHAOS-12555). The redeemed pick stays, as everywhere.
func comparisonRequest(ctx context.Context, comparison contextfabric.ComparisonOperands, request contextfabric.InvestigationRequest) contextfabric.InvestigationRequest {
	asked := map[contextfabric.SubjectKind]bool{}
	for _, slot := range comparison.Slots {
		asked[slot.Kind] = true
	}
	kept := make([]contextfabric.SubjectHint, 0, len(request.RequestedScope.SubjectHints))
	for _, hint := range request.RequestedScope.SubjectHints {
		if hint.Kind == "" || asked[hint.Kind] || contextfabric.IsConfirmedCandidatePick(ctx, hint) {
			kept = append(kept, hint)
		}
	}
	request.RequestedScope.SubjectHints = kept
	return request
}
