package devhealthfacts

// HistoricalCandidateSetsForTest is the candidate set the scoped historical
// statements filter by in the form the production builder renders (plain, one
// subquery per reference) and in the form the statements send (once, one scalar
// subquery per statement).
func HistoricalCandidateSetsForTest() (plain, once string) {
	plain = requestedProjectHistoryWorkItemIDsSQL(requestedProjectIDsPredicate)
	return plain, readOnceCandidatesSQL(plain)
}
