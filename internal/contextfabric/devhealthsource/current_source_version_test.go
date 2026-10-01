package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// TestEveryProductionSourceReportsTheVersionItsBatchesRecord pins, for the
// ClickHouse and teams/projects producers, the capability the epoch
// activation guard compares against: the version a source reports as current
// is exactly the version its own real batch records in every checkpoint
// (episodes has the same pin beside its own tests). A getter that drifted
// from the batch would refuse every epoch the source ever built; a blank one
// would refuse them as unverifiable.
func TestEveryProductionSourceReportsTheVersionItsBatchesRecord(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	clickhouse, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: baseTables(at)})
	if err != nil {
		t.Fatalf("new clickhouse source: %v", err)
	}
	cases := []struct {
		name   string
		source contextfabric.ProjectionSource
		cursor contextfabric.ProjectionCheckpoint
	}{
		{"clickhouse", clickhouse, contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}},
		{"teams_projects", enabledTeamsProjectsSource(t, liveShapedTeamsProjectsClient()), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}},
	}
	for _, tc := range cases {
		batch, available, err := tc.source.NextProjectionBatch(context.Background(), tc.cursor)
		if err != nil || !available {
			t.Fatalf("%s: next projection batch: available=%v err=%v", tc.name, available, err)
		}
		versioned, ok := tc.source.(contextfabric.ProjectionSourceVersion)
		if !ok {
			t.Fatalf("%s: does not implement ProjectionSourceVersion", tc.name)
		}
		if current := versioned.CurrentProjectionSourceVersion(); current == "" || current != batch.SourceVersion {
			t.Errorf("%s: CurrentProjectionSourceVersion() = %q, batch records %q; want the same non-empty version", tc.name, current, batch.SourceVersion)
		}
	}
}
