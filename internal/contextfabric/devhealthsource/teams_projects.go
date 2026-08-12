package devhealthsource

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TeamsProjectsSourceName is the Source value a TeamsProjectsSource writes.
const TeamsProjectsSourceName = "dev_health_teams_projects"

// TeamsProjectsSource is the documented seam for Team and Project entities.
// There is no canonical ClickHouse table (or any other adapter) for either
// kind in this repository today -- see
// docs/design/context-fabric-projection-worker.md. NewDisabledTeamsProjectsSource
// returns a source that always reports "nothing to project" rather than
// fabricating data; wiring a real implementation here is future work once
// Dev Health Ops publishes a canonical team/project source.
type TeamsProjectsSource struct{ enabled bool }

// NewTeamsProjectsSource returns a TeamsProjectsSource. When enabled is
// false (the default -- see ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED)
// it is a documented no-op, not a partially-working adapter.
func NewTeamsProjectsSource(enabled bool) *TeamsProjectsSource {
	return &TeamsProjectsSource{enabled: enabled}
}

func (s *TeamsProjectsSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if s == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: teams/projects source is not configured")
	}
	if !s.enabled {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	// Enabling this flag ahead of a real implementation would silently
	// promise Team/Project coverage that does not exist; fail loudly
	// instead of returning an empty batch that looks like "caught up".
	return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: %s is enabled but has no canonical source implementation yet for org %s/%s", TeamsProjectsSourceName, checkpoint.OrgID, checkpoint.Source)
}
