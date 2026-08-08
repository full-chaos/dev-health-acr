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
	// work_items (ops migration 009_raw_work_items.sql) carries genuine
	// provider lifecycle timestamps distinct from updated_at (already used
	// for observed_at): closed_at/completed_at/started_at are the real
	// event columns, falling back to the always-present created_at.
	"work_items.v1": "coalesce(closed_at, completed_at, started_at, created_at)",
	// pull_requests (git_pull_requests) carries merged_at/closed_at/created_at.
	// The most significant PR lifecycle event is its merge, else its close,
	// else its creation.
	"pull_requests.v1": "coalesce(p.merged_at, p.closed_at, p.created_at)",
}

const eventAtAbsentExpression = "CAST(NULL AS Nullable(DateTime64(3, 'UTC'))) event_at"

func TestCatalogSourceQueries_populateEventAtOnlyFromRealEventTimes(t *testing.T) {
	// Given the full catalog of source queries. Each entry is read directly
	// off the catalog -- not through an intermediate map[string]string keyed
	// by ID -- so a duplicate source ID cannot silently mask one of the two
	// statements from ever being checked.
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		query := query
		t.Run(query.ID, func(t *testing.T) {
			statement := query.Statement
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

// TestCatalogSourceQueries_selectStatementIncludesEventAtColumn pins the
// standardColumns event_at projection over every catalog statement, not just
// the first: standardColumns is a shared prefix, so a source built without it
// (or with a stale copy predating event_at) would otherwise go unnoticed as
// long as any other single entry in the catalog still used it.
func TestCatalogSourceQueries_selectStatementIncludesEventAtColumn(t *testing.T) {
	const standardEventAtProjection = "SELECT evidence_ref_id, system, entity_type, entity_id, display_label, safe_uri, provenance, toFloat64(confidence) confidence, citation, observed_at, event_at FROM ("
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if !strings.Contains(query.Statement, standardEventAtProjection) {
			t.Fatalf("%s: standard column projection must expose event_at alongside observed_at: %s", query.ID, query.Statement)
		}
	}
}

// TestSourceQueriesWithRealEventTime_keysMatchLiveCatalog guards against a
// stale or renamed source ID in sourceQueriesWithRealEventTime: if the
// catalog ever removes or renames a source listed here, that key would
// otherwise never run in
// TestCatalogSourceQueries_populateEventAtOnlyFromRealEventTimes (which only
// iterates the catalog, keyed by the catalog's own IDs) and silently stop
// testing anything.
func TestSourceQueriesWithRealEventTime_keysMatchLiveCatalog(t *testing.T) {
	catalogIDs := make(map[string]struct{}, len(contextpacket.SourceQueryCatalogV1))
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		catalogIDs[query.ID] = struct{}{}
	}
	for id := range sourceQueriesWithRealEventTime {
		if _, ok := catalogIDs[id]; !ok {
			t.Fatalf("sourceQueriesWithRealEventTime references %q, which is not a source ID in the live SourceQueryCatalogV1 (stale or renamed key)", id)
		}
	}
}
