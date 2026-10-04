package contextfabric

// deploymentCohortFrameMember reports whether frame asks for the deployment
// members of a named anchor, and the declared member kind when it does.
func deploymentCohortFrameMember(frame *QuestionFrame) (SubjectKind, bool) {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectDeployment {
		return "", false
	}
	return SubjectDeployment, true
}

// ScopeAnchorKindSource names where a sample's ScopeAnchorKind came from when
// it was not declared by the interpretation.
type ScopeAnchorKindSource string

// ScopeAnchorKindCommittedHint: derived from the one committed subject.
const ScopeAnchorKindCommittedHint ScopeAnchorKindSource = "committed_hint"

// withDerivedScopeAnchorKind fills an empty declared anchor kind from the
// kind of the one committed subject. A declared kind, and zero or several
// committed subjects, leave the sample unchanged.
func withDerivedScopeAnchorKind(sample FamilySample, committed []SubjectRef) FamilySample {
	if sample.ScopeAnchorKind != "" || len(committed) != 1 || committed[0].Kind == "" {
		return sample
	}
	sample.ScopeAnchorKind = committed[0].Kind
	sample.ScopeAnchorKindSource = ScopeAnchorKindCommittedHint
	return sample
}

// DeploymentCohortAnchor returns the committed subject a deployment-members
// frame is anchored on: the one committed subject, when its kind can anchor a
// deployment cohort. Zero or several committed subjects name no anchor.
//
// The commit basis is not read. The engine admits the frame on this rule and a
// graph reader chooses the member read on it, so a subject the engine admitted
// as the anchor is the subject whose reach the reader serves.
func DeploymentCohortAnchor(committed []SubjectRef) (SubjectRef, bool) {
	if len(committed) != 1 || !DeploymentCohortAnchorServable(committed[0].Kind) {
		return SubjectRef{}, false
	}
	return committed[0], true
}

// deploymentCohortAnchorsServable is true only for exactly one committed
// anchor of a proven kind whose declared anchor kind (derived from the
// committed subject when empty) equals the committed one.
func deploymentCohortAnchorsServable(committed []SubjectRef, declaredAnchorKind SubjectKind) bool {
	anchor, ok := DeploymentCohortAnchor(committed)
	if !ok {
		return false
	}
	return declaredAnchorKind == "" || declaredAnchorKind == anchor.Kind
}

// zeroCommitAnchorKindUnservable is true when no subject was committed and
// the anchor kind is known and cannot serve deployment members, so confirming
// a candidate could never help. The known kinds are the declared one, else
// the kinds of the offered candidates; an empty set is not a kind problem.
func zeroCommitAnchorKindUnservable(committed []SubjectRef, candidates []SubjectCandidate, declaredAnchorKind SubjectKind) bool {
	if len(committed) != 0 {
		return false
	}
	if declaredAnchorKind != "" {
		return !DeploymentCohortAnchorServable(declaredAnchorKind)
	}
	if len(candidates) == 0 {
		return false
	}
	for _, c := range candidates {
		if DeploymentCohortAnchorServable(c.Subject.Kind) {
			return false
		}
	}
	return true
}
