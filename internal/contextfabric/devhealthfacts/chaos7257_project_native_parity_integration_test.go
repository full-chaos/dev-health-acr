package devhealthfacts_test

// CHAOS-7257 result parity for the project-native mix: the single-pass
// statement returns the same rows as the multi-reference statement CHAOS-7124
// added (acr 1a1ef01e, kept as the oracle in chaos7257_oracle_test.go), on one
// seeded ClickHouse, for the current axis and for ranges.
//
//	n-p1  work items A-1, A-2, M-1 (placed under two repositories)
//	n-p2  work items B-1, and T-1 (moved here from n-p1 by a transition)
//	n-p3  no work item is named by any unit                 -> no row
//	n-p4  work item C-1; NOT requested, but a unit that reaches it and n-p1 is
//	      still a spanning unit of n-p1 (the spanning count is taken before the
//	      requested projects are selected)
//
// Units cover: one issue, two issues of one project, issues of two projects
// (spanning), an issue of an unrequested project, a multi-placed item, a
// duplicated issue ref, no refs, an unplaced ref, zero and negative effort, an
// empty theme map, a superseded unit, a unit outside the current membership
// run, a recomputed unit, a moved work item, and a unit outside the range.

import (
	"context"
	"fmt"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

type nativeRow struct {
	Key         string
	Themes      [5]float64
	Bugfix      float64
	WorkUnits   uint64
	EffortUnits uint64
	Spanning    uint64
	MultiPlaced uint64
}

func runNativeStatement(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, name, statement, orgID string, ids []string, w devhealthfacts.ProjectMixWindow) []nativeRow {
	t.Helper()
	extra := append(w.Bindings(), readers.Binding{Name: "bugfix_key", Value: readers.BugfixSubcategoryKey})
	var out []nativeRow
	err := readers.QueryOrgScopedNamed(ctx, query, name, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r nativeRow
		if err := row.Scan(&r.Key, &r.Themes[0], &r.Themes[1], &r.Themes[2], &r.Themes[3], &r.Themes[4], &r.Bugfix, &r.WorkUnits, &r.EffortUnits, &r.Spanning, &r.MultiPlaced); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	}, extra...)
	if err != nil {
		t.Fatalf("%s: %v\nstatement:\n%s", name, err, statement)
	}
	return out
}

func nativeRowsDiffer(want, got []nativeRow) error {
	toRollup := func(rows []nativeRow) []rollupRow {
		out := make([]rollupRow, len(rows))
		for i, r := range rows {
			// WorkUnits/EffortUnits/Spanning/MultiPlaced ride the roll-up
			// comparator's exact-count fields: (WorkUnits, Repos, Teams, Excluded).
			out[i] = rollupRow{Key: r.Key, Themes: r.Themes, Bugfix: r.Bugfix, WorkUnits: r.WorkUnits, Repos: r.EffortUnits, Teams: r.Spanning, Excluded: r.MultiPlaced}
		}
		return out
	}
	return rollupRowsDiffer(toRollup(want), toRollup(got))
}

func seedCHAOS7257Native(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) time.Time {
	t.Helper()
	at := ts(2026, 9, 18, 0, 0, 0)
	day := 24 * time.Hour
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	for _, id := range []string{"n-p1", "n-p2", "n-p3", "n-p4"} {
		exec("project "+id, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, "linear", nil, "Project "+id, uint8(1), "active", "", at)
	}
	exec("stop merges", `SYSTEM STOP MERGES work_unit_investments`)
	zero := "00000000-0000-0000-0000-000000000000"
	item := func(repoID, workItemID, projectID string) {
		exec("work item "+workItemID, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, workItemID, "linear", "title", "issue", "open", "", projectID, "", "", at, at, nil, "", "", at, orgID)
	}
	item(zero, "linear:A-1", "n-p1")
	item(zero, "linear:A-2", "n-p1")
	item(zero, "linear:B-1", "n-p2")
	item(zero, "linear:C-1", "n-p4")
	item(repoUUID("m-a"), "linear:M-1", "n-p1")
	item(repoUUID("m-b"), "linear:M-1", "n-p1")
	// T-1 sits in n-p1 by its column and was moved to n-p2 by a transition: the
	// transition history decides, so it is placed in n-p2 only.
	item(zero, "linear:T-1", "n-p1")
	exec("transition", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, nil, zero, "work_item", "linear:T-1", "linear", "n-p1", "n-p2", "", "", "", at.Add(-day), at, "ev-1")

	// A transition of ANOTHER subject kind naming the same subject id: the
	// presence read is for work items only, so A-1 must not gain this placement.
	exec("other-kind transition", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, nil, zero, "pull_request", "linear:A-1", "linear", "", "n-p2", "", "", "", at.Add(-day), at, "ev-2")
	type version struct {
		computedAt time.Time
		effort     float64
		themes     map[string]float64
		from, to   time.Time
	}
	from, to := at.Add(-3*day), at.Add(-3*day)
	unit := func(id string, issues []string, versions ...version) {
		ev := `{"issues":[`
		for i, issue := range issues {
			if i > 0 {
				ev += ","
			}
			ev += fmt.Sprintf("%q", issue)
		}
		ev += `],"prs":[]}`
		for _, v := range versions {
			f, tt := v.from, v.to
			if f.IsZero() {
				f, tt = from, to
			}
			exec("unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
				id, f, tt, v.effort, v.themes, map[string]float64{readers.BugfixSubcategoryKey: 0.4, "other": 0.6}, ev, v.computedAt, orgID)
		}
	}
	one := func(effort float64, themes map[string]float64) version {
		return version{computedAt: at, effort: effort, themes: themes}
	}
	th := func(kv ...any) map[string]float64 {
		out := map[string]float64{}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1].(float64)
		}
		return out
	}
	unit("nu-1", []string{"linear:A-1"}, one(10, th("feature_delivery", 1.0)))
	unit("nu-2", []string{"linear:A-1", "linear:A-2"}, one(4, th("operational", 0.6, "quality", 0.4)))
	unit("nu-3", []string{"linear:A-1", "linear:B-1"}, one(6, th("maintenance", 1.0)))
	unit("nu-4", []string{"linear:A-1", "linear:C-1"}, one(5, th("risk", 1.0)))
	unit("nu-5", []string{"linear:M-1"}, one(3, th("quality", 1.0)))
	unit("nu-6", []string{"linear:B-1", "linear:B-1"}, one(2, th("feature_delivery", 0.5, "risk", 0.5)))
	unit("nu-7", nil, one(50, th("risk", 1.0)))
	unit("nu-8", []string{"linear:X-none"}, one(50, th("risk", 1.0)))
	unit("nu-9", []string{"linear:B-1"}, one(0, th("feature_delivery", 1.0)))
	unit("nu-neg", []string{"linear:B-1"}, one(-5, th("feature_delivery", 1.0)))
	unit("nu-sup", []string{"linear:A-1"}, one(100, th("risk", 1.0)))
	unit("nu-out", []string{"linear:A-1"}, one(100, th("risk", 1.0)))
	unit("nu-empty", []string{"linear:A-2"}, one(3, th()))
	// One project reached through a single-placed item AND a multi-placed one:
	// the unit is multi-placed for n-p1 (max over its placements), counted once.
	unit("nu-mp", []string{"linear:A-1", "linear:M-1"}, one(2, th("operational", 1.0)))
	unit("nu-re", []string{"linear:B-1"},
		version{computedAt: at.Add(-2 * time.Hour), effort: 999, themes: th("risk", 1.0)},
		one(2, th("operational", 1.0)))
	unit("nu-t", []string{"linear:T-1"}, one(7, th("feature_delivery", 1.0)))
	unit("nu-old", []string{"linear:A-2"}, version{computedAt: at, effort: 9, themes: th("maintenance", 1.0), from: at.Add(-40 * day), to: at.Add(-35 * day)})

	exec("run-1", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-1", at.Add(-2*day))
	exec("run-2", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-2", at.Add(-day))
	exec("member out", `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
		orgID, "issue", "ISS-out", "nu-out", "theme", "risk", at.Add(-2*day), "run-1")
	for _, id := range []string{"nu-1", "nu-2", "nu-3", "nu-4", "nu-5", "nu-6", "nu-7", "nu-8", "nu-9", "nu-neg", "nu-sup", "nu-empty", "nu-mp", "nu-re", "nu-t", "nu-old"} {
		exec("member "+id, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
			orgID, "issue", "ISS-"+id, id, "theme", "feature_delivery", at.Add(-day), "run-2")
	}
	exec("supersession", `INSERT INTO work_unit_supersessions (org_id, superseded_work_unit_id, superseded_at) VALUES (?,?,?)`, orgID, "nu-sup", at)
	return at
}

func TestProjectNativeSinglePassMatchesTheMultiReferenceOracleAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7257-native"
	at := seedCHAOS7257Native(t, ctx, direct, orgID)
	day := 24 * time.Hour
	ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"} // n-p4 is deliberately not requested
	const rowLimit = 201

	windows := []struct {
		name     string
		window   devhealthfacts.ProjectMixWindow
		wantKeys []string
	}{
		{"current axis", devhealthfacts.ProjectMixWindow{}, []string{"linear:n-p1", "linear:n-p2"}},
		{"range over the last 10 days", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-10 * day), End: at}, []string{"linear:n-p1", "linear:n-p2"}},
		{"range over the old unit", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-45 * day), End: at.Add(-30 * day)}, []string{"linear:n-p1"}},
		{"point in time before any unit", devhealthfacts.ProjectMixWindow{Active: true, End: at.Add(-50 * day)}, nil},
	}
	for _, tc := range windows {
		t.Run(tc.name, func(t *testing.T) {
			oracle := runNativeStatement(t, ctx, query, "OracleProjectNative", devhealthfacts.OracleProjectNativeStatement(tc.window, rowLimit), orgID, ids, tc.window)
			got := runNativeStatement(t, ctx, query, "ProjectNative", devhealthfacts.ProjectNativeStatement(tc.window, rowLimit), orgID, ids, tc.window)
			keys := make([]string, 0, len(oracle))
			for _, r := range oracle {
				keys = append(keys, r.Key)
			}
			if fmt.Sprint(keys) != fmt.Sprint(tc.wantKeys) {
				t.Fatalf("oracle rows for %v, want %v: the fixture no longer reaches what this case claims (%+v)", keys, tc.wantKeys, oracle)
			}
			if err := nativeRowsDiffer(oracle, got); err != nil {
				t.Fatal(err)
			}
		})
	}

	// Pin the oracle's current-axis numbers so a fixture edit that stops
	// exercising a branch fails here instead of comparing two thin answers.
	oracle := runNativeStatement(t, ctx, query, "OracleProjectNative", devhealthfacts.OracleProjectNativeStatement(devhealthfacts.ProjectMixWindow{}, rowLimit), orgID, ids, devhealthfacts.ProjectMixWindow{})
	want := map[string]struct{ units, effortUnits, spanning, multi uint64 }{
		// nu-1,2,3,4,5,empty,old,mp (A-1/A-2/M-1); nu-2 and nu-mp count once for
		// two items; spanning: nu-3 (n-p2) and nu-4 (n-p4, unrequested);
		// multi-placed: nu-5 and nu-mp.
		"linear:n-p1": {8, 8, 2, 2},
		// nu-3, nu-6 (duplicate ref once), nu-9 (zero), nu-neg, nu-re, nu-t.
		"linear:n-p2": {6, 4, 1, 0},
	}
	for _, r := range oracle {
		w, ok := want[r.Key]
		if !ok {
			t.Fatalf("oracle served unexpected project %s", r.Key)
		}
		if r.WorkUnits != w.units || r.EffortUnits != w.effortUnits || r.Spanning != w.spanning || r.MultiPlaced != w.multi {
			t.Errorf("oracle %s = units %d effort %d spanning %d multi %d, want %+v", r.Key, r.WorkUnits, r.EffortUnits, r.Spanning, r.MultiPlaced, w)
		}
	}
}
