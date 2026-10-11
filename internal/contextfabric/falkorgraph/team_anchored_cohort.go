package falkorgraph

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// anchoredMemberPositions is, per anchor kind, the member kinds whose members
// are read over the anchor's own path of the entity tree, and the tree position
// each is read at. A member the question text happens to match, or only another
// committed subject reaches, is not a member, and the lexical arms never admit
// one. A repository anchor's teams stay with ownership routing, and an anchor
// or member kind off the tree (organization, incident) stays on the generic
// walk.
var anchoredMemberPositions = map[contextfabric.SubjectKind]map[contextfabric.SubjectKind]treePosition{
	contextfabric.SubjectTeam: {
		contextfabric.SubjectProject:     treeProject,
		contextfabric.SubjectRepository:  treeRepository,
		contextfabric.SubjectPullRequest: treePullRequest,
	},
	contextfabric.SubjectProject: {
		contextfabric.SubjectRepository:  treeRepository,
		contextfabric.SubjectTeam:        treeTeam,
		contextfabric.SubjectPullRequest: treePullRequest,
	},
	contextfabric.SubjectRepository: {
		contextfabric.SubjectProject:     treeProject,
		contextfabric.SubjectPullRequest: treePullRequest,
	},
}

// teamAnchoredCohort is the committed anchor subjects (a team, a project or a
// repository) whose own reach over the entity tree is the member set of a
// question for the members of that anchor, empty for any other question.
func teamAnchoredCohort(request contextfabric.GraphDiscoveryRequest, declaredKind contextfabric.SubjectKind) []contextfabric.SubjectRef {
	anchors := contextfabric.ScopeAnchorSubjects(request.Frame, request.ScopeAnchorKind, request.Resolution)
	if len(anchors) == 0 {
		return nil
	}
	if _, ok := anchoredMemberPositions[anchors[0].Kind][declaredKind]; !ok {
		return nil
	}
	return anchors
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

func teamAnchorInclusionReasonFor(anchorKind, kind contextfabric.SubjectKind) string {
	if anchorKind != contextfabric.SubjectTeam || kind == contextfabric.SubjectPullRequest {
		return "Member reached from the " + string(anchorKind) + " the question names over the entity tree in the authorized Context Fabric graph."
	}
	if kind == contextfabric.SubjectRepository {
		return teamAnchorRepositoryInclusionReason
	}
	return teamAnchorInclusionReason
}

func teamAnchorCohortRationaleFor(anchorKind, kind contextfabric.SubjectKind) string {
	if anchorKind != contextfabric.SubjectTeam || kind == contextfabric.SubjectPullRequest {
		return "Members were reached from the " + string(anchorKind) + " the question names over the entity tree in the authorized Context Fabric graph."
	}
	if kind == contextfabric.SubjectRepository {
		return teamAnchorRepositoryCohortRationale
	}
	return teamAnchorCohortRationale
}

// teamAnchorMemberPosition is the tree position an anchor's members are read
// at, and whether the subject is such an anchor. Every other anchor, and every
// other member kind, stays on the generic walk.
func teamAnchorMemberPosition(anchors []contextfabric.SubjectRef, subject contextfabric.SubjectRef, declaredKind contextfabric.SubjectKind) (treePosition, bool) {
	found := false
	for _, anchor := range anchors {
		found = found || anchor == subject
	}
	if !found {
		return "", false
	}
	position, ok := anchoredMemberPositions[subject.Kind][declaredKind]
	return position, ok
}

// ownedMemberStates is the lifecycle state each owned member carries, keyed by
// subject: a member the source marks completed, canceled or archived is still
// owned, and says so on its row.
func ownedMemberStates(nodes []graphrank.CandidateNode) map[string]string {
	states := map[string]string{}
	for _, n := range nodes {
		if kind, ok := graphrank.NodeSubject(n); ok && kind.Kind != contextfabric.SubjectProject && kind.Kind != contextfabric.SubjectRepository {
			continue
		}
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

// anchorLinkTierReason says, for a member reached over the issue <> pull
// request link, which tier of that link reached it. A native link is stated as
// native and nothing lower is: explicit_text is a link read from text, and
// heuristic an inferred one.
func anchorLinkTierReason(tier string) string {
	switch tier {
	case contextfabric.TreeLinkTierNative:
		return "Reached through a native issue and pull request link."
	case contextfabric.TreeLinkTierExplicitText:
		return "Reached through an issue and pull request link read from text, not a native link."
	case contextfabric.TreeLinkTierHeuristic:
		return "Reached through an inferred issue and pull request link, not a native link."
	}
	return ""
}
