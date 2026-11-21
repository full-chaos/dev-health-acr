package contextpacket_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestSourceCatalog_bounds_code_metric_aggregation_to_latest_snapshot(t *testing.T) {
	tests := []struct {
		name          string
		sourceID      string
		table         string
		dateColumn    string
		runIdentity   string
		latestColumns string
		latestOrder   string
		projection    string
	}{
		{name: "hotspots", sourceID: "file_hotspots.v1", table: "file_hotspot_daily", dateColumn: "day", runIdentity: "(day, computed_at)", latestColumns: "day, computed_at", latestOrder: "day DESC, computed_at DESC", projection: "prj_acr_file_hotspot_runs"},
		{name: "complexity", sourceID: "file_complexity.v1", table: "file_complexity_snapshots", dateColumn: "as_of_day", runIdentity: "(ref, as_of_day, computed_at)", latestColumns: "ref, as_of_day, computed_at", latestOrder: "as_of_day DESC, computed_at DESC, ref ASC", projection: "prj_acr_file_complexity_runs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			statement := catalogQuery(t, tt.sourceID).Statement

			// Then
			latestSnapshot := tt.runIdentity + " = (SELECT " + tt.latestColumns + " FROM " + tt.table
			if !strings.Contains(statement, latestSnapshot) {
				t.Fatalf("%s does not bound aggregation to its latest replacement run: %s", tt.sourceID, statement)
			}
			if !strings.Contains(statement, "ORDER BY "+tt.latestOrder+" LIMIT 1") {
				t.Fatalf("%s latest-run lookup does not stop after the newest run tuple: %s", tt.sourceID, statement)
			}
			if !strings.Contains(statement, "force_optimize_projection_name='"+tt.projection+"'") {
				t.Fatalf("%s latest-run lookup does not require its bounded run projection: %s", tt.sourceID, statement)
			}
			owned := false
			for _, projection := range devhealthschema.Projections[tt.table] {
				owned = owned || projection.Name == tt.projection
			}
			if !owned {
				t.Fatalf("%s forces %s, which devhealthschema.Projections does not declare on %s", tt.sourceID, tt.projection, tt.table)
			}
			if strings.Count(statement, "org_id = {org_id:String}") < 2 || strings.Count(statement, "repo_id = {repo_id:UUID}") < 2 {
				t.Fatalf("%s latest-snapshot subquery is not independently scoped: %s", tt.sourceID, statement)
			}
			if strings.Count(statement, "empty({files:Array(String)})") != 1 {
				t.Fatalf("%s applies the file filter before selecting the latest snapshot: %s", tt.sourceID, statement)
			}
			if strings.Contains(statement, "argMax(") || strings.Contains(statement, " GROUP BY file_path") {
				t.Fatalf("%s still scans historical rows after selecting an exact replacement run: %s", tt.sourceID, statement)
			}
		})
	}
}

// A statement that forces a projection fails on a server that lacks it, so
// every forced name must be one devhealthschema declares. Scope: a plain-text
// match of the compact force_optimize_projection_name='<name>' form in each
// catalog statement, by name only. The table-bound check is the table-driven
// test above. NOT pinned: a spaced, commented, or quoted spelling of the setting.
func TestSourceCatalog_forced_projections_are_declared_in_devhealthschema(t *testing.T) {
	forced := regexp.MustCompile(`force_optimize_projection_name='([^']+)'`)
	declared := map[string]bool{}
	for _, projections := range devhealthschema.Projections {
		for _, projection := range projections {
			declared[projection.Name] = true
		}
	}
	seen := map[string]bool{}
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		for _, match := range forced.FindAllStringSubmatch(query.Statement, -1) {
			seen[match[1]] = true
			if !declared[match[1]] {
				t.Errorf("%s forces projection %s, which devhealthschema.Projections does not declare", query.ID, match[1])
			}
		}
	}
	if !seen["prj_acr_file_hotspot_runs"] || !seen["prj_acr_file_complexity_runs"] {
		t.Fatalf("catalog forced projections = %v, want the hotspot and complexity run projections", seen)
	}
}
