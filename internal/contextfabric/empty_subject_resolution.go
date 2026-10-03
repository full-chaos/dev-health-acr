package contextfabric

func emptySubjectResolution() SubjectResolution {
	return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
}
