package contextfabric

// uncommittedTeamAnchorNamed is true when a scoped question ("which projects
// does team X own?") names a team anchor, resolution committed nothing, and
// candidates exist. The anchor is then not established, and discovering the
// member kind without it answers an organization-wide question instead.
func uncommittedTeamAnchorNamed(frame *QuestionFrame, declaredAnchorKind SubjectKind, resolution SubjectResolution) bool {
	return ScopeAnchorRetrievalKind(frame, declaredAnchorKind) == SubjectTeam &&
		len(resolution.Committed) == 0 && len(resolution.Candidates) > 0
}

// uncommittedTeamAnchorPrompt names the candidates the guard saw when the
// resolver built no prompt, so the clarification never goes out nameless.
func uncommittedTeamAnchorPrompt(resolution SubjectResolution) string {
	if resolution.ClarificationPrompt != "" {
		return resolution.ClarificationPrompt
	}
	if len(resolution.Candidates) == 1 {
		subject := resolution.Candidates[0].Subject
		return "Did you mean " + string(subject.Kind) + " " + subject.Label + "?"
	}
	return ClarificationPrompt(resolution.Candidates)
}
