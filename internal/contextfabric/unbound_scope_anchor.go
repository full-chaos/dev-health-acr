package contextfabric

import "context"

// unboundScopeAnchorTerminalReason is the subjectless-terminal reason of a turn
// ended before discovery because the scope anchor could not be bound to one
// subject.
const unboundScopeAnchorTerminalReason = "unbound_scope_anchor"

// treeScopeAnchorKinds are the anchor kinds whose members are read over the
// entity tree. An anchor of any other kind stays on the generic walk.
var treeScopeAnchorKinds = map[SubjectKind]bool{
	SubjectTeam: true, SubjectProject: true, SubjectRepository: true,
}

// unboundScopeAnchorNamed is true when a scoped question ("which projects does
// team X own?") names an anchor of a tree kind that discovery cannot bind to
// exactly one subject: several committed subjects of that kind and no anchor
// term matched one of them, or none committed at that kind while candidates of
// it exist. Discovering the member kind then answers an organization-wide
// question that silently drops the scope.
func unboundScopeAnchorNamed(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) bool {
	kind := ScopeAnchorRetrievalKind(frame, declaredAnchorKind)
	if !treeScopeAnchorKinds[kind] {
		return false
	}
	if _, owned := OwnershipAnchor(frame, declaredAnchorKind, resolution, bases); owned != OwnershipAnchorNone {
		return false
	}
	if len(ScopeAnchorSubjects(frame, declaredAnchorKind, resolution)) > 0 {
		return false
	}
	committed := 0
	for _, subject := range resolution.Committed {
		if subject.Kind == kind {
			committed++
		}
	}
	if committed > 1 {
		return true
	}
	if committed == 1 {
		return false
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == kind {
			return true
		}
	}
	return false
}

// unboundScopeAnchorResolution presents the anchor-kind subjects the guard
// could not choose between as ambiguous candidates and commits none, so the
// terminal answer is a clarification that names them.
func unboundScopeAnchorResolution(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution) SubjectResolution {
	kind := ScopeAnchorRetrievalKind(frame, declaredAnchorKind)
	candidates := make([]SubjectCandidate, 0, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != kind {
			continue
		}
		candidate.State = ResolutionAmbiguous
		candidates = append(candidates, candidate)
	}
	resolution.Candidates = candidates
	resolution.Committed = []SubjectRef{}
	resolution.CommitDecisionDigests = nil
	if resolution.ClarificationPrompt == "" {
		resolution.ClarificationPrompt = ClarificationPrompt(candidates)
	}
	return resolution
}

type unboundScopeAnchorContextKey struct{}

// withUnboundScopeAnchorTurn marks the context of a turn the unbound-anchor
// guard ended, so the terminal's reason names the decision the turn took
// rather than what retrieval found.
func withUnboundScopeAnchorTurn(ctx context.Context) context.Context {
	return context.WithValue(ctx, unboundScopeAnchorContextKey{}, true)
}

// unboundAnchorAwareTerminalReason is subjectlessTerminalReason with the one
// class the guard decided before discovery named: a pool that is non-empty
// only because the scope anchor could not be bound reads unbound_scope_anchor,
// not the generic ambiguous. Every other ambiguous turn keeps its reason.
func unboundAnchorAwareTerminalReason(ctx context.Context, gate FrameGate, resolution SubjectResolution, subjectCandidatesAuthzDropped int, declaredKind declaredKindDecision) string {
	reason := subjectlessTerminalReason(gate, resolution, subjectCandidatesAuthzDropped, declaredKind)
	if marked, _ := ctx.Value(unboundScopeAnchorContextKey{}).(bool); marked && reason == "ambiguous" {
		return unboundScopeAnchorTerminalReason
	}
	return reason
}
