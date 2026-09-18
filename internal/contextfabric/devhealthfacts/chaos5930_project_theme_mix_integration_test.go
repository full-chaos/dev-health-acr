package devhealthfacts_test

// CHAOS-5930: the project theme-mix roll-up's own SQL (project ->
// team_project_ownership -> team_repo_ownership -> work_unit_investments,
// joined directly on repo_id) is exactly the class of statement a
// fakeClient's canned rows cannot certify -- the join, the ARRAY JOIN over
// a Map(String, Float64) column, and the multi-CTE ownership-then-repo
// chain all need a real server evaluating the real query text. These tests
// EXECUTE InvestmentProvider.ReadFacts against a real ClickHouse over
// seeded projects/teams/ownership/work_unit_investments rows, mirroring
// chaos5893_completion_rollup_integration_test.go's own real-server
// discipline for the sibling roll-up.

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// repoUUID deterministically maps a short, readable test label (e.g.
// "repo-a1") to a stable UUID string -- repos.id/team_repo_ownership.repo_id/
// work_unit_investments.repo_id are all typed UUID in production (live
// schema, acr-trial-data/dh_0906), so a plain human-readable string is
// rejected outright ("Cannot parse uuid") rather than silently accepted.
// The label itself still names the repo everywhere ELSE (repo_full_name),
// so test failures stay readable.
func repoUUID(label string) string {
	sum := md5.Sum([]byte(label))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func createCHAOS5930Tables(t *testing.T, ctx context.Context, connection clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL(
		"projects", "teams", "team_project_ownership", "team_repo_ownership",
		"repos", "work_unit_investments", "investment_metrics_daily",
		"work_item_team_attributions",
	) {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
}

func readInvestmentFact(t *testing.T, providers []contextfabric.FactProvider, orgID string, subject contextfabric.SubjectRef) *contextfabric.CanonicalFact {
	t.Helper()
	provider := findProvider(t, providers, contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) == 0 {
		return nil
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1 (theme fields must MERGE onto the project's existing fact, never a second one)", result.Facts)
	}
	return &result.Facts[0]
}

// TestProjectThemeMixAgainstRealClickHouse proves the whole domain sweep
// (§ ownership fan-out, coverage vs. weight, null repo_id, merge-not-shadow,
// zero-effort honesty, and the served-output cap) against one real server.
func TestProjectThemeMixAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	at := ts(2026, 9, 18, 0, 0, 0)

	seedProject := func(id, orgID string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, orgID, "linear", nil, "Project "+id, uint8(1), "active", "", at); err != nil {
			t.Fatalf("seed project %s: %v", id, err)
		}
	}
	seedTeam := func(id, orgID, name string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			id, name, at, orgID, "linear", uint8(1)); err != nil {
			t.Fatalf("seed team %s: %v", id, err)
		}
	}
	seedProjectOwnership := func(orgID, teamID, projectID string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, projectID, nil, "native", at, nil, at); err != nil {
			t.Fatalf("seed team_project_ownership team=%s project=%s: %v", teamID, projectID, err)
		}
	}
	seedRepoOwnership := func(orgID, teamID, repoLabel string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, repoUUID(repoLabel), "acme/"+repoLabel, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatalf("seed team_repo_ownership team=%s repo=%s: %v", teamID, repoLabel, err)
		}
	}
	seedRepo := func(label, orgID string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatalf("seed repo %s: %v", label, err)
		}
	}
	seedWorkUnit := func(id, orgID, repoLabel string, effort float64, themes map[string]float64) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repoUUID(repoLabel), effort, themes, map[string]float64{}, "{}", at, orgID); err != nil {
			t.Fatalf("seed work_unit_investments %s: %v", id, err)
		}
	}
	seedWorkUnitNullRepo := func(id, orgID string, effort float64, themes map[string]float64) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, "{}", at, orgID); err != nil {
			t.Fatalf("seed null-repo work_unit_investments %s: %v", id, err)
		}
	}
	// seedWorkUnitNullRepoWithEvidence is seedWorkUnitNullRepo plus a real
	// structural_evidence_json PR reference into repoLabel -- the shape
	// evidence_resolved/wita/votes needs to evidence-vote-attribute a
	// repo_id-null work unit to a team, so excluded_no_repo_link has
	// something real to count.
	seedWorkUnitNullRepoWithEvidence := func(id, orgID, repoLabel string, prNumber int, effort float64, themes map[string]float64) {
		t.Helper()
		evidence := fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID(repoLabel), prNumber)
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, evidence, at, orgID); err != nil {
			t.Fatalf("seed evidenced null-repo work_unit_investments %s: %v", id, err)
		}
	}
	seedWorkItemTeamAttribution := func(orgID, repoLabel string, prNumber int, teamID string) {
		t.Helper()
		workItemID := fmt.Sprintf("ghpr:acme/%s#%d", repoLabel, prNumber)
		if err := direct.Exec(ctx,
			`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, repoUUID(repoLabel), workItemID, teamID, "Team", "linked_issue", uint8(1), "high", at); err != nil {
			t.Fatalf("seed work_item_team_attributions for %s: %v", workItemID, err)
		}
	}
	// seedWorkUnitNullRepoVotes seeds a repo_id-NULL work unit whose evidence
	// mixes matched PR refs (each resolvable to a repo, hence votable once a
	// work_item_team_attributions row exists for it) with UNMATCHED refs
	// (plain strings, never a "<uuid>#pr<n>" shape, so evidence_resolved
	// carries them through unresolved and wita can never match them) -- the
	// exact shape the votes CTE's cnt predicate must discriminate.
	seedWorkUnitNullRepoVotes := func(id, orgID string, prRepoLabels []string, prNumbers []int, unmatchedCount int) {
		t.Helper()
		if len(prRepoLabels) != len(prNumbers) {
			t.Fatalf("seedWorkUnitNullRepoVotes %s: mismatched pr slices", id)
		}
		prs := make([]string, 0, len(prRepoLabels))
		for i, label := range prRepoLabels {
			prs = append(prs, fmt.Sprintf(`"%s#pr%d"`, repoUUID(label), prNumbers[i]))
		}
		issues := make([]string, 0, unmatchedCount)
		for i := 0; i < unmatchedCount; i++ {
			issues = append(issues, fmt.Sprintf(`"UNMATCHED-%s-%d"`, id, i))
		}
		evidence := fmt.Sprintf(`{"issues":[%s],"prs":[%s]}`, strings.Join(issues, ","), strings.Join(prs, ","))
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, 10.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, evidence, at, orgID); err != nil {
			t.Fatalf("seed vote-shaped work_unit_investments %s: %v", id, err)
		}
	}

	t.Run("weighted_across_two_owning_teams_via_owned_repos", func(t *testing.T) {
		const orgID = "org-two-teams"
		seedProject("proj-a", orgID)
		seedTeam("team-a1", orgID, "Team A1")
		seedTeam("team-a2", orgID, "Team A2")
		seedProjectOwnership(orgID, "team-a1", "proj-a")
		seedProjectOwnership(orgID, "team-a2", "proj-a")
		seedRepo("repo-a1", orgID)
		seedRepo("repo-a2", orgID)
		seedRepoOwnership(orgID, "team-a1", "repo-a1")
		seedRepoOwnership(orgID, "team-a2", "repo-a2")
		// repo-a1's work: all feature_delivery (effort 10).
		seedWorkUnit("wu-a1", orgID, "repo-a1", 10, map[string]float64{"feature_delivery": 1.0})
		// repo-a2's work: all operational (effort 30) -- a bigger team should
		// pull the project-level share toward ITS theme, proving this is a
		// combined weighted total, not a per-team average.
		seedWorkUnit("wu-a2", orgID, "repo-a2", 30, map[string]float64{"operational": 1.0})

		fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-a"))
		if fact == nil {
			t.Fatal("facts = none, want a served theme-mix roll-up")
		}
		// feature_delivery share = 10 / (10+30) = 0.25; operational = 30/40 = 0.75.
		if got := factNumber(t, *fact, "theme_feature_delivery"); got != 0.25 {
			t.Errorf("theme_feature_delivery = %v, want 0.25 (10 of 40 total effort)", got)
		}
		if got := factNumber(t, *fact, "theme_operational"); got != 0.75 {
			t.Errorf("theme_operational = %v, want 0.75 (30 of 40 total effort)", got)
		}
		if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "team_project_ownership_via_owned_repos_work_unit_investments" {
			t.Errorf("rollup_basis = %#v", fact.Fields["rollup_basis"])
		}
		if got := factInt(t, *fact, "team_count"); got != 2 {
			t.Errorf("team_count = %d, want 2", got)
		}
		if got := factInt(t, *fact, "repo_count"); got != 2 {
			t.Errorf("repo_count = %d, want 2", got)
		}
		if got := factInt(t, *fact, "work_unit_count"); got != 2 {
			t.Errorf("work_unit_count = %d, want 2", got)
		}
		if got := factInt(t, *fact, "work_units_without_repo_link"); got != 0 {
			t.Errorf("work_units_without_repo_link = %d, want 0 -- neither seeded work unit is missing a repo link", got)
		}
		if fact.Fields["population_window"].String == nil || *fact.Fields["population_window"].String != "current" {
			t.Errorf("population_window = %#v, want \"current\"", fact.Fields["population_window"])
		}
	})

	t.Run("two_unrelated_projects_in_one_request_never_cross_attribute_each_others_repos", func(t *testing.T) {
		// Both the team_project_ownership->team_repo_ownership join and the
		// direct work_unit_investments.repo_id join are scoped to the SAME
		// requested project ids -- but every other subtest in this suite
		// requests at most one project whose OWN team/repo already belong to
		// it, so an unconditional ("ON 1 = 1") join would still land on the
		// right project by coincidence (nothing else to cross into). This
		// requests TWO unrelated projects -- different owning team, different
		// owned repo, different theme -- in ONE call, so a join that stopped
		// checking team_id/repo_id equality would leak repo-p1's
		// feature_delivery effort into proj-p2 (and repo-p2's risk effort
		// into proj-p1), which the assertions below would catch as a
		// polluted, non-1.0 share on either project.
		const orgID = "org-unrelated-projects"
		seedProject("proj-p1", orgID)
		seedProject("proj-p2", orgID)
		seedTeam("team-p1", orgID, "Team P1")
		seedTeam("team-p2", orgID, "Team P2")
		seedProjectOwnership(orgID, "team-p1", "proj-p1")
		seedProjectOwnership(orgID, "team-p2", "proj-p2")
		seedRepo("repo-p1", orgID)
		seedRepo("repo-p2", orgID)
		seedRepoOwnership(orgID, "team-p1", "repo-p1")
		seedRepoOwnership(orgID, "team-p2", "repo-p2")
		seedWorkUnit("wu-p1", orgID, "repo-p1", 10, map[string]float64{"feature_delivery": 1.0})
		seedWorkUnit("wu-p2", orgID, "repo-p2", 10, map[string]float64{"risk": 1.0})

		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{
				projectSubject("linear", "proj-p1"),
				projectSubject("linear", "proj-p2"),
			},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != 2 {
			t.Fatalf("facts = %#v, want exactly 2", result.Facts)
		}
		var p1, p2 *contextfabric.CanonicalFact
		for i := range result.Facts {
			switch result.Facts[i].Subject.Label {
			case "proj-p1":
				p1 = &result.Facts[i]
			case "proj-p2":
				p2 = &result.Facts[i]
			}
		}
		if p1 == nil || p2 == nil {
			t.Fatalf("facts = %#v, want one fact per requested project", result.Facts)
		}
		if got := factNumber(t, *p1, "theme_feature_delivery"); got != 1.0 {
			t.Errorf("proj-p1 theme_feature_delivery = %v, want 1.0 -- repo-p2's risk effort must not leak in via a mis-scoped join", got)
		}
		if got := factNumber(t, *p1, "theme_risk"); got != 0.0 {
			t.Errorf("proj-p1 theme_risk = %v, want 0.0", got)
		}
		if got := factNumber(t, *p2, "theme_risk"); got != 1.0 {
			t.Errorf("proj-p2 theme_risk = %v, want 1.0 -- repo-p1's feature_delivery effort must not leak in via a mis-scoped join", got)
		}
		if got := factNumber(t, *p2, "theme_feature_delivery"); got != 0.0 {
			t.Errorf("proj-p2 theme_feature_delivery = %v, want 0.0", got)
		}
		if got := factInt(t, *p1, "work_unit_count"); got != 1 {
			t.Errorf("proj-p1 work_unit_count = %d, want 1", got)
		}
		if got := factInt(t, *p2, "work_unit_count"); got != 1 {
			t.Errorf("proj-p2 work_unit_count = %d, want 1", got)
		}
	})

	t.Run("excluded_no_repo_link_counts_evidence_attributed_work_missing_a_repo_id", func(t *testing.T) {
		const orgID = "org-no-repo-link"
		seedProject("proj-cov", orgID)
		seedTeam("team-cov", orgID, "Team Coverage")
		seedProjectOwnership(orgID, "team-cov", "proj-cov")
		seedRepo("repo-cov", orgID)
		seedRepoOwnership(orgID, "team-cov", "repo-cov")
		// Counted: a normal repo-linked work unit, so this project also
		// serves theme shares -- the field this test cares about rides on
		// the same fact, not a bare coverage-only row.
		seedWorkUnit("wu-cov-counted", orgID, "repo-cov", 10, map[string]float64{"feature_delivery": 1.0})
		// Excluded: repo_id IS NULL on the work_unit_investments row itself,
		// but its OWN structural evidence (a PR against repo-cov) still
		// evidence-vote-attributes it to team-cov -- the SAME
		// evidence/wita/vote mechanism readTeamThemeMix already uses for the
		// team subject, reused here only to size this coverage gap, never
		// to recover the share into the counted total.
		seedWorkUnitNullRepoWithEvidence("wu-cov-excluded", orgID, "repo-cov", 7, 20, map[string]float64{"operational": 1.0})
		seedWorkItemTeamAttribution(orgID, "repo-cov", 7, "team-cov")

		fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-cov"))
		if fact == nil {
			t.Fatal("facts = none, want the counted work unit's roll-up")
		}
		if got := factInt(t, *fact, "work_unit_count"); got != 1 {
			t.Errorf("work_unit_count = %d, want 1 (only the repo-linked work unit)", got)
		}
		if got := factInt(t, *fact, "work_units_without_repo_link"); got != 1 {
			t.Errorf("work_units_without_repo_link = %d, want 1 -- the evidence-attributed, repo_id-null work unit must be disclosed as excluded, never silently dropped or folded into the counted share", got)
		}
		// counted + excluded = this project's disclosed work-unit
		// population for the theme mix (the population-partition rule).
		if got, want := factInt(t, *fact, "work_unit_count")+factInt(t, *fact, "work_units_without_repo_link"), int64(2); got != want {
			t.Errorf("work_unit_count + work_units_without_repo_link = %d, want %d", got, want)
		}
		// The excluded work unit's operational share must NOT leak into the
		// served theme shares -- only the counted work unit's
		// feature_delivery effort backs them.
		if got := factNumber(t, *fact, "theme_feature_delivery"); got != 1.0 {
			t.Errorf("theme_feature_delivery = %v, want 1.0 (only the counted work unit contributes)", got)
		}
	})

	t.Run("shared_repo_across_two_projects_is_coverage_not_weight", func(t *testing.T) {
		const orgID = "org-shared-repo"
		seedProject("proj-x", orgID)
		seedProject("proj-y", orgID)
		seedTeam("team-shared", orgID, "Shared Team")
		// The SAME team owns BOTH projects, and owns ONE repo.
		seedProjectOwnership(orgID, "team-shared", "proj-x")
		seedProjectOwnership(orgID, "team-shared", "proj-y")
		seedRepo("repo-shared", orgID)
		seedRepoOwnership(orgID, "team-shared", "repo-shared")
		seedWorkUnit("wu-shared", orgID, "repo-shared", 5, map[string]float64{"quality": 1.0})

		factX := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-x"))
		factY := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-y"))
		if factX == nil || factY == nil {
			t.Fatalf("facts = %#v / %#v, want both projects served", factX, factY)
		}
		// Each project sees the FULL share from the shared repo's work,
		// never a split -- a repo/team owned by several projects is a
		// coverage filter (which rows count toward THIS project), never a
		// weight divided across the projects that share it.
		if got := factNumber(t, *factX, "theme_quality"); got != 1.0 {
			t.Errorf("proj-x theme_quality = %v, want 1.0 (full, not split)", got)
		}
		if got := factNumber(t, *factY, "theme_quality"); got != 1.0 {
			t.Errorf("proj-y theme_quality = %v, want 1.0 (full, not split)", got)
		}
	})

	t.Run("null_repo_id_work_unit_is_invisible", func(t *testing.T) {
		const orgID = "org-null-repo"
		seedProject("proj-n", orgID)
		seedTeam("team-n", orgID, "Team N")
		seedProjectOwnership(orgID, "team-n", "proj-n")
		seedRepo("repo-n", orgID)
		seedRepoOwnership(orgID, "team-n", "repo-n")
		// This work unit has NO repo_id at all -- it must never be reachable
		// through the repo-keyed join, regardless of its theme content.
		seedWorkUnitNullRepo("wu-null", orgID, 100, map[string]float64{"risk": 1.0})

		fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-n"))
		if fact != nil {
			t.Fatalf("facts = %#v, want none -- a repo_id IS NULL work unit must be invisible to this roll-up, never fabricated in", fact)
		}
	})

	t.Run("zero_effort_omits_theme_fields_never_fabricates_zero", func(t *testing.T) {
		const orgID = "org-zero-effort"
		seedProject("proj-z", orgID)
		seedTeam("team-z", orgID, "Team Z")
		seedProjectOwnership(orgID, "team-z", "proj-z")
		seedRepo("repo-z", orgID)
		seedRepoOwnership(orgID, "team-z", "repo-z")
		seedWorkUnit("wu-z", orgID, "repo-z", 0, map[string]float64{"feature_delivery": 1.0})

		fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-z"))
		if fact != nil {
			t.Fatalf("facts = %#v, want none -- zero total effort must never fabricate a 0.0/degenerate share", fact)
		}
	})

	t.Run("merges_onto_existing_legacy_breakdown_fact_never_shadowed", func(t *testing.T) {
		const orgID = "org-merge"
		seedProject("proj-m", orgID)
		seedTeam("team-m", orgID, "Team M")
		seedProjectOwnership(orgID, "team-m", "proj-m")
		seedRepo("repo-m", orgID)
		seedRepoOwnership(orgID, "team-m", "repo-m")
		seedWorkUnit("wu-m", orgID, "repo-m", 10, map[string]float64{"maintenance": 1.0})
		// The LEGACY investment_metrics_daily row for the SAME team -- this
		// is what readProjectInvestment turns into the FIRST FactInvestment
		// fact for this project, appended BEFORE readProjectThemeMix runs.
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 9, 18), "team-m", "product", "growth", uint32(5), uint32(2), uint32(1), uint64(10), at, orgID); err != nil {
			t.Fatalf("seed legacy investment row: %v", err)
		}

		fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-m"))
		if fact == nil {
			t.Fatal("facts = none, want the merged fact")
		}
		if _, hasBreakdown := fact.Fields["team_breakdown"]; !hasBreakdown {
			t.Errorf("fields = %#v, want the legacy team_breakdown table still present (merged, not shadowed)", fact.Fields)
		}
		if got := factNumber(t, *fact, "theme_maintenance"); got != 1.0 {
			t.Errorf("theme_maintenance = %v, want 1.0 (merged onto the same fact the legacy breakdown produced)", got)
		}
	})

	t.Run("mixed_root_team_and_project_subjects_both_served", func(t *testing.T) {
		const orgID = "org-mixed-root"
		seedProject("proj-mix", orgID)
		seedTeam("team-mix-owner", orgID, "Team Mix Owner")
		seedTeam("team-mix-direct", orgID, "Team Mix Direct")
		seedProjectOwnership(orgID, "team-mix-owner", "proj-mix")
		seedRepo("repo-mix", orgID)
		seedRepoOwnership(orgID, "team-mix-owner", "repo-mix")
		seedWorkUnit("wu-mix", orgID, "repo-mix", 10, map[string]float64{"feature_delivery": 1.0})
		// team-mix-direct's own investment_metrics_daily row, read by the
		// SAME provider's team branch in the SAME ReadFacts call as the
		// project subject above -- proves widening this producer's project
		// path never makes it skip expansion for a co-requested team root.
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 9, 18), "team-mix-direct", "product", "growth", uint32(1), uint32(1), uint32(0), uint64(1), at, orgID); err != nil {
			t.Fatalf("seed direct team investment row: %v", err)
		}

		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{
				projectSubject("linear", "proj-mix"),
				teamSubject("team-mix-direct"),
			},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		var sawProject, sawTeam bool
		for _, fact := range result.Facts {
			if fact.Subject.Kind == contextfabric.SubjectProject {
				sawProject = true
			}
			if fact.Subject.Kind == contextfabric.SubjectTeam {
				sawTeam = true
			}
		}
		if !sawProject || !sawTeam {
			t.Fatalf("facts = %#v, want both the project root's theme-mix fact and the co-requested team root's own fact", result.Facts)
		}
	})

	t.Run("window_bound_excludes_work_units_outside_the_requested_range", func(t *testing.T) {
		const orgID = "org-window-bound"
		// Ownership edges must be valid AT THE END of the requested range
		// (ownershipValidityPredicate's as-of semantics), so this subtest's
		// own valid_from predates the whole [2026-06-01, 2026-07-01) window
		// under test -- seedProjectOwnership/seedRepoOwnership's shared `at`
		// (today) would otherwise make the ownership edge not-yet-valid as
		// of the requested range end, and every project/repo join upstream
		// of the range predicate would come back empty regardless of it.
		ownedSince := ts(2025, 1, 1, 0, 0, 0)
		seedProject("proj-window", orgID)
		seedTeam("team-window", orgID, "Team Window")
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-window", "proj-window", nil, "native", ownedSince, nil, ownedSince); err != nil {
			t.Fatalf("seed team_project_ownership: %v", err)
		}
		seedRepo("repo-window", orgID)
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-window", repoUUID("repo-window"), "acme/repo-window", "exact", "native", uint8(1), uint16(100), int32(0), ownedSince, nil, ownedSince); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
		inWindow := ts(2026, 6, 10, 0, 0, 0)
		outOfWindow := ts(2026, 1, 5, 0, 0, 0)
		seed := func(id string, validAt time.Time, effort float64, themes map[string]float64) {
			if err := direct.Exec(ctx,
				`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				id, validAt, validAt, repoUUID("repo-window"), effort, themes, map[string]float64{}, "{}", at, orgID); err != nil {
				t.Fatalf("seed work_unit_investments %s: %v", id, err)
			}
		}
		seed("wu-window-in", inWindow, 10, map[string]float64{"feature_delivery": 1.0})
		seed("wu-window-out", outOfWindow, 40, map[string]float64{"risk": 1.0})

		rangeStart := ts(2026, 6, 1, 0, 0, 0)
		rangeEnd := ts(2026, 7, 1, 0, 0, 0)
		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &rangeStart, End: &rangeEnd},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-window")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("facts = %#v, want exactly 1", result.Facts)
		}
		fact := result.Facts[0]
		// Every canonical theme key is always present (this producer writes
		// all five unconditionally once currentTotal > 0) -- the range
		// predicate's effect shows up in the SHARE VALUE, not in whether the
		// key exists. A theme_risk share above 0 would mean the
		// out-of-window work unit (2026-01-05) leaked past the predicate.
		if got := factNumber(t, fact, "theme_feature_delivery"); got != 1.0 {
			t.Errorf("theme_feature_delivery = %v, want 1.0 -- only the in-window work unit must contribute", got)
		}
		if got := factNumber(t, fact, "theme_risk"); got != 0.0 {
			t.Errorf("theme_risk = %v, want 0.0 -- the out-of-window work unit must not contribute", got)
		}
		if got := factInt(t, fact, "work_unit_count"); got != 1 {
			t.Errorf("work_unit_count = %d, want 1 (the out-of-window work unit excluded, not counted)", got)
		}
		if fact.Fields["population_window"].String == nil || *fact.Fields["population_window"].String != "requested_range" {
			t.Errorf("population_window = %#v, want \"requested_range\"", fact.Fields["population_window"])
		}
	})

	t.Run("served_output_capped_at_the_row_limit_with_truncation_disclosed", func(t *testing.T) {
		const orgID = "org-limit-probe-5930"
		const exactLimit = 200
		subjects := make([]contextfabric.SubjectRef, 0, exactLimit+1)
		for i := 0; i < exactLimit+1; i++ {
			label := fmt.Sprintf("limit-%04d", i)
			projectID := "PROJ-" + label
			teamID := "TEAM-" + label
			repoLabel := "repo-" + label
			seedProject(projectID, orgID)
			seedTeam(teamID, orgID, "Team "+label)
			seedProjectOwnership(orgID, teamID, projectID)
			seedRepo(repoLabel, orgID)
			seedRepoOwnership(orgID, teamID, repoLabel)
			seedWorkUnit("WU-"+label, orgID, repoLabel, 10, map[string]float64{"feature_delivery": 1.0})
			subjects = append(subjects, projectSubject("linear", projectID))
		}

		provider := findProvider(t, providers, contextfabric.FactInvestment)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != exactLimit {
			t.Fatalf("facts = %d, want capped at %d even though %d projects were requested", len(result.Facts), exactLimit, len(subjects))
		}
		if !result.Truncated {
			t.Fatalf("Truncated = false for %d requested projects (cap %d), want true -- the LIMIT+1 probe row must be detected and disclosed", len(subjects), exactLimit)
		}
	})

	// evidence_vote_attribution_partition_identity proves the votes CTE's
	// cnt predicate genuinely discriminates matched from unmatched evidence
	// -- an unmatched-evidence bucket that wins the argMax vote silently
	// drops the work unit from EITHER partition class (counted or
	// work_units_without_repo_link), breaking the disclosed population
	// identity. Every subtest here uses a repo_id-NULL work unit (only the
	// evidence-vote path can attribute one at all), one owning team per
	// project so the winning vote (if any) is unambiguous, and asserts
	// work_units_without_repo_link directly rather than inferring it.
	t.Run("evidence_vote_attribution_partition_identity", func(t *testing.T) {
		setupTeamProject := func(t *testing.T, orgID, projectID, teamID string) {
			t.Helper()
			seedProject(projectID, orgID)
			seedTeam(teamID, orgID, "Team "+teamID)
			seedProjectOwnership(orgID, teamID, projectID)
		}

		t.Run("one_matched_plus_n_unmatched_attributes_to_the_real_team", func(t *testing.T) {
			const orgID = "org-vote-1m3u"
			setupTeamProject(t, orgID, "proj-1m3u", "team-1m3u")
			seedRepo("repo-1m3u", orgID)
			seedRepoOwnership(orgID, "team-1m3u", "repo-1m3u")
			// A counted work unit rides alongside the vote-shaped one so the
			// project serves a fact at all (a project with ZERO counted
			// work is a separate, already-flagged scope boundary -- see
			// this producer's own doc comment -- not what this subtest is
			// about).
			seedWorkUnit("wu-1m3u-counted", orgID, "repo-1m3u", 10, map[string]float64{"feature_delivery": 1.0})
			seedWorkUnitNullRepoVotes("wu-1m3u", orgID, []string{"repo-1m3u"}, []int{1}, 3)
			seedWorkItemTeamAttribution(orgID, "repo-1m3u", 1, "team-1m3u")

			fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-1m3u"))
			if fact == nil {
				t.Fatal("facts = none, want the work unit attributed to team-1m3u and disclosed as excluded (no repo link)")
			}
			if got := factInt(t, *fact, "work_units_without_repo_link"); got != 1 {
				t.Errorf("work_units_without_repo_link = %d, want 1 -- 1 real vote must outweigh 3 unmatched-evidence rows, never the reverse", got)
			}
		})

		t.Run("n_matched_plus_one_unmatched_still_attributes_to_the_real_team", func(t *testing.T) {
			const orgID = "org-vote-2m1u"
			setupTeamProject(t, orgID, "proj-2m1u", "team-2m1u")
			seedRepo("repo-2m1u", orgID)
			seedRepoOwnership(orgID, "team-2m1u", "repo-2m1u")
			seedWorkUnit("wu-2m1u-counted", orgID, "repo-2m1u", 10, map[string]float64{"feature_delivery": 1.0})
			seedWorkUnitNullRepoVotes("wu-2m1u", orgID, []string{"repo-2m1u", "repo-2m1u"}, []int{1, 2}, 1)
			seedWorkItemTeamAttribution(orgID, "repo-2m1u", 1, "team-2m1u")
			seedWorkItemTeamAttribution(orgID, "repo-2m1u", 2, "team-2m1u")

			fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-2m1u"))
			if fact == nil {
				t.Fatal("facts = none, want the work unit attributed to team-2m1u and disclosed as excluded (no repo link)")
			}
			if got := factInt(t, *fact, "work_units_without_repo_link"); got != 1 {
				t.Errorf("work_units_without_repo_link = %d, want 1", got)
			}
		})

		t.Run("all_unmatched_is_reachable_by_neither_partition_class", func(t *testing.T) {
			const orgID = "org-vote-0m3u"
			setupTeamProject(t, orgID, "proj-0m3u", "team-0m3u")
			seedRepo("repo-0m3u", orgID)
			seedRepoOwnership(orgID, "team-0m3u", "repo-0m3u")
			seedWorkUnitNullRepoVotes("wu-0m3u", orgID, nil, nil, 3)

			fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-0m3u"))
			if fact != nil {
				t.Fatalf("facts = %#v, want none -- a work unit with zero matched evidence is reachable by neither the counted nor the excluded partition, never fabricated into either", fact)
			}
		})

		t.Run("tie_between_two_real_teams_breaks_deterministically_and_excludes_the_unmatched_bucket", func(t *testing.T) {
			const orgID = "org-vote-tie"
			setupTeamProject(t, orgID, "proj-tie-a", "team-tie-a")
			setupTeamProject(t, orgID, "proj-tie-b", "team-tie-b")
			seedRepo("repo-tie-a", orgID)
			seedRepo("repo-tie-b", orgID)
			seedRepoOwnership(orgID, "team-tie-a", "repo-tie-a")
			seedRepoOwnership(orgID, "team-tie-b", "repo-tie-b")
			// One vote for team-tie-a, one for team-tie-b (a genuine 1-1 tie
			// between two REAL teams), plus 3 unmatched refs -- under the
			// bug the unmatched bucket (cnt=3) would beat BOTH real votes
			// and the work unit would attribute to neither project at all.
			seedWorkUnit("wu-tie-a-counted", orgID, "repo-tie-a", 10, map[string]float64{"feature_delivery": 1.0})
			seedWorkUnit("wu-tie-b-counted", orgID, "repo-tie-b", 10, map[string]float64{"feature_delivery": 1.0})
			seedWorkUnitNullRepoVotes("wu-tie", orgID, []string{"repo-tie-a", "repo-tie-b"}, []int{1, 1}, 3)
			seedWorkItemTeamAttribution(orgID, "repo-tie-a", 1, "team-tie-a")
			seedWorkItemTeamAttribution(orgID, "repo-tie-b", 1, "team-tie-b")

			factA := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-tie-a"))
			factB := readInvestmentFact(t, providers, orgID, projectSubject("linear", "proj-tie-b"))
			// Both projects serve a fact (each has its own counted work
			// unit), but argMax(vote_team_id, (cnt, vote_team_id)) breaks
			// the equal-cnt tie by the LARGER vote_team_id string --
			// "team-tie-b" > "team-tie-a" -- so only team-tie-b's project
			// sees the vote-shaped work unit disclosed as excluded; never
			// both, never neither.
			if factA == nil {
				t.Fatal("facts = none for proj-tie-a, want its own counted work unit served")
			}
			if factB == nil {
				t.Fatal("facts = none for proj-tie-b, want the tie-broken attribution disclosed as excluded (no repo link)")
			}
			if got := factInt(t, *factA, "work_units_without_repo_link"); got != 0 {
				t.Errorf("proj-tie-a work_units_without_repo_link = %d, want 0 -- the tie breaks to team-tie-b, never both projects", got)
			}
			if got := factInt(t, *factB, "work_units_without_repo_link"); got != 1 {
				t.Errorf("proj-tie-b work_units_without_repo_link = %d, want 1", got)
			}
		})
	})
}
