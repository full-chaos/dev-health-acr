package contextfabric

// treeScopeAnchorKinds are the anchor kinds whose members are read over the
// entity tree. An anchor of any other kind stays on the generic walk.
var treeScopeAnchorKinds = map[SubjectKind]bool{
	SubjectTeam: true, SubjectProject: true, SubjectRepository: true,
}

// unboundScopeAnchorNamed is true when a scoped question ("which projects does
// team X own?") names an anchor of a tree kind that discovery cannot bind to
// exactly one subject: several committed subjects of that kind and no anchor
// term matched one of them, or none committed at that kind while candidates of
// it exist. Discovering the member kind then answers an organization-wide
// question that silently drops the scope.
func unboundScopeAnchorNamed(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) bool {
	kind := ScopeAnchorRetrievalKind(frame, declaredAnchorKind)
	if !treeScopeAnchorKinds[kind] {
		return false
	}
	if _, owned := OwnershipAnchor(frame, declaredAnchorKind, resolution, bases); owned != OwnershipAnchorNone {
		return false
	}
	if len(ScopeAnchorSubjects(frame, declaredAnchorKind, resolution)) > 0 {
		return false
	}
	committed := 0
	for _, subject := range resolution.Committed {
		if subject.Kind == kind {
			committed++
		}
	}
	if committed > 1 {
		return true
	}
	if committed == 1 {
		return false
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == kind {
			return true
		}
	}
	return false
}

// unboundScopeAnchorResolution presents the anchor-kind subjects the guard
// could not choose between as ambiguous candidates and commits none, so the
// terminal answer is a clarification that names them.
func unboundScopeAnchorResolution(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution) SubjectResolution {
	kind := ScopeAnchorRetrievalKind(frame, declaredAnchorKind)
	candidates := make([]SubjectCandidate, 0, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != kind {
			continue
		}
		candidate.State = ResolutionAmbiguous
		candidates = append(candidates, candidate)
	}
	resolution.Candidates = candidates
	resolution.Committed = []SubjectRef{}
	resolution.CommitDecisionDigests = nil
	resolution.ClarificationPrompt = ClarificationPrompt(candidates)
	return resolution
}
