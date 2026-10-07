package contextfabric

// uncommittedTeamAnchorNamed is true when a scoped question ("which projects
// does team X own?") names a team anchor, resolution committed nothing, and
// candidates exist. The anchor is then not established, and discovering the
// member kind without it answers an organization-wide question instead.
func uncommittedTeamAnchorNamed(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution) bool {
	return ScopeAnchorRetrievalKind(frame, declaredAnchorKind) == SubjectTeam &&
		len(resolution.Committed) == 0 && len(resolution.Candidates) > 0
}

// uncommittedTeamAnchorPrompt names the one candidate; several candidates keep
// the resolver's prompt, or the generic one when it built none.
func uncommittedTeamAnchorPrompt(resolution SubjectResolution) string {
	if resolution.ClarificationPrompt != "" || len(resolution.Candidates) != 1 {
		return resolution.ClarificationPrompt
	}
	subject := resolution.Candidates[0].Subject
	return "Did you mean " + string(subject.Kind) + " " + subject.Label + "?"
}
