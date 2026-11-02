package falkorgraph

import (
	"context"
	"errors"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12505: the population walk of a requested repository scope is the TEST
// ORACLE of the keyed link read (linkScopedWorkItemsOf) and serves nothing in
// production. Two production implementations of one rule would drift; this one
// is kept to prove, on real stores, that the keyed read's answer for any ids is
// this walk's members intersected with them, wherever the walk is not cut.

// linkScopedIssues returns the issues in a requested repository scope: the
// issues linked to a pull request of a repository the scope names, found by
// the same walk that serves a repository's work items (treeMembers, repository
// to issue, the link rule of admitted). It is the union of that walk over
// every repository of the scope the caller may see, each issue with the
// strongest tier among its admitted links. complete is false when the
// repositories or a walk were cut, or the union is larger than limit: the set
// is then not the whole population.
func (a *Adapter) linkScopedIssues(ctx context.Context, key string, principal storage.Principal, scope contextfabric.RequestedScope, limit int) (map[string]string, bool, error) {
	if len(scope.RepositorySlugs) == 0 {
		return nil, false, errors.New("a link-scoped issue read needs a requested repository scope")
	}
	repositories, cut, err := a.cohortKindCensusCandidates(ctx, key, principal.OrgID, []string{string(contextfabric.SubjectRepository)}, temporalFilter{})
	if err != nil {
		return nil, false, err
	}
	complete := !cut
	walked := 0
	members := map[string]string{}
	for _, repository := range repositories {
		if !graphrank.AuthorizedAttributes(principal, scope, repository.Attributes) {
			continue
		}
		anchor, ok := graphrank.NodeSubject(repository)
		if !ok || anchor.Kind != contextfabric.SubjectRepository {
			continue
		}
		if walked == maxLinkScopedRepositories {
			complete = false
			break
		}
		walked++
		// CHAOS-12319 review r1: each repository of the scope is a walk's
		// anchor, with the same requested team narrowing.
		walkCtx, narrowing, err := a.withTeamNarrowing(ctx, key, principal.OrgID, scope, anchor)
		if err != nil {
			return nil, false, err
		}
		walk, err := a.treeMembers(walkCtx, key, principal.OrgID, principal, scope, anchor, treeIssue, limit+1, temporalFilter{})
		if err != nil {
			return nil, false, err
		}
		complete = complete && !walk.truncated && !narrowing.Undecided()
		for _, subject := range walk.linkSubjects {
			tier := walk.memberTiers[subject.CanonicalID]
			if best, seen := members[subject.CanonicalID]; !seen || tierRank(tier) < tierRank(best) {
				members[subject.CanonicalID] = tier
			}
		}
		if len(members) > limit {
			complete = false
			break
		}
	}
	slog.DebugContext(ctx, "context fabric: link-scoped issues of a requested repository scope",
		"repositories_walked", walked, "repositories_cut", cut, "issues", len(members), "complete", complete)
	return members, complete, nil
}
