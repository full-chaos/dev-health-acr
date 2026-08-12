package devhealthsource_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestTeamsProjectsSourceDisabledByDefaultIsANoop(t *testing.T) {
	t.Parallel()
	source := devhealthsource.NewTeamsProjectsSource(false)
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("disabled source returned an error: %v", err)
	}
	if available {
		t.Fatalf("disabled source must never claim a batch is available: %+v", batch)
	}
}

func TestTeamsProjectsSourceEnabledWithoutImplementationFailsLoudly(t *testing.T) {
	t.Parallel()
	source := devhealthsource.NewTeamsProjectsSource(true)
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.TeamsProjectsSourceName}); err == nil {
		t.Fatal("enabling teams/projects ahead of a real implementation must fail loudly, not silently no-op")
	}
}
