package contextfabric

// OwnershipCohortAnchor returns the repository a team-members frame is
// anchored on when the commit basis does not bind it: the one committed
// subject, when it is a repository and the reading points at it. The teams of
// a repository are the teams that own it, so a graph reader serves this anchor
// from ownership records, not from graph proximity.
//
// The reading points at the repository in one of two ways: it states the
// anchor kind as repository, or it states no anchor kind and resolution
// recorded the repository as a match for one of the frame's anchor terms. A
// reading that states another anchor kind (a project, say), and a repository
// that matched no anchor term under a reading that states none, name no
// ownership anchor: a hint or a carry committed that repository and the
// question is not about it. Zero or several committed subjects name none
// either.
func OwnershipCohortAnchor(frame *QuestionFrame, resolution SubjectResolution, declaredAnchorKind SubjectKind) (SubjectRef, bool) {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectTeam {
		return SubjectRef{}, false
	}
	committed := resolution.Committed
	if len(committed) != 1 || committed[0].Kind != SubjectRepository || committed[0].Label == "" {
		return SubjectRef{}, false
	}
	switch declaredAnchorKind {
	case SubjectRepository:
		return committed[0], true
	case "":
		if anchorTermMatched(frame, committed[0], resolution) {
			return committed[0], true
		}
	}
	return SubjectRef{}, false
}
