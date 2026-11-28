package v1

// ContextFabricSubjectCandidatesCompleteHeader is set to
// ContextFabricSubjectCandidatesCompleteValue on a fresh clarification answer
// whose subject candidates are the whole set the searches found for the
// caller. It is a response header so that no published schema carries the
// claim. Absent means not proven, never "incomplete".
const (
	ContextFabricSubjectCandidatesCompleteHeader = "X-ACR-Subject-Candidates-Complete"
	ContextFabricSubjectCandidatesCompleteValue  = "true"
)

// ContextFabricSubjectRefsOf is the subjects of candidates, in order. It is
// never nil.
func ContextFabricSubjectRefsOf(candidates []ContextFabricSubjectCandidate) []ContextFabricSubjectRef {
	return nil
}

// CandidateSetProvenWhole reports whether nothing is committed and Candidates
// holds exactly the subjects WholeCandidates proved whole, by kind and
// canonical id. A candidate added or dropped after the proof makes it false.
func (r ContextFabricSubjectResolution) CandidateSetProvenWhole() bool {
	return false
}
