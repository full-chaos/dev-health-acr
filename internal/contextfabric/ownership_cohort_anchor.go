package contextfabric

// OwnershipCohortAnchor returns the repository a team-members frame is
// anchored on when the commit basis does not bind it: the one committed
// subject, when it is a repository and the reading points at it. The teams of
// a repository are the teams that own it, so a graph reader serves this anchor
// from ownership records, not from graph proximity.
//
// The reading points at the repository when resolution recorded it as a match
// for one of the frame's anchor terms, and the reading states no anchor kind
// or states repository. A repository that matched no anchor term was committed
// for another term of the question, and a reading that states another anchor
// kind (a project, say) is not about a repository: neither names an ownership
// anchor, whatever kind the model declared. Zero or several committed subjects
// name none either.
func OwnershipCohortAnchor(frame *QuestionFrame, resolution SubjectResolution, declaredAnchorKind SubjectKind) (SubjectRef, bool) {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectTeam {
		return SubjectRef{}, false
	}
	committed := resolution.Committed
	if len(committed) != 1 || committed[0].Kind != SubjectRepository || committed[0].Label == "" {
		return SubjectRef{}, false
	}
	if declaredAnchorKind != "" && declaredAnchorKind != SubjectRepository {
		return SubjectRef{}, false
	}
	if !anchorTermMatched(frame, committed[0], resolution) {
		return SubjectRef{}, false
	}
	return committed[0], true
}
