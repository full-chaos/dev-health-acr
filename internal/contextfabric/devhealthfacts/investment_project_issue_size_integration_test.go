package devhealthfacts_test

// The project mixes weigh a work unit by its issue size, EXECUTED against a
// real ClickHouse, through the one weight function every mix shares: a unit in
// a team's project-linked mix and in a project's native mix weighs the same in
// both. The scopes differ (a team, a project), so the test compares the unit's
// weight, never the totals of the two scopes. Every number is hand-computed.

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestProjectMixesWeighAUnitLikeTheTeamLinkedMixAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-project-size"
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	r, a := repoUUID("repo-r"), repoUUID("repo-a")
	for _, label := range []string{"repo-r", "repo-a"} {
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", at)
	}
	for _, project := range []string{"proj-1", "proj-2"} {
		exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, orgID, "linear", nil, project, uint8(1), "active", "", at)
	}
	for _, team := range []string{"team-linked", "team-roll"} {
		exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, orgID, "linear", uint8(1))
	}
	for team, project := range map[string]string{"team-linked": "proj-1", "team-roll": "proj-2"} {
		exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", team, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	exec("repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-roll", a, "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		"i-1", r, orgID, "title", at, "proj-1")
	exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r, "i-1", uint32(1), float32(1), "native", "", at, orgID)
	exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		r, orgID, uint32(1), "pr", "open", at, at, "feature", "")

	seedUnit := func(id string, themes map[string]float64, storedRepo, evidence string) {
		t.Helper()
		var repoID any
		if storedRepo != "" {
			repoID = storedRepo
		}
		exec("unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repoID, 123456.0, themes, map[string]float64{}, evidence, at, orgID)
	}
	seedSize := func(id string, size float64) {
		t.Helper()
		exec("size "+id, `INSERT INTO work_unit_issue_size (org_id, work_unit_id, issue_size, computed_at) VALUES (?,?,?,?)`, orgID, id, size, at)
	}
	// wu-both reaches proj-1 by its issue (native mix) and repo-r by its pull
	// request, which proj-1's issue links (team-linked's project-linked mix).
	seedUnit("wu-both", map[string]float64{"feature_delivery": 1}, "", fmt.Sprintf(`{"issues":["i-1"],"prs":["%s#pr1"]}`, r))
	seedSize("wu-both", 6)
	// proj-2 is owned by team-roll, which owns repo-a: the roll-up counts the
	// units stored on repo-a, one sized 4 and one unsized (counts 1).
	seedUnit("wu-sized", map[string]float64{"operational": 1}, a, `{"issues":[],"prs":[]}`)
	seedSize("wu-sized", 4)
	seedUnit("wu-unsized", map[string]float64{"operational": 1}, a, `{"issues":[],"prs":[]}`)

	read := func(subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("%s: err=%v facts=%d reason=%q", subject.CanonicalID, err, len(result.Facts), result.Reason)
		}
		return result.Facts[0]
	}
	near := func(what string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", what, got, want)
		}
	}

	native := read(projectSubject("linear", "proj-1"))
	linked := read(teamSubject("team-linked"))
	if got := *native.Fields["investment_mix_source"].String; got != contextfabric.InvestmentMixSourceProjectNative {
		t.Fatalf("proj-1 mix source = %q, want the project-native mix", got)
	}
	// One unit in each scope: the scope's denominator is that unit's weight.
	near("native weight of wu-both", *native.Fields["denominator"].Number, 6)
	near("team-linked weight of wu-both", *linked.Fields["denominator"].Number, 6)
	near("the same unit's weight in both facts", *native.Fields["denominator"].Number, *linked.Fields["denominator"].Number)
	for name, fact := range map[string]contextfabric.CanonicalFact{"proj-1": native, "team-linked": linked} {
		if got := *fact.Fields["measure"].String; got != "ISSUE_SIZE" {
			t.Errorf("%s: measure = %q", name, got)
		}
		near(name+" sizing_coverage", *fact.Fields["sizing_coverage"].Number, 1)
		if got := *fact.Fields["unsized_unit_count"].Integer; got != 0 {
			t.Errorf("%s: unsized_unit_count = %d, want 0", name, got)
		}
		if _, has := fact.Fields["sizing_note"]; has {
			t.Errorf("%s: a fully sized mix carries a sizing_note", name)
		}
	}

	rollup := read(projectSubject("linear", "proj-2"))
	if got := *rollup.Fields["investment_mix_source"].String; got != contextfabric.InvestmentMixSourceOwningTeamRollup {
		t.Fatalf("proj-2 mix source = %q, want the owning-team roll-up", got)
	}
	if got := *rollup.Fields["measure"].String; got != "ISSUE_SIZE" {
		t.Errorf("proj-2: measure = %q", got)
	}
	near("proj-2 denominator", *rollup.Fields["denominator"].Number, 5)
	near("proj-2 sizing_coverage", *rollup.Fields["sizing_coverage"].Number, 0.5)
	if got := *rollup.Fields["unsized_unit_count"].Integer; got != 1 {
		t.Errorf("proj-2: unsized_unit_count = %d, want 1", got)
	}
	if got := *rollup.Fields["work_unit_count"].Integer; got != 2 {
		t.Errorf("proj-2: work_unit_count = %d, want 2", got)
	}
}
