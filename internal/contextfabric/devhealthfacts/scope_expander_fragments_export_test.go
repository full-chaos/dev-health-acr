package devhealthfacts

// HistoricalCandidatesForTest is the candidate set the scoped historical
// statements filter by, as the production builder renders it.
func HistoricalCandidatesForTest() string {
	return requestedProjectHistoryWorkItemIDsSQL(requestedProjectIDsPredicate)
}
