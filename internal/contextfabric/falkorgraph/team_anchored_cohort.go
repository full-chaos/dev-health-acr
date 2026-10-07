package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// teamAnchoredCohort is the committed team subjects whose own reach is the
// member set of a question for the projects or the repositories of a named
// team, empty for any other question. A member the question text happens to
// match, or only another committed subject reaches, is not a member, and the
// lexical arms never admit one.
func teamAnchoredCohort(request contextfabric.GraphDiscoveryRequest, declaredKind contextfabric.SubjectKind) []contextfabric.SubjectRef {
	if declaredKind != contextfabric.SubjectProject && declaredKind != contextfabric.SubjectRepository {
		return nil
	}
	return contextfabric.ScopeAnchorTeams(request.Frame, request.ScopeAnchorKind, request.Resolution)
}

// teamAnchorInclusionReason is the inclusion reason of a project the named
// team's own reach admitted.
const teamAnchorInclusionReason = "Project the named team owns, reached from the team in the authorized Context Fabric graph."

// teamAnchorRepositoryInclusionReason is the inclusion reason of a repository
// the named team's own reach admitted.
const teamAnchorRepositoryInclusionReason = "Repository the named team owns, reached from the team in the authorized Context Fabric graph."

// teamAnchorCohortRationale is the rationale of a team-anchored project cohort.
const teamAnchorCohortRationale = "Projects were reached from the team the question names in the authorized Context Fabric graph."

// teamAnchorRepositoryCohortRationale is the rationale of a team-anchored
// repository cohort.
const teamAnchorRepositoryCohortRationale = "Repositories were reached from the team the question names in the authorized Context Fabric graph."

func teamAnchorInclusionReasonFor(kind contextfabric.SubjectKind) string {
	if kind == contextfabric.SubjectRepository {
		return teamAnchorRepositoryInclusionReason
	}
	return teamAnchorInclusionReason
}

func teamAnchorCohortRationaleFor(kind contextfabric.SubjectKind) string {
	if kind == contextfabric.SubjectRepository {
		return teamAnchorRepositoryCohortRationale
	}
	return teamAnchorCohortRationale
}
