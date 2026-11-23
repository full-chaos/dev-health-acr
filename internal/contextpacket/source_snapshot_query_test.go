package contextpacket_test

import (
	"fmt"
	"slices"
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
			forcedSettings, err := activeForcedProjections(statement)
			if err != nil {
				t.Fatalf("%s: %v", tt.sourceID, err)
			}
			if !slices.Contains(forcedSettings, forcedProjection{Table: tt.table, Name: tt.projection}) {
				t.Fatalf("%s latest-run lookup does not require its bounded run projection %s on %s: active forced settings %v", tt.sourceID, tt.projection, tt.table, forcedSettings)
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
// every forced name must be one devhealthschema declares. The forced names are
// the ACTIVE force_optimize_projection_name assignments of each SETTINGS clause,
// read with the SQL tokenizer of the org-scope sweep: a comment or a string that
// only mentions the setting is not one, a spaced or quoted spelling is.
func TestSourceCatalog_forced_projections_are_declared_in_devhealthschema(t *testing.T) {
	declared := map[string]bool{}
	for _, projections := range devhealthschema.Projections {
		for _, projection := range projections {
			declared[projection.Name] = true
		}
	}
	seen := map[string]bool{}
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		forcedSettings, err := activeForcedProjections(query.Statement)
		if err != nil {
			t.Errorf("%s: %v", query.ID, err)
			continue
		}
		for _, forced := range forcedSettings {
			seen[forced.Name] = true
			if !declared[forced.Name] {
				t.Errorf("%s forces projection %s, which devhealthschema.Projections does not declare", query.ID, forced.Name)
			}
		}
	}
	if !seen["prj_acr_file_hotspot_runs"] || !seen["prj_acr_file_complexity_runs"] {
		t.Fatalf("catalog forced projections = %v, want the hotspot and complexity run projections", seen)
	}
}

const forcedProjectionSetting = "force_optimize_projection_name"

// forcedProjection is one active force_optimize_projection_name assignment and
// the base table of the query block whose SETTINGS clause holds it ("" when the
// block reads no named table).
type forcedProjection struct{ Table, Name string }

// activeForcedProjections walks every SETTINGS clause of a statement, in every
// query block, subquery and CTE, and returns its active forced projections. It
// errors on a forced-projection assignment whose value is not a string or a
// quoted name, so an unreadable spelling fails the guard instead of passing it.
func activeForcedProjections(statement string) ([]forcedProjection, error) {
	toks, err := tokenizeSQL(statement)
	if err != nil {
		return nil, err
	}
	items, err := groupSQL(toks)
	if err != nil {
		return nil, err
	}
	var out []forcedProjection
	err = walkForcedProjections(items, &out)
	return out, err
}

func walkForcedProjections(items []sqlNode, out *[]forcedProjection) error {
	table, afterFrom := "", false
	for i := 0; i < len(items); i++ {
		item := items[i]
		switch {
		case item.group:
			if err := walkForcedProjections(item.children, out); err != nil {
				return err
			}
			if afterFrom {
				table, afterFrom = "", false
			}
		case item.isKw(sqlSetOperators...) || item.isKw("SELECT"):
			table, afterFrom = "", false
		case item.isKw("FROM"):
			table, afterFrom = "", true
		case afterFrom && item.isIdent():
			table, afterFrom = item.tok.text, false
			for i+2 < len(items) && items[i+1].isPunct(".") && items[i+2].isIdent() {
				table = items[i+2].tok.text
				i += 2
			}
		case item.isKw("SETTINGS"):
			j := i + 1
			for j+2 < len(items) && items[j].isIdent() && items[j+1].isPunct("=") {
				if items[j].tok.text == forcedProjectionSetting {
					value := items[j+2]
					if value.group || (value.tok.kind != sqlString && !(value.tok.kind == sqlIdent && value.tok.quoted)) {
						return fmt.Errorf("%s has a value that is not a quoted name", forcedProjectionSetting)
					}
					name := value.tok.text
					if value.tok.kind == sqlString {
						name = strings.ReplaceAll(name[1:len(name)-1], "''", "'")
					}
					*out = append(*out, forcedProjection{Table: table, Name: name})
				}
				j += 3
				if j < len(items) && items[j].isPunct(",") {
					j++
					continue
				}
				break
			}
			i = j - 1
		}
	}
	return nil
}

func TestForcedProjections_scanner_reads_active_settings_by_tokens(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []forcedProjection
	}{
		{name: "compact", sql: "SELECT 1 FROM t SETTINGS force_optimize_projection_name='p'", want: []forcedProjection{{"t", "p"}}},
		{name: "spaced", sql: "SELECT 1 FROM t SETTINGS force_optimize_projection_name = 'prj_undeclared'", want: []forcedProjection{{"t", "prj_undeclared"}}},
		{name: "double quoted value", sql: `SELECT 1 FROM t SETTINGS force_optimize_projection_name="prj_undeclared"`, want: []forcedProjection{{"t", "prj_undeclared"}}},
		{name: "comment names a declared projection beside an active undeclared one", sql: "SELECT 1 FROM t /* force_optimize_projection_name='prj_declared' */ SETTINGS force_optimize_projection_name = 'prj_undeclared'", want: []forcedProjection{{"t", "prj_undeclared"}}},
		{name: "line comment is inactive", sql: "SELECT 1 FROM t -- SETTINGS force_optimize_projection_name='prj_undeclared'\n", want: nil},
		{name: "string literal is inactive", sql: "SELECT 'force_optimize_projection_name=''x''' FROM t", want: nil},
		{name: "after another setting", sql: "SELECT 1 FROM t SETTINGS max_threads = 4, force_optimize_projection_name = 'p'", want: []forcedProjection{{"t", "p"}}},
		{name: "subquery after another setting binds its own table", sql: "SELECT 1 FROM a WHERE x = (SELECT y FROM db.b SETTINGS max_threads = 1, force_optimize_projection_name = 'pb') SETTINGS force_optimize_projection_name = 'pa'", want: []forcedProjection{{"b", "pb"}, {"a", "pa"}}},
		{name: "CTE body", sql: "WITH c AS (SELECT 1 FROM t SETTINGS force_optimize_projection_name = 'p') SELECT * FROM c", want: []forcedProjection{{"t", "p"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := activeForcedProjections(tt.sql)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("active forced projections = %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := activeForcedProjections("SELECT 1 FROM t SETTINGS force_optimize_projection_name = 5"); err == nil {
		t.Fatal("an unreadable forced-projection value must be an error")
	}
}
