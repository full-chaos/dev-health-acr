package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// teamAnchoredProjectCohort is true when the question asks for the projects of
// a named team and that team is committed. The members are then the projects
// the team's own reach names; a project the question text happens to match is
// not a member, and the lexical arms never admit one.
func teamAnchoredProjectCohort(request contextfabric.GraphDiscoveryRequest, declaredKind contextfabric.SubjectKind) bool {
	if declaredKind != contextfabric.SubjectProject {
		return false
	}
	if contextfabric.ScopeAnchorRetrievalKind(request.Frame, request.ScopeAnchorKind) != contextfabric.SubjectTeam {
		return false
	}
	for _, subject := range request.Resolution.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			return true
		}
	}
	return false
}
