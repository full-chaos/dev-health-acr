package falkorgraph

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamOwnedRepositories returns the repositories a team currently owns that the
// caller may read (CHAOS-12557): the tree walk team -> repository over the
// current OWNED_BY_TEAM edges, the same walk a team's repository cohort takes.
// Team = ownership only. complete is false when the walk was cut or the team owns
// more than limit readable repositories: the list is then not the whole set.
func (a *Adapter) teamOwnedRepositories(ctx context.Context, key string, principal storage.Principal, scope contextfabric.RequestedScope, team contextfabric.SubjectRef, limit int) ([]contextfabric.SubjectRef, bool, error) {
	walk, err := a.teamAnchorMembers(ctx, key, principal.OrgID, principal, scope, team, treeRepository, limit+1, temporalFilter{}, memberStatusPredicate{})
	if err != nil {
		return nil, false, err
	}
	repositories := make([]contextfabric.SubjectRef, 0, len(walk.nodes))
	for _, node := range walk.nodes {
		if subject, ok := graphrank.NodeSubject(node); ok && subject.Kind == contextfabric.SubjectRepository {
			repositories = append(repositories, subject)
		}
	}
	complete := !walk.truncated && !walk.anchorDenied && len(repositories) <= limit
	if len(repositories) > limit {
		repositories = repositories[:limit]
	}
	return repositories, complete, nil
}
