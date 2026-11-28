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
	refs := make([]ContextFabricSubjectRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, candidate.Subject)
	}
	return refs
}

// CandidateSetProvenWhole reports whether nothing is committed and Candidates
// holds exactly the subjects WholeCandidates proved whole, by kind and
// canonical id. A candidate added or dropped after the proof makes it false.
func (r ContextFabricSubjectResolution) CandidateSetProvenWhole() bool {
	if r.WholeCandidates == nil || len(r.Committed) != 0 || len(r.Candidates) != len(r.WholeCandidates) {
		return false
	}
	type identity struct {
		kind ContextFabricSubjectKind
		id   string
	}
	proven := make(map[identity]bool, len(r.WholeCandidates))
	for _, subject := range r.WholeCandidates {
		proven[identity{subject.Kind, subject.CanonicalID}] = true
	}
	seen := make(map[identity]bool, len(r.Candidates))
	for _, candidate := range r.Candidates {
		key := identity{candidate.Subject.Kind, candidate.Subject.CanonicalID}
		if !proven[key] || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
