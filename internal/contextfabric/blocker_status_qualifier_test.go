package contextfabric

import "testing"

func TestBlockerStatusQualifierReachesTheBlockersQuery(t *testing.T) {
	frame := &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{
		MemberKind: SubjectWorkItem, MemberQualifier: MemberQualifierStatus, MemberQualifierValue: "done",
	}}}
	if got := blockerStatusQualifier(frame); got != "done" {
		t.Fatalf("qualifier = %q, want done", got)
	}
	capability := FactCapability{Kind: FactBlockers, SupportedSubjectKinds: []SubjectKind{SubjectTeam}}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:t"}
	allowed := map[string]SubjectRef{canonicalFactSubjectKey(team): team}
	request := CanonicalFactRequest{blockerStatus: "done"}
	query, err := buildFactQuery(request, FactRequirement{Kind: FactBlockers, Subjects: []SubjectRef{team}}, capability, allowed, []SubjectRef{team})
	if err != nil {
		t.Fatalf("buildFactQuery: %v", err)
	}
	if got := query.Parameters[BlockerStatusParameter]; got != "done" {
		t.Fatalf("blockers query parameter = %q, want done", got)
	}
	other, err := buildFactQuery(request, FactRequirement{Kind: FactStatus, Subjects: []SubjectRef{team}}, FactCapability{Kind: FactStatus, SupportedSubjectKinds: []SubjectKind{SubjectTeam}}, allowed, []SubjectRef{team})
	if err != nil || other.Parameters[BlockerStatusParameter] != "" {
		t.Fatalf("another kind carried the status: %+v %v", other.Parameters, err)
	}
	frame.SubjectExpression.Scoped.MemberQualifierValue = "closed"
	if got := blockerStatusQualifier(frame); got != "" {
		t.Fatalf("a value outside the status set was carried: %q", got)
	}
	if got := blockerStatusQualifier(nil); got != "" {
		t.Fatalf("nil frame carried %q", got)
	}
}
