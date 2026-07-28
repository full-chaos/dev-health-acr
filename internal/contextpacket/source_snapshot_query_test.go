package contextpacket_test

import (
	"strings"
	"testing"
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
	}{
		{name: "hotspots", sourceID: "file_hotspots.v1", table: "file_hotspot_daily", dateColumn: "day", runIdentity: "(day, computed_at)", latestColumns: "day, computed_at", latestOrder: "day DESC, computed_at DESC"},
		{name: "complexity", sourceID: "file_complexity.v1", table: "file_complexity_snapshots", dateColumn: "as_of_day", runIdentity: "(ref, as_of_day, computed_at)", latestColumns: "ref, as_of_day, computed_at", latestOrder: "as_of_day DESC, computed_at DESC, ref ASC"},
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
			if strings.Contains(statement, "force_optimize_projection_name") {
				t.Fatalf("%s latest-run lookup forces a projection the engine may reject: %s", tt.sourceID, statement)
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
