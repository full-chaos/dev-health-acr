package devhealthfacts_test

// CHAOS-5935: a project's investment theme mix attributed through its OWN
// work items, beside the owning-team roll-up. These tests execute
// InvestmentProvider.ReadFacts against a real ClickHouse over seeded
// projects, work items, ownership and work_unit_investments rows, and read
// back which source the served fact carries.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func factString(t *testing.T, fact contextfabric.CanonicalFact, field string) string {
	t.Helper()
	value, ok := fact.Fields[field]
	if !ok || value.String == nil {
		t.Fatalf("field %q = %#v, want a string", field, value)
	}
	return *value.String
}

func TestQueryVersionMovedPastTheUnlabelledProjectMix(t *testing.T) {
	if devhealthfacts.QueryVersion == "devhealthfacts.clickhouse.v12" {
		t.Fatalf("QueryVersion = %q: a candidate saved before project theme facts named their source must not be served as though it did", devhealthfacts.QueryVersion)
	}
}

func TestProjectNativeThemeMixAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"projects", "teams", "team_project_ownership", "team_repo_ownership", "repos",
		"work_unit_investments", "investment_metrics_daily", "work_item_team_attributions",
		"work_items", "project_membership_transitions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create view: %v", err)
	}
	providers := devhealthfacts.NewProviders(query)
	at := ts(2026, 9, 18, 0, 0, 0)

	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	seedProject := func(id, orgID string) {
		exec("project "+id, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, "linear", nil, "Project "+id, uint8(1), "active", "", at)
	}
	seedOwnedRepo := func(orgID, projectID, teamID, repoLabel string) {
		exec("team "+teamID, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			teamID, teamID, at, orgID, "linear", uint8(1))
		exec("ownership "+teamID, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, projectID, nil, "native", at, nil, at)
		exec("repo "+repoLabel, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repoLabel), orgID, "acme/"+repoLabel, "github", at)
		exec("repo ownership "+repoLabel, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, repoUUID(repoLabel), "acme/"+repoLabel, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
	}
	seedItem := func(orgID, itemID, projectID string) {
		exec("work item "+itemID, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"00000000-0000-0000-0000-000000000000", itemID, "linear", "title", "issue", "open", "", projectID, "", "", at, at, nil, "", "", at, orgID)
	}
	seedRepoUnit := func(orgID, id, repoLabel string, effort float64, themes map[string]float64) {
		exec("repo unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repoUUID(repoLabel), effort, themes, map[string]float64{}, "{}", at, orgID)
	}
	seedIssueUnit := func(orgID, id string, effort float64, themes map[string]float64, issues ...string) {
		evidence := `{"issues":[`
		for i, issue := range issues {
			if i > 0 {
				evidence += ","
			}
			evidence += fmt.Sprintf("%q", issue)
		}
		evidence += `],"prs":[]}`
		exec("issue unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, evidence, at, orgID)
	}
	read := func(orgID string, projects ...string) map[string]contextfabric.CanonicalFact {
		t.Helper()
		subjects := make([]contextfabric.SubjectRef, len(projects))
		for i, project := range projects {
			subjects[i] = projectSubject("linear", project)
		}
		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		out := map[string]contextfabric.CanonicalFact{}
		for _, fact := range result.Facts {
			if _, dup := out[fact.Subject.Label]; dup {
				t.Fatalf("two facts for %s: %#v", fact.Subject.Label, result.Facts)
			}
			out[fact.Subject.Label] = fact
		}
		return out
	}
	absent := func(t *testing.T, fact contextfabric.CanonicalFact, fields ...string) {
		t.Helper()
		for _, field := range fields {
			if _, has := fact.Fields[field]; has {
				t.Errorf("field %q present: %#v", field, fact.Fields[field])
			}
		}
	}

	t.Run("native_mix_replaces_the_rollup_and_moves_its_population_aside", func(t *testing.T) {
		const org = "org-native"
		seedProject("proj-n", org)
		seedOwnedRepo(org, "proj-n", "team-n", "repo-n")
		seedRepoUnit(org, "wu-rollup", "repo-n", 100, map[string]float64{"feature_delivery": 1.0})
		seedItem(org, "linear:N-1", "proj-n")
		seedIssueUnit(org, "wu-native", 10, map[string]float64{"operational": 1.0}, "linear:N-1")

		fact := read(org, "proj-n")["proj-n"]
		if got := factString(t, fact, "investment_mix_source"); got != "project_native" {
			t.Fatalf("investment_mix_source = %q, want project_native", got)
		}
		if got := factNumber(t, fact, "theme_operational"); got != 1.0 {
			t.Errorf("theme_operational = %v, want 1.0 (the roll-up's feature_delivery effort must not leak in)", got)
		}
		if got := factNumber(t, fact, "theme_feature_delivery"); got != 0.0 {
			t.Errorf("theme_feature_delivery = %v, want 0", got)
		}
		if got := factInt(t, fact, "work_unit_count"); got != 1 {
			t.Errorf("work_unit_count = %d, want the native population 1", got)
		}
		if got := factInt(t, fact, "effort_unit_count"); got != 1 {
			t.Errorf("effort_unit_count = %d, want 1", got)
		}
		if got := factInt(t, fact, "spanning_unit_count"); got != 0 {
			t.Errorf("spanning_unit_count = %d, want 0", got)
		}
		if got := factInt(t, fact, "owning_team_rollup_work_unit_count"); got != 1 {
			t.Errorf("owning_team_rollup_work_unit_count = %d, want the roll-up's own population 1", got)
		}
		if got := factString(t, fact, "rollup_basis"); got != "project_work_items_issue_evidence_work_unit_investments" {
			t.Errorf("rollup_basis = %q", got)
		}
		absent(t, fact, "team_count", "repo_count", "work_units_without_repo_link")
	})

	t.Run("a_project_with_no_native_units_keeps_the_labelled_rollup", func(t *testing.T) {
		const org = "org-rollup-only"
		seedProject("proj-r", org)
		seedOwnedRepo(org, "proj-r", "team-r", "repo-r")
		seedRepoUnit(org, "wu-r", "repo-r", 40, map[string]float64{"quality": 1.0})

		fact := read(org, "proj-r")["proj-r"]
		if got := factString(t, fact, "investment_mix_source"); got != "owning_team_rollup" {
			t.Fatalf("investment_mix_source = %q, want owning_team_rollup", got)
		}
		if got := factNumber(t, fact, "theme_quality"); got != 1.0 {
			t.Errorf("theme_quality = %v, want 1.0", got)
		}
		absent(t, fact, "effort_unit_count", "spanning_unit_count", "owning_team_rollup_work_unit_count")
		if got := factInt(t, fact, "team_count"); got != 1 {
			t.Errorf("team_count = %d, want 1", got)
		}
	})

	t.Run("native_units_with_no_effort_leave_the_rollup_and_never_a_zero_mix", func(t *testing.T) {
		const org = "org-zero-native"
		seedProject("proj-z", org)
		seedOwnedRepo(org, "proj-z", "team-z", "repo-z")
		seedRepoUnit(org, "wu-z-rollup", "repo-z", 25, map[string]float64{"risk": 1.0})
		seedItem(org, "linear:Z-1", "proj-z")
		seedIssueUnit(org, "wu-z-native", 0, map[string]float64{"operational": 1.0}, "linear:Z-1")

		fact := read(org, "proj-z")["proj-z"]
		if got := factString(t, fact, "investment_mix_source"); got != "owning_team_rollup" {
			t.Fatalf("investment_mix_source = %q, want owning_team_rollup (zero effort is not a mix)", got)
		}
		if got := factNumber(t, fact, "theme_risk"); got != 1.0 {
			t.Errorf("theme_risk = %v, want the roll-up's 1.0", got)
		}
		absent(t, fact, "effort_unit_count")
	})

	t.Run("a_project_with_only_native_work_serves_a_standalone_native_fact", func(t *testing.T) {
		const org = "org-native-only"
		seedProject("proj-o", org)
		seedItem(org, "linear:O-1", "proj-o")
		seedIssueUnit(org, "wu-o", 8, map[string]float64{"maintenance": 0.5, "risk": 0.5}, "linear:O-1")

		fact := read(org, "proj-o")["proj-o"]
		if got := factString(t, fact, "investment_mix_source"); got != "project_native" {
			t.Fatalf("investment_mix_source = %q, want project_native", got)
		}
		if got := factNumber(t, fact, "theme_maintenance"); got != 0.5 {
			t.Errorf("theme_maintenance = %v, want 0.5", got)
		}
		absent(t, fact, "owning_team_rollup_work_unit_count", "team_count")
	})

	t.Run("native_mix_merges_onto_the_legacy_breakdown_fact_when_there_is_no_rollup", func(t *testing.T) {
		const org = "org-native-legacy"
		seedProject("proj-l", org)
		seedOwnedRepo(org, "proj-l", "team-l", "repo-l")
		exec("legacy investment row", `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 9, 18), "team-l", "product", "growth", uint32(5), uint32(2), uint32(1), uint64(10), at, org)
		seedItem(org, "linear:L-1", "proj-l")
		seedIssueUnit(org, "wu-l", 10, map[string]float64{"quality": 1.0}, "linear:L-1")

		fact := read(org, "proj-l")["proj-l"]
		if _, has := fact.Fields["team_breakdown"]; !has {
			t.Errorf("fields = %#v, want the legacy team_breakdown kept on the same fact", fact.Fields)
		}
		if got := factString(t, fact, "investment_mix_source"); got != "project_native" {
			t.Errorf("investment_mix_source = %q, want project_native", got)
		}
		absent(t, fact, "owning_team_rollup_work_unit_count")
	})

	t.Run("a_unit_spanning_two_projects_counts_in_full_for_each_and_is_disclosed", func(t *testing.T) {
		const org = "org-span"
		seedProject("proj-s1", org)
		seedProject("proj-s2", org)
		seedItem(org, "linear:S-1", "proj-s1")
		seedItem(org, "linear:S-2", "proj-s2")
		seedIssueUnit(org, "wu-span", 10, map[string]float64{"feature_delivery": 1.0}, "linear:S-1", "linear:S-2")
		seedIssueUnit(org, "wu-s1-only", 30, map[string]float64{"quality": 1.0}, "linear:S-1")

		facts := read(org, "proj-s1", "proj-s2")
		s1, s2 := facts["proj-s1"], facts["proj-s2"]
		if got := factInt(t, s1, "work_unit_count"); got != 2 {
			t.Errorf("proj-s1 work_unit_count = %d, want 2", got)
		}
		if got := factNumber(t, s1, "theme_feature_delivery"); got != 0.25 {
			t.Errorf("proj-s1 theme_feature_delivery = %v, want 0.25 (10 of 40)", got)
		}
		if got := factInt(t, s1, "spanning_unit_count"); got != 1 {
			t.Errorf("proj-s1 spanning_unit_count = %d, want 1", got)
		}
		if got := factInt(t, s2, "work_unit_count"); got != 1 {
			t.Errorf("proj-s2 work_unit_count = %d, want 1", got)
		}
		if got := factNumber(t, s2, "theme_feature_delivery"); got != 1.0 {
			t.Errorf("proj-s2 theme_feature_delivery = %v, want 1.0", got)
		}
		if got := factInt(t, s2, "spanning_unit_count"); got != 1 {
			t.Errorf("proj-s2 spanning_unit_count = %d, want 1", got)
		}
	})

	t.Run("the_requested_range_bounds_native_units_and_is_disclosed", func(t *testing.T) {
		const org = "org-native-window"
		seedProject("proj-w", org)
		seedItem(org, "linear:W-1", "proj-w")
		seed := func(id string, validAt time.Time, effort float64, themes map[string]float64) {
			exec("windowed unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
				id, validAt, validAt, effort, themes, map[string]float64{}, `{"issues":["linear:W-1"],"prs":[]}`, at, org)
		}
		seed("wu-w-in", ts(2026, 6, 10, 0, 0, 0), 10, map[string]float64{"feature_delivery": 1.0})
		seed("wu-w-out", ts(2026, 1, 5, 0, 0, 0), 40, map[string]float64{"risk": 1.0})
		rangeStart, rangeEnd := ts(2026, 6, 1, 0, 0, 0), ts(2026, 7, 1, 0, 0, 0)
		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &rangeStart, End: &rangeEnd},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-w")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("facts = %#v err = %v, want exactly one", result.Facts, err)
		}
		fact := result.Facts[0]
		if got := factNumber(t, fact, "theme_feature_delivery"); got != 1.0 {
			t.Errorf("theme_feature_delivery = %v, want 1.0 (the out-of-range risk effort must not count)", got)
		}
		if got := factInt(t, fact, "work_unit_count"); got != 1 {
			t.Errorf("work_unit_count = %d, want 1", got)
		}
		if got := factString(t, fact, "population_window"); got != "requested_range" {
			t.Errorf("population_window = %q, want requested_range", got)
		}
	})

	t.Run("another_organizations_work_never_reaches_the_project", func(t *testing.T) {
		seedProject("proj-x", "org-x-a")
		seedProject("proj-x", "org-x-b")
		seedItem("org-x-a", "linear:X-1", "proj-x")
		seedItem("org-x-b", "linear:X-1", "proj-x")
		seedIssueUnit("org-x-a", "wu-x-a", 10, map[string]float64{"quality": 1.0}, "linear:X-1")
		seedIssueUnit("org-x-b", "wu-x-b", 90, map[string]float64{"risk": 1.0}, "linear:X-1")

		fact := read("org-x-a", "proj-x")["proj-x"]
		if got := factNumber(t, fact, "theme_quality"); got != 1.0 {
			t.Errorf("theme_quality = %v, want 1.0 (org-x-b's risk effort must not leak in)", got)
		}
		if got := factInt(t, fact, "work_unit_count"); got != 1 {
			t.Errorf("work_unit_count = %d, want 1", got)
		}
	})
}
