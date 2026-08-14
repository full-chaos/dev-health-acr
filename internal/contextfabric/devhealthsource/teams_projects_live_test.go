package devhealthsource_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
)

// TestTeamsProjectsSourceAgainstLiveClickHouse runs the real
// TeamsProjectsSource against a real ClickHouse through the real Go driver.
//
// This closes a gap the package's fake cannot: fakeScanner hands Go values
// straight to Scan, so it proves nothing about ClickHouse column types. That
// is exactly the CHAOS-3789 class of bug (git_pull_requests.number modelled
// as int64 when production is UInt32; the native driver rejects the
// conversion outright and no test caught it). This source's producers scan
// several shapes the fake cannot vouch for at all -- is_active UInt8,
// source/confidence Enum8 through toString(), team_id Nullable(String)
// through ifNull, a Nullable(DateTime64) collapsed by
// `max(valid_to IS NULL)` into a UInt8, and teams.updated_at's
// DateTime64(6) with NO timezone qualifier compared against a
// DateTime64(6,'UTC') bind parameter.
//
// Gated behind ACR_CLICKHOUSE_INTEGRATION_DSN like this package's other
// live check: only a database already carrying production's real schema can
// answer the question, which a fresh empty container never does.
func TestTeamsProjectsSourceAgainstLiveClickHouse(t *testing.T) {
	dsn := os.Getenv("ACR_CLICKHOUSE_INTEGRATION_DSN")
	if dsn == "" {
		if os.Getenv("ACR_CLICKHOUSE_INTEGRATION_REQUIRED") == "1" {
			t.Fatal("ACR_CLICKHOUSE_INTEGRATION_DSN is required when native ClickHouse integration is mandatory")
		}
		t.Skip("ACR_CLICKHOUSE_INTEGRATION_DSN is required to run the teams/projects producers against real ClickHouse")
	}
	orgID := os.Getenv("ACR_CLICKHOUSE_INTEGRATION_ORG_ID")
	if orgID == "" {
		t.Skip("ACR_CLICKHOUSE_INTEGRATION_ORG_ID is required to name the organization to project")
	}

	ctx := context.Background()
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: dsn, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open live ClickHouse query client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}

	// Walk every page rather than only the first: the ground-truth org's
	// edge volume (thousands of work-item edges) exceeds the contract's
	// per-batch relationship bound, so page one is the oversized-fallback
	// path and later pages are ordinary keyset-paginated catch-up. Every
	// producer's Scan must survive both.
	kinds := map[contractsv1.ContextFabricSubjectKind]int{}
	types := map[contractsv1.ContextFabricRelationshipType]int{}
	cursor := ""
	for page := 0; page < 64; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: NextProjectionBatch against live ClickHouse: %v", page, err)
		}
		if !available {
			break
		}
		for _, entity := range batch.Entities {
			kinds[entity.Subject.Kind]++
		}
		for _, relationship := range batch.Relationships {
			types[relationship.Type]++
		}
		if batch.NextCursor == cursor {
			t.Fatalf("page %d: cursor did not advance (%q) -- projection would loop forever", page, cursor)
		}
		cursor = batch.NextCursor
	}

	for _, kind := range []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject} {
		if kinds[kind] == 0 {
			t.Fatalf("no %s subjects projected from live ClickHouse for org %s; got %v", kind, orgID, kinds)
		}
	}
	for _, kind := range devhealthsource.TeamsProjectsRelationshipTypes() {
		if types[kind] == 0 {
			t.Fatalf("no %s relationships projected from live ClickHouse for org %s; got %v", kind, orgID, types)
		}
	}
	t.Logf("live projection: entities=%v relationships=%v", kinds, types)
}
