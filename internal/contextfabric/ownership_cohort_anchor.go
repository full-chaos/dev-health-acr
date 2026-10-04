package contextfabric

// OwnershipAnchorBasis names how OwnershipAnchor chose the anchor.
type OwnershipAnchorBasis string

const (
	// OwnershipAnchorNone: no committed subject is the ownership anchor.
	OwnershipAnchorNone OwnershipAnchorBasis = "none"
	// OwnershipAnchorBound: the anchor is proven, either named by an anchor
	// term on an identity-proven commit or named by the caller's id.
	OwnershipAnchorBound OwnershipAnchorBasis = "bound"
	// OwnershipAnchorSoleCommit: the anchor is the one committed subject,
	// named by an anchor term on a commit that is not a proven identity (a
	// label match).
	OwnershipAnchorSoleCommit OwnershipAnchorBasis = "sole_commit"
)

// OwnershipAnchor decides which committed repository, if any, a team-members
// question is about. The teams of a repository are the teams that own it, so
// a graph reader serves this anchor from ownership records. It is the one
// decision for every way a repository reaches the committed set (an anchor
// term, an alias, a caller's id); a reader keeps no other copy of the rule.
//
// The rule:
//
//  1. Only a children_of_scope frame whose member kind is team has an
//     ownership anchor.
//  2. The reading must declare no anchor kind or declare repository. A
//     reading that names a project, a team or any other kind is not about a
//     repository, whatever was committed beside it.
//  3. A candidate is a committed repository with a label. It is named by the
//     question when resolution matched it to one of the frame's anchor terms.
//  4. When the reading declares no kind and a committed subject that is not a
//     repository matched an anchor term, the question may be about that
//     subject: there is no anchor.
//  5. When more than one candidate is named, the question is ambiguous: there
//     is no anchor.
//  6. The one named candidate is the anchor when its commit is an identity
//     proof (bound), or when it is the only committed subject (sole_commit,
//     a label match).
//  7. When no candidate is named, a repository the caller named by its id is
//     the anchor (bound), but only when it is the only committed repository
//     and no subject resolution found, committed or not, matched an anchor
//     term: a subject the anchor term names, even one left uncommitted, is
//     what the question is about.
//
// PURE: reads its arguments and mutates nothing.
func OwnershipAnchor(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) (SubjectRef, OwnershipAnchorBasis) {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectTeam {
		return SubjectRef{}, OwnershipAnchorNone
	}
	if declaredAnchorKind != "" && declaredAnchorKind != SubjectRepository {
		return SubjectRef{}, OwnershipAnchorNone
	}
	var candidates, named []SubjectRef
	otherNamed := false
	for _, subject := range resolution.Committed {
		matched := anchorTermMatched(frame, subject, resolution)
		if subject.Kind != SubjectRepository || subject.Label == "" {
			otherNamed = otherNamed || matched
			continue
		}
		candidates = append(candidates, subject)
		if matched {
			named = append(named, subject)
		}
	}
	if declaredAnchorKind == "" && otherNamed {
		return SubjectRef{}, OwnershipAnchorNone
	}
	switch len(named) {
	case 0:
		if !anyCandidateNamed(frame, resolution) && len(candidates) == 1 && bases.For(candidates[0]) == CommitBasisCallerCanonicalID {
			return candidates[0], OwnershipAnchorBound
		}
	case 1:
		if bases.For(named[0]).IdentityProven() {
			return named[0], OwnershipAnchorBound
		}
		if len(resolution.Committed) == 1 {
			return named[0], OwnershipAnchorSoleCommit
		}
	}
	// Two or more named candidates fall through: the question is ambiguous.
	return SubjectRef{}, OwnershipAnchorNone
}

// anyCandidateNamed reports whether any subject resolution found, committed
// or not, matched one of the frame's anchor terms.
func anyCandidateNamed(frame *QuestionFrame, resolution SubjectResolution) bool {
	for _, candidate := range resolution.Candidates {
		if anchorTermMatched(frame, candidate.Subject, resolution) {
			return true
		}
	}
	return false
}
