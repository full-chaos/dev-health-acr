package devhealthfacts_test

// A team read serves investment for every owned-repository provider alike.
// The Demo shape: one team per provider (gitlab, github, jira-tracked,
// linear-tracked), each owning one repository with persisted units, read
// under different membership-run layouts. EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type matrixUnit struct {
	id, team, evidence string
}

func effortOf(u matrixUnit) float64 {
	if strings.HasSuffix(u.id, "-zeroeff") {
		return 0
	}
	return 1.0
}

func TestTeamInvestmentServesEveryProviderAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	day := 24 * time.Hour
	end := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	synced := end.Add(-30 * day)
	from, to := end.Add(-20*day), end.Add(-10*day)

	type team struct{ id, repoLabel, repoName, provider string }
	teams := []team{
		{"team-gitlab", "repo-gl", "chaos-gl/data/pipeline", "gitlab"},
		{"team-github", "repo-gh", "acme/payments", "github"},
		{"team-jira", "repo-jira", "acme/support", "github"},
		{"team-linear", "repo-linear", "acme/checkout", "github"},
	}
	unitsFor := func(prefix string) []matrixUnit {
		return []matrixUnit{
			{prefix + "gl-own", "team-gitlab", `{"issues":["gitlab:chaos-gl/data/pipeline#1"],"prs":[]}`},
			{prefix + "gl-mr", "team-gitlab", `{"issues":["gitlab:chaos-gl/data/pipeline!2"],"prs":[]}`},
			{prefix + "gh-own", "team-github", `{"issues":["gh:acme/payments#1"],"prs":[]}`},
			{prefix + "gh-pr", "team-github", `{"issues":["ghpr:acme/payments#2"],"prs":[]}`},
			{prefix + "jira-own", "team-jira", `{"issues":["jira:SUP-1"],"prs":[]}`},
			{prefix + "jira-pr", "team-jira", fmt.Sprintf(`{"issues":["jira:SUP-2"],"prs":["%s#pr3"]}`, repoUUID("repo-jira"))},
			{prefix + "lin-own", "team-linear", `{"issues":["linear:CHK-1"],"prs":[]}`},
			{prefix + "lin-pr", "team-linear", fmt.Sprintf(`{"issues":["linear:CHK-2"],"prs":["%s#pr4"]}`, repoUUID("repo-linear"))},
		}
	}
	repoOf := map[string]string{}
	for _, tm := range teams {
		repoOf[tm.id] = tm.repoLabel
	}

	type scenario struct {
		name  string
		org   string
		units []matrixUnit
		// runs lists membership runs oldest first; each holds the unit ids it contains.
		runs [][]string
		// extraEvidence replaces one unit's evidence (an unresolved reference).
		want map[string]float64
		// token is the closed empty-cause token a team with no mix must name.
		token map[string]string
	}
	all := unitsFor("")
	ids := func(units []matrixUnit, keep func(matrixUnit) bool) []string {
		var out []string
		for _, u := range units {
			if keep == nil || keep(u) {
				out = append(out, u.id)
			}
		}
		return out
	}
	each := map[string]float64{"team-gitlab": 2, "team-github": 2, "team-jira": 2, "team-linear": 2}
	scenarios := []scenario{
		{name: "one complete run holds every unit", org: "org-matrix-one-run", units: all, runs: [][]string{ids(all, nil)}, want: each},
		{name: "no run recorded", org: "org-matrix-no-run", units: all, runs: nil, want: each},
		{
			name: "latest run holds only the non-gitlab units", org: "org-matrix-split-run", units: all,
			runs:  [][]string{ids(all, nil), ids(all, func(u matrixUnit) bool { return u.team != "team-gitlab" })},
			want:  map[string]float64{"team-gitlab": 0, "team-github": 2, "team-jira": 2, "team-linear": 2},
			token: map[string]string{"team-gitlab": "investment_team_units_outside_membership_run"},
		},
		{
			name: "latest run holds only the github units", org: "org-matrix-github-run", units: all,
			runs:  [][]string{ids(all, nil), ids(all, func(u matrixUnit) bool { return u.team == "team-github" })},
			want:  map[string]float64{"team-gitlab": 0, "team-github": 2, "team-jira": 0, "team-linear": 0},
			token: map[string]string{"team-gitlab": "investment_team_units_outside_membership_run", "team-jira": "investment_team_units_outside_membership_run", "team-linear": "investment_team_units_outside_membership_run"},
		},
	}
	unresolved := []matrixUnit{
		{"unres-gl", "team-gitlab", `{"issues":["gitlab:vendor/unknown!9"],"prs":[]}`},
		{"unres-gh", "team-github", `{"issues":["ghpr:vendor/unknown#9"],"prs":[]}`},
		{"unres-jira", "team-jira", `{"issues":["jira:SUP-9"],"prs":["00000000-0000-0000-0000-000000000009#pr9"]}`},
		{"unres-lin", "team-linear", `{"issues":["linear:CHK-9"],"prs":["00000000-0000-0000-0000-000000000009#pr9"]}`},
	}
	both := []matrixUnit{
		{"both-out", "team-gitlab", `{"issues":["gitlab:chaos-gl/data/pipeline#1"],"prs":[]}`},
		{"both-unres", "team-gitlab", `{"issues":["gitlab:vendor/unknown!9"],"prs":[]}`},
		{"both-gh", "team-github", `{"issues":["gh:acme/payments#1"],"prs":[]}`},
		{"both-jira", "team-jira", `{"issues":["jira:SUP-1"],"prs":[]}`},
		{"both-lin", "team-linear", `{"issues":["linear:CHK-1"],"prs":[]}`},
	}
	zeroOut := []matrixUnit{
		{"out-zeroeff", "team-gitlab", `{"issues":["gitlab:chaos-gl/data/pipeline#1"],"prs":[]}`},
		{"zero-gh", "team-github", `{"issues":["gh:acme/payments#1"],"prs":[]}`},
		{"zero-jira", "team-jira", `{"issues":["jira:SUP-1"],"prs":[]}`},
		{"zero-lin", "team-linear", `{"issues":["linear:CHK-1"],"prs":[]}`},
	}
	mixed := []matrixUnit{
		{"mix-zeroeff", "team-gitlab", `{"issues":["gitlab:chaos-gl/data/pipeline#1"],"prs":[]}`},
		{"mix-unres", "team-gitlab", `{"issues":["gitlab:vendor/unknown!9"],"prs":[]}`},
		{"mix-gh", "team-github", `{"issues":["gh:acme/payments#1"],"prs":[]}`},
		{"mix-jira", "team-jira", `{"issues":["jira:SUP-1"],"prs":[]}`},
		{"mix-lin", "team-linear", `{"issues":["linear:CHK-1"],"prs":[]}`},
	}
	scenarios = append(scenarios, scenario{
		name: "in-scope zero-effort unit plus a dropped positive-effort unit names the unattributed cause", org: "org-matrix-zero-and-dropped", units: mixed,
		runs:  [][]string{ids(mixed, nil)},
		want:  map[string]float64{"team-gitlab": 0, "team-github": 1, "team-jira": 1, "team-linear": 1},
		token: map[string]string{"team-gitlab": "investment_team_units_unattributed"},
	})
	scenarios = append(scenarios, scenario{
		name: "all-zero-effort units outside the run name the run, not the effort", org: "org-matrix-zero-out", units: zeroOut,
		runs:  [][]string{{"zero-gh", "zero-jira", "zero-lin"}},
		want:  map[string]float64{"team-gitlab": 0, "team-github": 1, "team-jira": 1, "team-linear": 1},
		token: map[string]string{"team-gitlab": "investment_team_units_outside_membership_run"},
	})
	scenarios = append(scenarios, scenario{
		name: "a team with a unit outside the run and an unresolved reference names the run first", org: "org-matrix-both", units: both,
		runs:  [][]string{{"both-unres", "both-gh", "both-jira", "both-lin"}},
		want:  map[string]float64{"team-gitlab": 0, "team-github": 1, "team-jira": 1, "team-linear": 1},
		token: map[string]string{"team-gitlab": "investment_team_units_outside_membership_run"},
	})
	scenarios = append(scenarios, scenario{
		name: "one reference resolves to no repository", org: "org-matrix-unresolved", units: unresolved, runs: [][]string{ids(unresolved, nil)}, want: map[string]float64{"team-gitlab": 0, "team-github": 0, "team-jira": 0, "team-linear": 0},
		token: map[string]string{"team-gitlab": "investment_team_units_unattributed", "team-github": "investment_team_units_unattributed", "team-jira": "investment_team_units_unattributed", "team-linear": "investment_team_units_unattributed"},
	})
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			exec := func(statement string, args ...any) {
				t.Helper()
				if err := direct.Exec(ctx, statement, args...); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			for _, tm := range teams {
				exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(tm.repoLabel+sc.org), sc.org, tm.repoName, tm.provider, synced)
				exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					sc.org, tm.provider, tm.id, repoUUID(tm.repoLabel+sc.org), tm.repoName, "exact", "native", uint8(1), uint16(100), int32(0), synced, nil, synced)
			}
			for _, u := range sc.units {
				evidence := u.evidence
				for _, tm := range teams {
					evidence = strings.ReplaceAll(evidence, repoUUID(tm.repoLabel), repoUUID(tm.repoLabel+sc.org))
				}
				exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
					u.id, from, to, repoUUID(repoOf[u.team]+sc.org), effortOf(u), map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, evidence, to, sc.org)
			}
			for i, run := range sc.runs {
				runID := fmt.Sprintf("run-%d", i+1)
				completed := end.Add(-time.Duration(len(sc.runs)-i) * day)
				for _, id := range run {
					exec(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
						sc.org, "issue", id, id, "theme", "feature_delivery", completed, runID)
				}
				exec(`INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, sc.org, runID, completed)
			}
			for _, tm := range teams {
				start, stop := end.Add(-60*day), end
				result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: sc.org}, contextfabric.FactQuery{
					Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &stop},
					Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject(tm.id)},
				})
				if err != nil {
					t.Fatalf("%s: ReadFacts: %v", tm.id, err)
				}
				got := 0.0
				if len(result.Facts) == 1 {
					for _, row := range result.Facts[0].Fields["theme_breakdown"].Table.Rows {
						got += *row.Fields["weighted_effort"].Number
					}
				}
				t.Logf("MATRIX scenario=%q team=%s provider=%s facts=%d effort=%v reason=%q", sc.name, tm.id, tm.provider, len(result.Facts), got, result.Reason)
				repoResult, repoErr := provider.ReadFacts(ctx, storage.Principal{OrgID: sc.org}, contextfabric.FactQuery{
					Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &stop},
					Kind:     contextfabric.FactInvestment,
					Subjects: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(tm.repoLabel+sc.org), Label: tm.repoLabel}},
				})
				repoGot := 0.0
				if repoErr == nil && len(repoResult.Facts) == 1 {
					for _, row := range repoResult.Facts[0].Fields["theme_breakdown"].Table.Rows {
						repoGot += *row.Fields["weighted_effort"].Number
					}
				}
				t.Logf("MATRIX repository scenario=%q repo=%s facts=%d effort=%v err=%v reason=%q note=%q", sc.name, tm.repoLabel, len(repoResult.Facts), repoGot, repoErr, repoResult.Reason, repoResult.RequestNote)
				t.Logf("MATRIX team note=%q", result.RequestNote)
				if wantToken := sc.token[tm.id]; wantToken != "" {
					served := contextfabric.CombineRequestNote(result.Reason, result.RequestNote) + " " + result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject(tm.id))]
					if !strings.Contains(served, wantToken) {
						t.Errorf("%s: served reason lacks %s: %q", tm.id, wantToken, served)
					}
				} else if sc.want[tm.id] == 0 {
					t.Errorf("%s: scenario expects no mix and names no token", tm.id)
				}
				if got != sc.want[tm.id] {
					t.Errorf("%s (%s) effort = %v, want %v (facts=%d reason=%q)", tm.id, tm.provider, got, sc.want[tm.id], len(result.Facts), result.Reason)
				}
			}
		})
	}
}
