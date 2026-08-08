package clickhouse

import (
	"context"
	"errors"
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

// Both hand-rolled SourceQuery.Statements in this file (quality-boundary.v1
// below, provenance-boundary.v1 further down) must project all 11 columns
// scanEvidenceRow (source_executor.go) now Scans, ending in event_at --
// QueryEvidence wraps the raw Statement in a bare `SELECT * FROM (...)`, so
// the column count/order come entirely from the Statement itself, not from
// standardColumns (source_queries.go), which only the catalog-driven
// queries go through. CHAOS-3562 added event_at as an 11th Scan
// destination without updating these two ad-hoc test fixtures, so the CI
// hosted-integration job (make hosted-integration -> the native ClickHouse
// fixture check) failed with "expected 10 destination arguments in Scan,
// not 11" from the moment that landed -- undetected here because this file
// only runs against a real ClickHouse container (ACR_HOSTED_INTEGRATION=1),
// which local `go test ./...` runs skip by design.
func TestIntegrationSourceExecutor_filters_and_deduplicates_before_read_cap(t *testing.T) {
	// Given
	client, _ := integrationClient(t)
	executor := contextpacket.NewClickHouseSourceExecutor(client)
	query := contextpacket.SourceQuery{
		ID:    "quality-boundary.v1",
		Scope: contextpacket.EvidenceScopeRepo,
		Statement: `SELECT
			concat('acr:v1:test:', toString(number)) evidence_ref_id,
			'dev_health' system,
			'signal' entity_type,
			if(number < 100, concat('low-', toString(number)), if(number < 200, 'duplicate', 'target')) entity_id,
			if(number < 100, concat('low ', toString(number)), if(number < 200, 'duplicate', 'target')) display_label,
			'' safe_uri,
			'native' provenance,
			if(number < 100, 0.1, 0.9) confidence,
			if(number < 100, 'low quality', if(number < 200, 'duplicate', 'target')) citation,
			toDateTime(toUInt32(1700000000 + (201 - number))) observed_at,
			CAST(NULL AS Nullable(DateTime64(3, 'UTC'))) event_at
		FROM numbers(201)`,
	}

	// When
	evidence, err := executor.QueryEvidence(
		context.Background(),
		query,
		[]contextpacket.ClickHouseBinding{{Name: "include_low_confidence", Value: uint8(0)}},
	)

	// Then
	if err != nil {
		cause := err
		for errors.Unwrap(cause) != nil {
			cause = errors.Unwrap(cause)
		}
		t.Fatalf("query quality boundary: %v: %v", err, cause)
	}
	if len(evidence) != 2 || evidence[0].Source.EntityID != "duplicate" || evidence[1].Source.EntityID != "target" {
		t.Fatalf("evidence = %#v, want one duplicate representative plus target", evidence)
	}
}

func TestIntegrationSourceExecutor_preserves_ranked_provenance_before_read_cap(t *testing.T) {
	// Given
	client, _ := integrationClient(t)
	executor := contextpacket.NewClickHouseSourceExecutor(client)
	query := contextpacket.SourceQuery{
		ID:    "provenance-boundary.v1",
		Scope: contextpacket.EvidenceScopeRepo,
		Statement: `SELECT
			concat('acr:v1:test:', toString(number)) evidence_ref_id,
			'dev_health' system,
			'signal' entity_type,
			multiIf(number = 0, 'duplicate', number <= 100, concat('distractor-', toString(number)), number = 101, 'duplicate', 'target') entity_id,
			multiIf(number = 0, 'duplicate', number <= 100, concat('distractor ', toString(number)), number = 101, 'duplicate', 'target') display_label,
			'' safe_uri,
			if(number >= 101, 'native', 'heuristic') provenance,
			0.9 confidence,
			multiIf(number = 0 OR number = 101, 'duplicate', number <= 100, 'distractor', 'target') citation,
			toDateTime(toUInt32(1700000000 + (103 - number))) observed_at,
			CAST(NULL AS Nullable(DateTime64(3, 'UTC'))) event_at
		FROM numbers(103)`,
	}

	// When
	evidence, err := executor.QueryEvidence(
		context.Background(),
		query,
		[]contextpacket.ClickHouseBinding{{Name: "include_low_confidence", Value: uint8(0)}},
	)

	// Then
	if err != nil {
		cause := err
		for errors.Unwrap(cause) != nil {
			cause = errors.Unwrap(cause)
		}
		t.Fatalf("query provenance boundary: %v: %v", err, cause)
	}
	targetFound, duplicateCount := false, 0
	for _, ref := range evidence {
		switch ref.Source.EntityID {
		case "target":
			targetFound = true
		case "duplicate":
			duplicateCount++
			if ref.Provenance != "native" {
				t.Fatalf("duplicate provenance = %q, want native", ref.Provenance)
			}
		}
	}
	if len(evidence) != 100 || !targetFound || duplicateCount != 1 {
		t.Fatalf("evidence count=%d target=%t duplicate_count=%d, want 100 rows including target and one native duplicate", len(evidence), targetFound, duplicateCount)
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
