package devhealthfacts_test

// CHAOS-13741: a subcategory map may hold the bugfix key twice, and which entry
// a read returns is not defined. These cells keep the repeated key out of the
// parity fixtures and assert only what is defined: every unit is counted, in
// the oracle and in the phased read, also through a merge after the scope
// phase. They do not assert the bugfix share.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-go/readers"
)

func TestCHAOS13741ProjectMixDuplicateBugfixKeyKeepsEveryUnitAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-13741-dupkey"
	at := ts(2026, 9, 18, 0, 0, 0)
	day := 24 * time.Hour
	long := at.Add(-100 * day)
	from := at.Add(-3 * day)
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	exec("stop merges", `SYSTEM STOP MERGES work_unit_investments`)
	for _, id := range []string{"n-d", "proj-d"} {
		exec("project "+id, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, "linear", nil, "Project "+id, uint8(1), "active", "", at)
	}
	exec("work item", `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"00000000-0000-0000-0000-000000000000", "linear:D-1", "linear", "title", "issue", "open", "", "n-d", "", "", at, at, nil, "", "", at, orgID)
	exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-d", "team-d", at, orgID, "linear", uint8(1))
	exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-d", "proj-d", nil, "native", long, nil, at)
	exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("rd"), orgID, "acme/rd", "github", at)
	exec("repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-d", repoUUID("rd"), "acme/rd", "exact", "native", uint8(1), uint16(100), int32(0), long, nil, at)
	exec("attribution", `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "00000000-0000-0000-0000-000000000000", "ghpr:acme/rd#30", "team-d", "team-d", "linked_issue", uint8(1), "high", at)

	dupThemes := rawMapSQL("feature_delivery", 0.2, "feature_delivery", 0.3, "risk", 0.5)
	dupSub := rawMapSQL(readers.BugfixSubcategoryKey, 0.1, readers.BugfixSubcategoryKey, 0.4, "other", 0.5)
	plainSub := rawMapSQL(readers.BugfixSubcategoryKey, 0.4, "other", 0.6)
	native := `{"issues":["linear:D-1"],"prs":[]}`
	// Native project n-d: nd-dup (repeated bugfix key) and nd-1, both through D-1.
	insertRawUnit(t, ctx, direct, orgID, "nd-dup", "", 5, dupThemes, dupSub, native, from, from, at)
	insertRawUnit(t, ctx, direct, orgID, "nd-1", "", 5, rawMapSQL("feature_delivery", 1.0), plainSub, native, from, from, at)
	// Roll-up project proj-d: wu-dup (repo rd) and wu-dupnull (no repo; reached
	// by its evidence vote for team-d), both with the repeated bugfix key, and
	// wu-1.
	insertRawUnit(t, ctx, direct, orgID, "wu-dup", "rd", 10, dupThemes, dupSub, `{"issues":[],"prs":[]}`, from, from, at)
	insertRawUnit(t, ctx, direct, orgID, "wu-dupnull", "", 5, dupThemes, dupSub, fmt.Sprintf(`{"issues":[],"prs":["%s#pr30"]}`, repoUUID("rd")), from, from, at)
	insertRawUnit(t, ctx, direct, orgID, "wu-1", "rd", 10, rawMapSQL("feature_delivery", 1.0), plainSub, `{"issues":[],"prs":[]}`, from, from, at)
	exec("run", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-d", at.Add(-day))
	for _, id := range []string{"nd-dup", "nd-1", "wu-dup", "wu-dupnull", "wu-1"} {
		exec("member "+id, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
			orgID, "issue", "ISS-"+id, id, "theme", "feature_delivery", at.Add(-day), "run-d")
	}

	// The fixture still holds the repeated key: two entries, one key.
	var entries, keys uint64
	if err := direct.QueryRow(ctx, `SELECT length(subcategory_distribution_json), length(arrayDistinct(mapKeys(subcategory_distribution_json))) FROM work_unit_investments WHERE org_id = ? AND work_unit_id = 'nd-dup'`, orgID).Scan(&entries, &keys); err != nil {
		t.Fatal(err)
	}
	if entries != 3 || keys != 2 {
		t.Fatalf("nd-dup stores %d entries over %d keys, want 3 over 2: the fixture no longer repeats the bugfix key", entries, keys)
	}

	nativeIDs, rollupIDs := []string{"linear:n-d"}, []string{"linear:proj-d"}
	const rowLimit = 201
	nativeUnits := func(t *testing.T, side string, rows []nativeRow) {
		t.Helper()
		if len(rows) != 1 || rows[0].Key != "linear:n-d" || rows[0].WorkUnits != 2 {
			t.Errorf("%s: %+v, want one linear:n-d row of WorkUnits 2 (nd-dup, nd-1: no unit lost)", side, rows)
		}
	}
	// A vote-reached unit is counted in Excluded (no repository link), not in
	// WorkUnits.
	rollupUnits := func(t *testing.T, side string, rows []rollupRow) {
		t.Helper()
		if len(rows) != 1 || rows[0].Key != "linear:proj-d" || rows[0].WorkUnits != 2 || rows[0].Excluded != 1 {
			t.Errorf("%s: %+v, want one linear:proj-d row of WorkUnits 2 (wu-dup, wu-1) and Excluded 1 (wu-dupnull): no unit lost", side, rows)
		}
	}
	for _, tc := range []struct {
		name   string
		window devhealthfacts.ProjectMixWindow
	}{
		{"current axis", devhealthfacts.ProjectMixWindow{}},
		{"range over the last 10 days", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-10 * day), End: at}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeUnits(t, "native oracle", runNativeStatement(t, ctx, query, "OracleProjectNative", devhealthfacts.OracleProjectNativeStatement(tc.window, rowLimit), orgID, nativeIDs, tc.window))
			nativeUnits(t, "native phased", runNativePhased(t, ctx, query, orgID, nativeIDs, tc.window, rowLimit))
			rollupUnits(t, "roll-up oracle", runRollupStatement(t, ctx, query, "OracleProjectRollup", devhealthfacts.OracleProjectRollupStatement(tc.window), orgID, rollupIDs, tc.window))
			rollupUnits(t, "roll-up phased", runRollupPhased(t, ctx, query, orgID, rollupIDs, tc.window))
		})
	}

	// Last: a merge between the scope phase and the later phases (the hook of
	// the pinned-version cells) must not drop a unit with the repeated key.
	t.Run("through a merge after the scope phase", func(t *testing.T) {
		var once sync.Once
		merged := devhealthfacts.WithProjectMixAfterScope(ctx, func() {
			once.Do(func() {
				if err := direct.Exec(ctx, `SYSTEM START MERGES work_unit_investments`); err != nil {
					t.Fatalf("start merges: %v", err)
				}
				if err := direct.Exec(ctx, `OPTIMIZE TABLE work_unit_investments FINAL`); err != nil {
					t.Fatalf("merge: %v", err)
				}
			})
		})
		window := devhealthfacts.ProjectMixWindow{}
		nativeUnits(t, "native phased", runNativePhased(t, merged, query, orgID, nativeIDs, window, rowLimit))
		rollupUnits(t, "roll-up phased", runRollupPhased(t, merged, query, orgID, rollupIDs, window))
	})
}
