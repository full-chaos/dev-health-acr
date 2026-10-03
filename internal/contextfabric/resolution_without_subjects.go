package contextfabric

func withoutSubjects(resolution SubjectResolution) SubjectResolution {
	resolution.Candidates = []SubjectCandidate{}
	resolution.Committed = []SubjectRef{}
	resolution.CommitDecisionDigests = nil
	return resolution
}
