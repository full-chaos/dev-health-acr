package falkorgraph

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

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

// teamAnchorMemberPosition is the tree position a team anchor's members are
// read at, and whether the subject is such an anchor: a committed team whose
// own reach is the member set of a question for its projects or repositories.
// Every other anchor, and every other member kind, stays on the generic walk.
func teamAnchorMemberPosition(anchors []contextfabric.SubjectRef, subject contextfabric.SubjectRef, declaredKind contextfabric.SubjectKind) (treePosition, bool) {
	found := false
	for _, anchor := range anchors {
		found = found || anchor == subject
	}
	if !found {
		return "", false
	}
	switch declaredKind {
	case contextfabric.SubjectRepository:
		return treeRepository, true
	case contextfabric.SubjectProject:
		return treeProject, true
	}
	return "", false
}

// ownedMemberStates is the lifecycle state each owned member carries, keyed by
// subject: a member the source marks completed, canceled or archived is still
// owned, and says so on its row.
func ownedMemberStates(nodes []graphrank.CandidateNode) map[string]string {
	states := map[string]string{}
	for _, n := range nodes {
		state := strings.TrimSpace(propStringValue(n.Attributes[propPropertyPrefix+"state"]))
		archived := false
		if active, ok := n.Attributes[propPropertyPrefix+"is_active"].(bool); ok {
			archived = !active
		}
		if state == "" && !archived {
			continue
		}
		subject, ok := graphrank.NodeSubject(n)
		if !ok {
			continue
		}
		reason := "Project state: " + state + "."
		switch {
		case state == "":
			reason = "Project is archived."
		case archived:
			reason = "Project state: " + state + "; archived."
		}
		states[graphrank.SubjectKey(subject)] = reason
	}
	return states
}
