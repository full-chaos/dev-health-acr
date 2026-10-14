package devhealthschema

import (
	"strings"
)

// Projection is one production projection a reader relies on for a bounded
// read. It is declared here, from the ops migration that creates it, so a
// fixture builds the same projection production has and a freshness check can
// compare the declaration with system.projections of an ops-migrated server.
type Projection struct {
	Name string
	// Select and GroupBy are the column lists of the projection's
	// `SELECT <Select> GROUP BY <GroupBy>` body.
	Select  string
	GroupBy string
	// Source names the ops migration that creates it.
	Source string
}

// Projections maps a table to the projections production carries on it.
// file_hotspot_daily's run projection keeps the latest-run lookup bounded
// (ops migration 069_acr_code_metric_access_paths.sql, kept across the
// ReplacingMergeTree conversion by 087 and 096 under
// deduplicate_merge_projection_mode = 'rebuild').
var Projections = map[string][]Projection{
	"file_hotspot_daily": {{
		Name:    "prj_acr_file_hotspot_runs",
		Select:  "org_id, repo_id, day, computed_at",
		GroupBy: "org_id, repo_id, day, computed_at",
		Source:  "ops migrations/clickhouse/069_acr_code_metric_access_paths.sql",
	}},
}

// ProjectionDDL renders, for a table, the statements that give it its
// declared projections: the dedup setting a projection on a
// ReplacingMergeTree needs, then one ADD PROJECTION each.
func ProjectionDDL(table string) []string {
	declared := Projections[table]
	if len(declared) == 0 {
		return nil
	}
	statements := []string{"ALTER TABLE " + table + " MODIFY SETTING deduplicate_merge_projection_mode = 'rebuild'"}
	for _, projection := range declared {
		statements = append(statements, "ALTER TABLE "+table+" ADD PROJECTION IF NOT EXISTS "+projection.Name+
			" (SELECT "+projection.Select+" GROUP BY "+projection.GroupBy+")")
	}
	return statements
}

// ProjectionMatchesLive reports whether a live system.projections query text
// is the declared projection's body, ignoring case and whitespace.
func ProjectionMatchesLive(projection Projection, liveQuery string) bool {
	normalize := func(text string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(text, ",", " , ")), " "))
	}
	want := "select " + projection.Select + " group by " + projection.GroupBy
	return normalize(want) == normalize(liveQuery)
}
