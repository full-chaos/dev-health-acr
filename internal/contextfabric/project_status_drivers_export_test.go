package contextfabric

// ProjectStatusDriversForTest runs the project status driver builder for a
// test in the external test package.
func ProjectStatusDriversForTest(interpretation InterpretedQuestion, committed []SubjectRef, facts []CanonicalFact, coverage Coverage, existingDrivers []DriverJudgment, existingClaims []ClaimedFact, allocation ItemAllocation) ([]DriverJudgment, []ClaimedFact, *ProjectStatusDriverEvent) {
	return projectStatusDrivers(interpretation, committed, facts, coverage, existingDrivers, existingClaims, allocation)
}
