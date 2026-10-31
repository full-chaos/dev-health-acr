package falkorgraph

import (
	"context"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12319: a requested team narrowing (scope.team_ids) on the repository
// work-item walk. Repository, project and work-item nodes carry the "*" team
// list, so the node rule (graphrank.AuthorizedAttributes) narrows none of
// them; "*" is never team evidence. Team = ownership only (the entity tree,
// docs/design/context-fabric-architecture-diagrams.md §3): under a team
// narrowing an issue is admitted when
//
//	(i)  it is an issue of the anchor -- the walk reaches every member through
//	     an actual LINKS_PULL_REQUEST row, its tier named -- and the anchor is
//	     currently OWNED_BY_TEAM a requested team (any current edge, co-owners
//	     included), or
//	(ii) its own project (BELONGS_TO_PROJECT) is currently OWNED_BY_TEAM a
//	     requested team.
//
// An issue that passes neither is out of the requested SCOPE: it is not a
// member and not a denial (never in the walk's authorization count). The walk
// is floored (truncated) only where the ownership it needed to decide is
// absent, not current, or cut -- an absent ownership row is missing data, not
// "not owned" (team-attribution.md R74). The narrowing never widens: an issue
// the caller's grants or the rest of the request refuse stays refused.

// teamNarrowingContextKey carries one walk's team narrowing on ctx, so the
// link read applies it without a new parameter on every walk (as project
// reach does, project_reach.go).
type teamNarrowingContextKey struct{}

// teamNarrowing is one repository walk's requested team narrowing.
type teamNarrowing struct {
	adapter *Adapter
	key     string
	orgID   string
	// requested are the requested team ids, compared exactly with a team
	// node's own authorization_teams.
	requested map[string]bool
	// anchorOwned: (i) holds, so every walked issue passes.
	anchorOwned bool
	// anchorUnknown: (i) could not be decided -- the anchor has no current
	// owner, or the read was cut. It floors the walk only when an issue
	// the rest of the request admits is then left to (ii) and not reached.
	anchorUnknown bool

	mu sync.Mutex
	// undecided: an ownership read the narrowing needed to decide an issue
	// the rest of the request admits was absent, not current, or cut.
	undecided bool
}

// requestedTeamNarrowing returns the requested team ids that narrow: blanks
// do not count, and a requested "*" narrows nothing.
func requestedTeamNarrowing(scope contextfabric.RequestedScope) map[string]bool {
	requested := map[string]bool{}
	for _, id := range scope.TeamIDs {
		switch id = strings.TrimSpace(id); id {
		case "":
		case "*":
			return nil
		default:
			requested[id] = true
		}
	}
	if len(requested) == 0 {
		return nil
	}
	return requested
}

// withTeamNarrowing reads (i) for the anchor and puts the narrowing on ctx. A
// request with no team narrowing gets ctx unchanged and a nil narrowing.
func (a *Adapter) withTeamNarrowing(ctx context.Context, key, orgID string, scope contextfabric.RequestedScope, anchor contextfabric.SubjectRef) (context.Context, *teamNarrowing, error) {
	requested := requestedTeamNarrowing(scope)
	if requested == nil {
		return ctx, nil, nil
	}
	n := &teamNarrowing{adapter: a, key: key, orgID: orgID, requested: requested}
	hits, cut, err := a.walkStepHits(ctx, key, orgID, []string{anchor.CanonicalID}, repositoryOwnersStep, currentOwnership(temporalFilter{}, a.now()), exactNameCandidateQueryLimit)
	if err != nil {
		return ctx, nil, err
	}
	n.anchorOwned = n.anyRequested(hits)
	n.anchorUnknown = !n.anchorOwned && (cut || len(hits) == 0)
	return context.WithValue(ctx, teamNarrowingContextKey{}, n), n, nil
}

// teamNarrowingFrom returns the walk's team narrowing, or nil.
func teamNarrowingFrom(ctx context.Context) *teamNarrowing {
	n, _ := ctx.Value(teamNarrowingContextKey{}).(*teamNarrowing)
	return n
}

// issueProjectStep reads an issue's current projects.
var issueProjectStep = walkStep{
	fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectProject,
	relation: contractsv1.ContextFabricRelationshipBelongsToProject, direction: walkOut, edgeWindowOnly: true,
}

// projectOwnersStep reads a project's current owning teams. The edge says the
// team owns the project; a project whose own validity ended is still owned.
var projectOwnersStep = walkStep{
	fromKind: contractsv1.ContextFabricSubjectProject, toKind: contractsv1.ContextFabricSubjectTeam,
	relation: contractsv1.ContextFabricRelationshipOwnedByTeam, direction: walkOut, edgeWindowOnly: true,
}

// excluded returns the issues among ids that are out of the narrowing: (i)
// does not hold and (ii) does not reach a requested team. ids are the issues
// the rest of the request already admits (review r1): an issue refused there
// never floors the walk. A nil narrowing, or one whose anchor a requested
// team owns, excludes nothing and reads nothing.
func (n *teamNarrowing) excluded(ctx context.Context, ids []string) (map[string]bool, error) {
	if n == nil || n.anchorOwned || len(ids) == 0 {
		return nil, nil
	}
	current := currentOwnership(temporalFilter{}, n.adapter.now())
	projects, cut, err := n.adapter.walkStepHits(ctx, n.key, n.orgID, ids, issueProjectStep, current, exactNameCandidateQueryLimit)
	if err != nil {
		return nil, err
	}
	projectsOf := map[string][]string{}
	var projectIDs []string
	seen := map[string]bool{}
	for _, h := range projects {
		project := canonicalIDOf(h.to)
		projectsOf[h.from] = append(projectsOf[h.from], project)
		if !seen[project] {
			seen[project] = true
			projectIDs = append(projectIDs, project)
		}
	}
	owners, ownersCut, err := n.adapter.walkStepHits(ctx, n.key, n.orgID, projectIDs, projectOwnersStep, current, exactNameCandidateQueryLimit)
	if err != nil {
		return nil, err
	}
	owned, hasOwner := map[string]bool{}, map[string]bool{}
	for _, h := range owners {
		hasOwner[h.from] = true
		if n.isRequested(h.to) {
			owned[h.from] = true
		}
	}
	undecided := false
	out := map[string]bool{}
	for _, id := range ids {
		reached, missing := false, false
		for _, project := range projectsOf[id] {
			if owned[project] {
				reached = true
			} else if !hasOwner[project] {
				// No current owner: missing data, not "not owned".
				missing = true
			}
		}
		if reached {
			continue
		}
		out[id] = true
		// The issue is left out on ownership that was not all there: (i)
		// undecided, a project of its with no current owner, or a cut read.
		if n.anchorUnknown || missing || cut || ownersCut {
			undecided = true
		}
	}
	if undecided {
		n.mu.Lock()
		n.undecided = true
		n.mu.Unlock()
	}
	return out, nil
}

// Undecided reports whether an ownership read the narrowing needed was
// absent, not current, or cut.
func (n *teamNarrowing) Undecided() bool {
	if n == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.undecided
}

func (n *teamNarrowing) anyRequested(hits []walkHit) bool {
	for _, h := range hits {
		if n.isRequested(h.to) {
			return true
		}
	}
	return false
}

// isRequested reports whether a team node is a requested team: its own
// authorization_teams list names one exactly. The "*" team value is a string,
// not a list, and requested never holds "*" (requestedTeamNarrowing), so the
// wildcard proves nothing.
func (n *teamNarrowing) isRequested(team *node) bool {
	if team == nil {
		return false
	}
	ids, _ := team.Properties[propAuthzTeams].([]string)
	for _, id := range ids {
		if n.requested[strings.TrimSpace(id)] {
			return true
		}
	}
	return false
}
