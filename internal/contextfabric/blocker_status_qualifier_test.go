package contextfabric

import "testing"

func TestBlockerStatusQualifierReachesTheBlockersRequirement(t *testing.T) {
	frame := &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{
		MemberKind: SubjectWorkItem, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: "done",
	}}}
	in := []FactRequirement{{Kind: FactBlockers}, {Kind: FactStatus}}
	out := withBlockerStatusQualifier(in, frame)
	if got := out[0].Parameters[BlockerStatusParameter]; got != "done" {
		t.Fatalf("blockers parameter = %q, want done", got)
	}
	if len(out[1].Parameters) != 0 || len(in[0].Parameters) != 0 {
		t.Fatalf("other requirements or the input were modified: %+v %+v", out, in)
	}
	frame.SubjectExpression.Scoped.MemberQualifierValue = "closed"
	if got := withBlockerStatusQualifier(in, frame)[0].Parameters[BlockerStatusParameter]; got != "" {
		t.Fatalf("a value outside the status set was carried: %q", got)
	}
	if got := withBlockerStatusQualifier(in, nil)[0].Parameters[BlockerStatusParameter]; got != "" {
		t.Fatalf("nil frame carried %q", got)
	}
}
