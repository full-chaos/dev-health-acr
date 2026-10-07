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

// teamAnchorInclusionReason is the inclusion reason of a project the named
// team's own reach admitted.
const teamAnchorInclusionReason = "Project the named team owns, reached from the team in the authorized Context Fabric graph."

// teamAnchorCohortRationale is the rationale of a team-anchored project cohort.
const teamAnchorCohortRationale = "Projects were reached from the team the question names in the authorized Context Fabric graph."
