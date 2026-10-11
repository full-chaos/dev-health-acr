package contextfabric

// BlockerStatusParameter is the one fact parameter the engine hands the
// blockers provider when the question's frame names a work-item status: the
// team blockers rollup then counts blockers in that status instead of open
// ones. It is system-authored, so no capability lists it as model-allowed.
const BlockerStatusParameter = "blocker_status"

// blockerStatusQualifier is the frame's status qualifier when its value is in
// the closed work-item status set, else empty (the default: open blockers).
func blockerStatusQualifier(frame *QuestionFrame) string {
	if frame == nil || frame.SubjectExpression.Scoped == nil {
		return ""
	}
	scoped := frame.SubjectExpression.Scoped
	if scoped.MemberQualifier != MemberQualifierStatus || !InWorkItemStatusVocabulary(scoped.MemberQualifierValue) {
		return ""
	}
	return scoped.MemberQualifierValue
}
