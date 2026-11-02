package contextfabric

// AskedSubjectKind is the one kind a named-subject question asks for: the kind the
// caller confirmed, or else the kind the frame states. A frame that names no
// subject, or states no kind, asks for none, and neither does a nil frame. A
// cohort asks for members under an anchor, so its anchor is a different kind by
// design (CHAOS-12555).
func AskedSubjectKind(frame *QuestionFrame, confirmedKind *ConfirmedExpectedKind) SubjectKind {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionNamed {
		return ""
	}
	if confirmedKind != nil {
		return confirmedKind.Kind
	}
	if named := frame.SubjectExpression.Named; named != nil && named.ExpectedKind != nil {
		return *named.ExpectedKind
	}
	return ""
}
