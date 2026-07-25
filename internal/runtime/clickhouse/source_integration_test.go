package clickhouse

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestIntegrationCodeMetrics_return_only_latest_replacement_run_under_read_cap(t *testing.T) {
	// Given
	_, options := integrationClient(t)
	options.MaxBytesToRead = 16 << 20
	client, err := NewClickHouseQueryClientWithOptions(options)
	if err != nil {
		t.Fatalf("create read-limited client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	asOf := time.Date(2026, time.January, 14, 12, 0, 0, 0, time.UTC)
	plan := contextpacket.ReadPlan{
		OrgID:    "acr-integration-org",
		RepoID:   "00000000-3065-4000-8000-000000000001",
		RepoSlug: "example-org/widget-service",
		Branch:   "main",
		AsOf:     &asOf,
	}
	executor := contextpacket.NewClickHouseSourceExecutor(client)

	for _, sourceID := range []string{"file_hotspots.v1", "file_complexity.v1"} {
		t.Run(sourceID, func(t *testing.T) {
			// When
			evidence, queryErr := executor.QueryEvidence(
				context.Background(),
				integrationSourceQuery(t, sourceID),
				plan.Bindings(),
			)

			// Then
			if queryErr != nil {
				t.Fatalf("query latest replacement run: %v", queryErr)
			}
			if len(evidence) != 1 || evidence[0].Source.EntityID != "src/checkout/cart_drawer.ts" {
				t.Fatalf("evidence = %#v, want only the latest-run cart drawer row", evidence)
			}
		})
	}
}

func TestIntegrationCodeMetrics_do_not_restore_explicit_file_from_replaced_run(t *testing.T) {
	// Given
	client, _ := integrationClient(t)
	asOf := time.Date(2026, time.January, 14, 12, 0, 0, 0, time.UTC)
	plan := contextpacket.ReadPlan{
		OrgID:    "acr-integration-org",
		RepoID:   "00000000-3065-4000-8000-000000000001",
		RepoSlug: "example-org/widget-service",
		Branch:   "main",
		Files:    []string{"src/checkout/replaced.ts"},
		AsOf:     &asOf,
	}
	executor := contextpacket.NewClickHouseSourceExecutor(client)

	for _, sourceID := range []string{"file_hotspots.v1", "file_complexity.v1"} {
		t.Run(sourceID, func(t *testing.T) {
			// When
			evidence, queryErr := executor.QueryEvidence(
				context.Background(),
				integrationSourceQuery(t, sourceID),
				plan.Bindings(),
			)

			// Then
			if queryErr != nil {
				t.Fatalf("query replaced file: %v", queryErr)
			}
			if len(evidence) != 0 {
				t.Fatalf("evidence = %#v, want no row restored from the replaced run", evidence)
			}
		})
	}
}

func TestIntegrationFileComplexity_absent_ref_succeeds_under_read_cap(t *testing.T) {
	// Given
	_, options := integrationClient(t)
	options.MaxBytesToRead = 32 << 10
	client, err := NewClickHouseQueryClientWithOptions(options)
	if err != nil {
		t.Fatalf("create read-limited client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	plan := contextpacket.ReadPlan{
		OrgID:    "acr-integration-org",
		RepoID:   "00000000-3065-4000-8000-000000000001",
		RepoSlug: "example-org/widget-service",
		Branch:   "release/absent",
	}

	// When
	evidence, err := contextpacket.NewClickHouseSourceExecutor(client).QueryEvidence(
		context.Background(),
		integrationSourceQuery(t, "file_complexity.v1"),
		plan.Bindings(),
	)

	// Then
	if err != nil {
		t.Fatalf("query absent complexity ref under read cap: %v", err)
	}
	if len(evidence) != 0 {
		t.Fatalf("evidence = %#v, want no rows for absent ref", evidence)
	}
}

func TestIntegrationFileComplexity_empty_branch_chooses_one_ref_for_tied_run_time(t *testing.T) {
	// Given
	client, _ := integrationClient(t)
	asOf := time.Date(2026, time.January, 14, 12, 0, 0, 0, time.UTC)
	plan := contextpacket.ReadPlan{
		OrgID:    "acr-integration-org",
		RepoID:   "00000000-3065-4000-8000-000000000001",
		RepoSlug: "example-org/widget-service",
		AsOf:     &asOf,
	}

	// When
	evidence, err := contextpacket.NewClickHouseSourceExecutor(client).QueryEvidence(
		context.Background(),
		integrationSourceQuery(t, "file_complexity.v1"),
		plan.Bindings(),
	)

	// Then
	if err != nil {
		t.Fatalf("query tied complexity refs: %v", err)
	}
	if len(evidence) != 1 || evidence[0].Source.EntityID != "src/checkout/cart_drawer.ts" {
		t.Fatalf("evidence = %#v, want only the deterministic main-ref row", evidence)
	}
}

func integrationSourceQuery(t *testing.T, sourceID string) contextpacket.SourceQuery {
	t.Helper()
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == sourceID {
			return query
		}
	}
	t.Fatalf("source query %q not found", sourceID)
	return contextpacket.SourceQuery{}
}
