package contextfabric

import "testing"

// A qualifier on an organization_scope that names no member kind qualifies
// nothing a read can filter: it is invalid, not silently accepted.
func TestAnOrganizationScopeQualifierNeedsAMemberKind(t *testing.T) {
	frame := QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{MemberQualifier: MemberQualifierStatus, MemberQualifierValue: "open"}},
	}
	failure, bad := checkI17(frame)
	if !bad || failure.Detail != FrameFailureMemberQualifierInvalid {
		t.Fatalf("checkI17 = %+v, %v; want member_qualifier_invalid", failure, bad)
	}
}
