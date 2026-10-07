package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// teamAnchoredProjectCohort is the committed team subjects whose own reach is
// the member set of a question for the projects of a named team, empty for any
// other question. A project the question text happens to match, or only
// another committed subject reaches, is not a member, and the lexical arms
// never admit one.
func teamAnchoredProjectCohort(request contextfabric.GraphDiscoveryRequest, declaredKind contextfabric.SubjectKind) []contextfabric.SubjectRef {
	if declaredKind != contextfabric.SubjectProject {
		return nil
	}
	return contextfabric.ScopeAnchorTeams(request.Frame, request.ScopeAnchorKind, request.Resolution)
}
