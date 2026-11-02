package contextfabric

import (
	"context"
	"strings"
)

type confirmedCandidatePickKey struct{}

// WithConfirmedCandidatePick records on ctx the subject this turn's redeemed
// candidate receipt picked. Only the engine sets it, after redeeming the receipt,
// so a hint's source string, which a caller writes, can never claim it
// (CHAOS-12555).
func WithConfirmedCandidatePick(ctx context.Context, pick SubjectRef) context.Context {
	return context.WithValue(ctx, confirmedCandidatePickKey{}, pick)
}

// IsConfirmedCandidatePick reports whether hint names the subject this turn's
// redeemed candidate receipt picked.
func IsConfirmedCandidatePick(ctx context.Context, hint SubjectHint) bool {
	pick, ok := ctx.Value(confirmedCandidatePickKey{}).(SubjectRef)
	return ok && pick.Kind == hint.Kind && pick.CanonicalID == strings.TrimSpace(hint.ID)
}

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
