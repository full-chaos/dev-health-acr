package contextfabric

// deploymentCohortFrameMember reports whether frame asks for the deployment
// members of a named anchor, and the declared member kind when it does.
func deploymentCohortFrameMember(frame *QuestionFrame) (SubjectKind, bool) {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectDeployment {
		return "", false
	}
	return SubjectDeployment, true
}

// deploymentCohortAnchorsServable is true only for exactly one committed
// anchor of a proven kind, and only when the interpretation did not declare a
// different anchor kind than the committed one (a caller-supplied hint can
// commit a repository the question never named as its anchor).
func deploymentCohortAnchorsServable(committed []SubjectRef, declaredAnchorKind SubjectKind) bool {
	if declaredAnchorKind != "" && !DeploymentCohortAnchorServable(declaredAnchorKind) {
		return false
	}
	return len(committed) == 1 && DeploymentCohortAnchorServable(committed[0].Kind)
}
