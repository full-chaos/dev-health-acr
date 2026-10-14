package devhealthschema

import "testing"

// A declared projection must be told apart from a live one that differs in
// its select list or its group by, or the freshness check against the ops
// schema could not fail.
func TestProjectionMatchesLiveSeesADifferentSelectAndGroupBy(t *testing.T) {
	declared := Projections["file_hotspot_daily"][0]
	if !ProjectionMatchesLive(declared, "SELECT org_id,  repo_id, day, computed_at\nGROUP BY org_id, repo_id, day, computed_at") {
		t.Fatal("the declared body, differently spaced, must match")
	}
	for name, live := range map[string]string{
		"select drops a column":   "SELECT org_id, repo_id, day GROUP BY org_id, repo_id, day, computed_at",
		"group by drops a column": "SELECT org_id, repo_id, day, computed_at GROUP BY org_id, repo_id, day",
		"other body":              "SELECT org_id, repo_id, max(day) GROUP BY org_id, repo_id",
	} {
		if ProjectionMatchesLive(declared, live) {
			t.Errorf("%s: a differing live projection matched the declaration", name)
		}
	}
}

func TestProjectionDDLRendersTheSettingThenEachProjection(t *testing.T) {
	statements := ProjectionDDL("file_hotspot_daily")
	if len(statements) != 2 || statements[1] != "ALTER TABLE file_hotspot_daily ADD PROJECTION IF NOT EXISTS prj_acr_file_hotspot_runs (SELECT org_id, repo_id, day, computed_at GROUP BY org_id, repo_id, day, computed_at)" {
		t.Fatalf("statements = %q", statements)
	}
	if ProjectionDDL("repos") != nil {
		t.Fatal("a table with no declared projection renders nothing")
	}
}
