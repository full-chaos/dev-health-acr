package devhealthfacts_test

// CHAOS-7257 result parity: the single-pass owning-team roll-up statement
// returns the same rows as the multi-reference statement it replaced.
//
// Both statement texts run on ONE seeded ClickHouse. The old text lives in
// chaos7257_oracle_test.go as the oracle (acr 1a1ef01e, the statement that
// scanned work_unit_investments three times); the new text is what
// readProjectThemeMix runs now. The fixture is small and dense on purpose: every
// branch of the statement has a unit that takes it.
//
//	proj-1  owned by team-1                 (repos r1a, r1b, r-shared)
//	proj-2  owned by team-2                 (repos r2a, r-shared)
//	proj-3  owned by team-1 AND team-2      (a project with two owning teams)
//	proj-4  owned by team-4, whose repo has no work        -> no row
//	proj-5  owned by team-5, which owns no repository       -> no row, only votes
//	proj-6  owned by team-6 until yesterday                 -> a row only as of an earlier instant
//	r7      owned by team-1 until two days ago (team-1 itself is a current owner)
//	r-shared is owned by team-1 AND team-2: one unit, three projects
//
// Work units cover: a recomputed unit (two versions), a superseded unit, a
// unit outside the current membership run, a unit whose latest repo_id is NULL
// while an older row named a repository, an empty theme map, zero effort, units
// with windows before / inside / across a requested range, and for the evidence
// vote: a clean vote, a tie (the greater team id wins), a majority (2 vs 1),
// no attribution, no refs, a gitlab ref, a plain issue ref, two primary
// attributions for one work item, a newer attribution replacing an older one,
// a non-primary attribution, and a ref naming a repository that does not exist.

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

type rollupRow struct {
	Key       string
	Themes    [5]float64 // feature_delivery, operational, maintenance, quality, risk
	Bugfix    float64
	WorkUnits uint64
	Repos     uint64
	Teams     uint64
	Excluded  uint64
}

func runRollupStatement(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, name, statement, orgID string, ids []string, w devhealthfacts.ProjectMixWindow) []rollupRow {
	t.Helper()
	var out []rollupRow
	err := readers.QueryOrgScopedNamed(ctx, query, name, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r rollupRow
		if err := row.Scan(&r.Key, &r.Themes[0], &r.Themes[1], &r.Themes[2], &r.Themes[3], &r.Themes[4], &r.Bugfix, &r.WorkUnits, &r.Repos, &r.Teams, &r.Excluded); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	}, w.Bindings()...)
	if err != nil {
		t.Fatalf("%s: %v\nstatement:\n%s", name, err, statement)
	}
	return out
}

// rollupRowsDiffer reports the first difference between two row sets (nil when
// identical). Counts compare exactly; floats compare to a relative 1e-9, since a
// sum over the same units in a different order is not bit-identical.
func rollupRowsDiffer(want, got []rollupRow) error {
	if len(want) != len(got) {
		return fmt.Errorf("row count: oracle %d, new %d\noracle %+v\nnew    %+v", len(want), len(got), want, got)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b))) }
	for i := range want {
		w, g := want[i], got[i]
		if w.Key != g.Key || w.WorkUnits != g.WorkUnits || w.Repos != g.Repos || w.Teams != g.Teams || w.Excluded != g.Excluded || !near(w.Bugfix, g.Bugfix) {
			return fmt.Errorf("row %d: oracle %+v, new %+v", i, w, g)
		}
		for k := range w.Themes {
			if !near(w.Themes[k], g.Themes[k]) {
				return fmt.Errorf("row %d (%s): theme %d oracle %v, new %v", i, w.Key, k, w.Themes[k], g.Themes[k])
			}
		}
	}
	return nil
}

func seedCHAOS7257Parity(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) time.Time {
	t.Helper()
	at := ts(2026, 9, 18, 0, 0, 0)
	day := 24 * time.Hour
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	project := func(id string) {
		exec("project "+id, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, "linear", nil, "Project "+id, uint8(1), "active", "", at)
	}
	team := func(id string) {
		exec("team "+id, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			id, id, at, orgID, "linear", uint8(1))
	}
	ownsProject := func(teamID, projectID string, from time.Time, to any) {
		exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, projectID, nil, "native", from, to, at)
	}
	repo := func(label, provider string) {
		exec("repo "+label, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, provider, at)
	}
	ownsRepo := func(teamID, label string, from time.Time, to any) {
		exec("repo ownership "+label, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), from, to, at)
	}
	// Append-only history: the recomputed units keep BOTH versions readable, so
	// argMax (not a background merge that would keep only the newest row) is what
	// picks the latest.
	exec("stop merges", `SYSTEM STOP MERGES work_unit_investments`)
	long := at.Add(-100 * day)
	for _, id := range []string{"proj-1", "proj-2", "proj-3", "proj-4", "proj-5", "proj-6"} {
		project(id)
	}
	for _, id := range []string{"team-1", "team-2", "team-4", "team-5", "team-6"} {
		team(id)
	}
	ownsProject("team-1", "proj-1", long, nil)
	ownsProject("team-2", "proj-2", long, nil)
	ownsProject("team-1", "proj-3", long, nil)
	ownsProject("team-2", "proj-3", long, nil)
	ownsProject("team-4", "proj-4", long, nil)
	ownsProject("team-5", "proj-5", long, nil)
	ownsProject("team-6", "proj-6", long, at.Add(-day))
	for _, label := range []string{"r1a", "r1b", "r2a", "r-shared", "r4", "r6"} {
		repo(label, "github")
	}
	repo("gl", "gitlab")
	ownsRepo("team-1", "r1a", long, nil)
	ownsRepo("team-1", "r1b", long, nil)
	ownsRepo("team-2", "r2a", long, nil)
	ownsRepo("team-1", "r-shared", long, nil)
	ownsRepo("team-2", "r-shared", long, nil)
	ownsRepo("team-4", "r4", long, nil)
	ownsRepo("team-6", "r6", long, at.Add(-day))
	repo("r7", "github")
	ownsRepo("team-1", "r7", long, at.Add(-2*day)) // the owner is current, this repository's ownership is not

	themes := func(kv ...any) map[string]float64 {
		out := map[string]float64{}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1].(float64)
		}
		return out
	}
	evidence := func(issues []string, prs ...string) string {
		q := func(items []string) string {
			out := ""
			for i, item := range items {
				if i > 0 {
					out += ","
				}
				out += fmt.Sprintf("%q", item)
			}
			return out
		}
		return fmt.Sprintf(`{"issues":[%s],"prs":[%s]}`, q(issues), q(prs))
	}
	pr := func(label string, n int) string { return fmt.Sprintf("%s#pr%d", repoUUID(label), n) }
	from, to := at.Add(-3*day), at.Add(-3*day)
	type version struct {
		computedAt time.Time
		repo       string // "" = NULL
		effort     float64
		themes     map[string]float64
		from, to   time.Time
		evidence   string
	}
	unit := func(id string, versions ...version) {
		for _, v := range versions {
			var repoID any
			if v.repo != "" {
				repoID = repoUUID(v.repo)
			}
			f, tt := v.from, v.to
			if f.IsZero() {
				f, tt = from, to
			}
			ev := v.evidence
			if ev == "" {
				ev = `{"issues":[],"prs":[]}`
			}
			exec("unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				id, f, tt, repoID, v.effort, v.themes, map[string]float64{readers.BugfixSubcategoryKey: 0.5, "other": 0.5}, ev, v.computedAt, orgID)
		}
	}
	fd, op, mt, ql, rk := "feature_delivery", "operational", "maintenance", "quality", "risk"
	// Repository-keyed units.
	unit("wu-1", version{computedAt: at, repo: "r1a", effort: 10, themes: themes(fd, 1.0)})
	unit("wu-2",
		version{computedAt: at.Add(-2 * time.Hour), repo: "r1a", effort: 999, themes: themes(rk, 1.0)},
		version{computedAt: at, repo: "r1a", effort: 4, themes: themes(op, 0.6, ql, 0.4)})
	unit("wu-3", version{computedAt: at, repo: "r1b", effort: 6, themes: themes(mt, 1.0)})
	unit("wu-4", version{computedAt: at, repo: "r2a", effort: 8, themes: themes(ql, 0.5, rk, 0.5)})
	unit("wu-5", version{computedAt: at, repo: "r-shared", effort: 5, themes: themes(op, 1.0)})
	unit("wu-sup", version{computedAt: at, repo: "r1a", effort: 100, themes: themes(rk, 1.0)})
	unit("wu-out", version{computedAt: at, repo: "r1a", effort: 100, themes: themes(rk, 1.0)})
	unit("wu-empty", version{computedAt: at, repo: "r1b", effort: 3, themes: themes()})
	unit("wu-zero", version{computedAt: at, repo: "r2a", effort: 0, themes: themes(fd, 1.0)})
	unit("wu-old", version{computedAt: at, repo: "r1a", effort: 9, themes: themes(mt, 1.0), from: at.Add(-40 * day), to: at.Add(-35 * day)})
	unit("wu-span", version{computedAt: at, repo: "r1b", effort: 2, themes: themes(ql, 1.0), from: at.Add(-12 * day), to: at.Add(-9 * day)})
	unit("wu-r7", version{computedAt: at, repo: "r7", effort: 11, themes: themes(rk, 1.0)})
	unit("wu-r6", version{computedAt: at, repo: "r6", effort: 7, themes: themes(fd, 1.0), from: at.Add(-60 * day), to: at.Add(-55 * day)})
	// A unit whose LATEST row has no repository although an older row named one:
	// it must not fall back to the older repository, and it is reached only by
	// its evidence (issue linear:X-9, attributed to team-2).
	unit("wu-moved",
		version{computedAt: at.Add(-2 * time.Hour), repo: "r1a", effort: 50, themes: themes(rk, 1.0)},
		version{computedAt: at, repo: "", effort: 50, themes: themes(rk, 1.0), evidence: evidence([]string{"linear:X-9"})})
	// Evidence-vote units (no repo_id).
	unit("wu-n1", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence(nil, pr("r1a", 7))})
	unit("wu-n2", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence(nil, pr("r1a", 8), pr("r2a", 9))})
	unit("wu-n3", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence(nil, pr("r1a", 77))})
	unit("wu-n4", version{computedAt: at, effort: 5, themes: themes(fd, 1.0)})
	unit("wu-n5", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence(nil, pr("gl", 3))})
	unit("wu-n6", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence([]string{"linear:X-1"})})
	unit("wu-n7", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence([]string{"linear:X-2"})})
	unit("wu-n8", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence([]string{"linear:X-3"}, pr("nowhere", 1))})
	unit("wu-n9", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence([]string{"linear:X-4"})})
	unit("wu-n10", version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence(nil, pr("r1a", 20), pr("r1a", 21), pr("r2a", 22))})

	// Three-way vote ties (team-1, team-2, team-4 one attributed ref each): the
	// greatest team id, team-4, wins every one. Six units, so a tie broken by
	// arrival order instead of by team id cannot pass by luck.
	for k := 0; k < 6; k++ {
		unit(fmt.Sprintf("wu-t3-%d", k), version{computedAt: at, effort: 5, themes: themes(fd, 1.0), evidence: evidence([]string{fmt.Sprintf("linear:T3-%d", k)})})
	}
	attribute := func(workItem, teamID string, primary uint8, computedAt time.Time) {
		exec("attribution "+workItem, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "00000000-0000-0000-0000-000000000000", workItem, teamID, teamID, "linked_issue", primary, "high", computedAt)
	}
	attribute("ghpr:acme/r1a#7", "team-1", 1, at)
	attribute("ghpr:acme/r1a#8", "team-1", 1, at)
	attribute("ghpr:acme/r2a#9", "team-2", 1, at)
	attribute("gitlab:acme/gl!3", "team-5", 1, at)
	attribute("linear:X-1", "team-2", 1, at)
	attribute("linear:X-2", "team-1", 1, at)
	attribute("linear:X-2", "team-2", 1, at)
	attribute("linear:X-3", "team-1", 1, at.Add(-5*day))
	attribute("linear:X-3", "team-2", 1, at)
	attribute("linear:X-4", "team-1", 0, at)
	attribute("linear:X-9", "team-2", 1, at)
	attribute("ghpr:acme/r1a#20", "team-1", 1, at)
	attribute("ghpr:acme/r1a#21", "team-1", 1, at)
	attribute("ghpr:acme/r2a#22", "team-2", 1, at)
	// An attribution row whose work item id is empty: a unit with no evidence
	// refs must never vote through it (it resolves to the empty id).
	attribute("", "team-1", 1, at)
	for k := 0; k < 6; k++ {
		for _, teamID := range []string{"team-1", "team-2", "team-4"} {
			attribute(fmt.Sprintf("linear:T3-%d", k), teamID, 1, at)
		}
	}

	// Membership: run-2 is the latest complete run and lists every unit except
	// wu-out; run-1 (older) lists wu-out.
	exec("run-1", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-1", at.Add(-2*day))
	exec("run-2", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-2", at.Add(-day))
	exec("member out", `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
		orgID, "issue", "ISS-out", "wu-out", "theme", "risk", at.Add(-2*day), "run-1")
	for _, id := range []string{"wu-1", "wu-2", "wu-3", "wu-4", "wu-5", "wu-sup", "wu-empty", "wu-zero", "wu-old", "wu-span", "wu-r6", "wu-moved",
		"wu-n1", "wu-n2", "wu-n3", "wu-n4", "wu-n5", "wu-n6", "wu-n7", "wu-n8", "wu-n9", "wu-n10", "wu-r7",
		"wu-t3-0", "wu-t3-1", "wu-t3-2", "wu-t3-3", "wu-t3-4", "wu-t3-5"} {
		exec("member "+id, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
			orgID, "issue", "ISS-"+id, id, "theme", "feature_delivery", at.Add(-day), "run-2")
	}
	exec("supersession", `INSERT INTO work_unit_supersessions (org_id, superseded_work_unit_id, superseded_at) VALUES (?,?,?)`, orgID, "wu-sup", at)
	return at
}

func TestProjectRollupSinglePassMatchesTheMultiReferenceOracleAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7257-parity"
	at := seedCHAOS7257Parity(t, ctx, direct, orgID)
	day := 24 * time.Hour
	ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3", "linear:proj-4", "linear:proj-5", "linear:proj-6"}

	windows := []struct {
		name     string
		window   devhealthfacts.ProjectMixWindow
		wantKeys []string // oracle's rows: proves the fixture reaches each project it claims to
	}{
		{"current axis", devhealthfacts.ProjectMixWindow{}, []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}},
		{"range over the last 10 days", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-10 * day), End: at}, []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}},
		{"point in time before proj-6's ownership ended", devhealthfacts.ProjectMixWindow{Active: true, End: at.Add(-50 * day)}, []string{"linear:proj-6"}},
		{"range around the old work", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-70 * day), End: at.Add(-50 * day)}, []string{"linear:proj-6"}},
		{"range over the old unit", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: at.Add(-45 * day), End: at.Add(-30 * day)}, []string{"linear:proj-1", "linear:proj-3"}},
	}
	for _, tc := range windows {
		t.Run(tc.name, func(t *testing.T) {
			oracle := runRollupStatement(t, ctx, query, "OracleProjectRollup", devhealthfacts.OracleProjectRollupStatement(tc.window), orgID, ids, tc.window)
			got := runRollupStatement(t, ctx, query, "ProjectRollup", devhealthfacts.ProjectRollupStatement(tc.window), orgID, ids, tc.window)
			keys := make([]string, 0, len(oracle))
			for _, r := range oracle {
				keys = append(keys, r.Key)
			}
			if fmt.Sprint(keys) != fmt.Sprint(tc.wantKeys) {
				t.Fatalf("oracle rows for %v, want %v: the fixture no longer reaches what this case claims (%+v)", keys, tc.wantKeys, oracle)
			}
			if err := rollupRowsDiffer(oracle, got); err != nil {
				t.Fatal(err)
			}
		})
	}

	// The current axis is where every branch is live: pin the oracle's numbers
	// so a fixture edit that quietly stops exercising a branch fails here rather
	// than turning the parity check into a comparison of two empty answers.
	oracle := runRollupStatement(t, ctx, query, "OracleProjectRollup", devhealthfacts.OracleProjectRollupStatement(devhealthfacts.ProjectMixWindow{}), orgID, ids, devhealthfacts.ProjectMixWindow{})
	want := map[string]struct{ units, repos, teams, excluded uint64 }{
		"linear:proj-1": {7, 3, 1, 2}, // wu-1,2,3,empty,5,old,span; votes: n1, n10 (team-1)
		"linear:proj-2": {3, 2, 1, 5}, // wu-4,zero,5;               votes: n2, n6, n7, n8, moved (team-2 wins the ties)
		"linear:proj-3": {9, 4, 2, 7}, // both teams' units, r-shared once; every team-1/team-2 vote
	}
	for _, r := range oracle {
		w, ok := want[r.Key]
		if !ok {
			t.Fatalf("oracle served unexpected project %s", r.Key)
		}
		if r.WorkUnits != w.units || r.Repos != w.repos || r.Teams != w.teams || r.Excluded != w.excluded {
			t.Errorf("oracle %s = units %d repos %d teams %d excluded %d, want %+v", r.Key, r.WorkUnits, r.Repos, r.Teams, r.Excluded, w)
		}
	}
}
