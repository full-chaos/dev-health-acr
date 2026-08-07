package contextpacket_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// sourceQueriesWithRealEventTime lists the catalog sources whose underlying
// ClickHouse tables carry a genuine real-world event timestamp distinct from
// mere ingestion/sync bookkeeping (last_synced, discovered_at, or a table's
// own generic observed_at column). Every other source in the catalog leaves
// event_at absent rather than synthesizing one from a sync watermark.
var sourceQueriesWithRealEventTime = map[string]string{
	"git_commits.v1":          "c.committer_when",
	"pull_request_reviews.v1": "r.submitted_at",
	"ci_pipeline_runs.v1":     "coalesce(c.finished_at, c.started_at)",
	"deployments.v1":          "coalesce(d.deployed_at, d.started_at)",
	"incidents.v1":            "coalesce(i.started_at, i.source_event_at)",
}

const eventAtAbsentExpression = "CAST(NULL AS Nullable(DateTime64(3, 'UTC'))) event_at"

func TestCatalogSourceQueries_populateEventAtOnlyFromRealEventTimes(t *testing.T) {
	// Given the full catalog of source queries.
	queries := make(map[string]string, len(contextpacket.SourceQueryCatalogV1))
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		queries[query.ID] = query.Statement
	}

	for _, query := range contextpacket.SourceQueryCatalogV1 {
		query := query
		t.Run(query.ID, func(t *testing.T) {
			statement := queries[query.ID]
			if expr, hasRealEventTime := sourceQueriesWithRealEventTime[query.ID]; hasRealEventTime {
				// When a source has a real event time, event_at must be filled
				// from that exact expression -- never a sync/ingestion fallback.
				want := expr + " event_at"
				if !strings.Contains(statement, want) {
					t.Fatalf("%s must populate event_at from its real event time (%q):\n%s", query.ID, want, statement)
				}
				if strings.Contains(statement, eventAtAbsentExpression) {
					t.Fatalf("%s has a real event time but also emits a NULL event_at literal", query.ID)
				}
			} else {
				// When no real event time exists, event_at must be left absent,
				// never synthesized from observed_at or a sync watermark.
				if !strings.Contains(statement, eventAtAbsentExpression) {
					t.Fatalf("%s must leave event_at absent (never synthesized):\n%s", query.ID, statement)
				}
			}
		})
	}
}

func TestCatalogSourceQueries_selectStatementIncludesEventAtColumn(t *testing.T) {
	if !strings.Contains(contextpacket.SourceQueryCatalogV1[0].Statement, "SELECT evidence_ref_id, system, entity_type, entity_id, display_label, safe_uri, provenance, toFloat64(confidence) confidence, citation, observed_at, event_at FROM (") {
		t.Fatalf("standard column projection must expose event_at alongside observed_at: %s", contextpacket.SourceQueryCatalogV1[0].Statement)
	}
}
