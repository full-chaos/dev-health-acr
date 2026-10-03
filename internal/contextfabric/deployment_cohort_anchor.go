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

// deploymentCohortAnchorsServable is true only for exactly one committed
// anchor of a proven kind whose declared anchor kind (derived from the
// committed subject when empty) equals the committed one.
func deploymentCohortAnchorsServable(committed []SubjectRef, declaredAnchorKind SubjectKind) bool {
	if len(committed) != 1 || !DeploymentCohortAnchorServable(committed[0].Kind) {
		return false
	}
	return declaredAnchorKind == "" || declaredAnchorKind == committed[0].Kind
}
