package v1

import "testing"

func TestAnswerBudgetBytesIsOptionalAndBoundedByTheBodyBound(t *testing.T) {
	base := ContextFabricInvestigationOptions{
		MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
		MaxDrivers: 10, MaxEvidenceRefs: 10, MaxSerializedBytes: 65536,
	}
	for _, tc := range []struct {
		name   string
		budget int
		valid  bool
	}{
		{"absent", 0, true},
		{"minimum", ContextFabricSerializedBytesMin, true},
		{"equal to the body bound", 65536, true},
		{"below the minimum", ContextFabricSerializedBytesMin - 1, false},
		{"above the body bound", 65537, false},
		{"negative", -1, false},
	} {
		options := base
		options.AnswerBudgetBytes = tc.budget
		if err := options.Validate(); (err == nil) != tc.valid {
			t.Errorf("%s: Validate() = %v, want valid=%v", tc.name, err, tc.valid)
		}
	}
}
