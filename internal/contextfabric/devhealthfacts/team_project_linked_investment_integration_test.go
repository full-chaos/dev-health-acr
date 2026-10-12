package devhealthfacts_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team that owns a project reaches repositories only through the project's
// issues' linked pull requests. Its investment mix counts the work units of
// those linked pull requests, never the whole mix of a repository it does not
// own; a repository it owns keeps its whole mix.
func TestTeamInvestmentMixCountsProjectLinkedPullRequestsOfUnownedRepositories(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-linked-investment"
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	for _, label := range []string{"repo-a", "repo-r"} {
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", at)
	}
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-1", orgID, "linear", nil, "proj-1", uint8(1), "active", "", at)
	for _, team := range []string{"team-owner", "team-both", "team-project-only", "team-nothing"} {
		exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, orgID, "linear", uint8(1))
	}
	for _, team := range []string{"team-both", "team-project-only"} {
		exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", team, "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	for _, team := range []string{"team-owner", "team-both"} {
		exec("repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", team, repoUUID("repo-a"), "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	for _, id := range []string{"i-1", "i-2", "i-3"} {
		exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			id, repoUUID("repo-a"), orgID, "title "+id, at, "proj-1")
	}
	link := func(id, repo string, number uint32, tier string) {
		exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(repo), id, number, float32(1), tier, "", at, orgID)
		exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(repo), orgID, number, "pr", "open", at, at, "feature", "")
	}
	link("i-1", "repo-a", 1, "native")
	link("i-2", "repo-r", 2, "explicit_text")
	link("i-3", "repo-r", 3, "heuristic")
	seed := func(id string, themes map[string]float64, effort float64, refs ...string) {
		t.Helper()
		prs := make([]string, len(refs))
		for i, ref := range refs {
			prs[i] = `"` + ref + `"`
		}
		exec("work unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, at, effort, themes, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":[%s]}`, strings.Join(prs, ",")), at, orgID)
	}
	a, r := repoUUID("repo-a"), repoUUID("repo-r")
	// wu1 splits over repo-a#1 and repo-r#2 (5 each); wu2 over repo-r#3 (linked)
	// and repo-r#20 (not linked): 4 each; wu3 is repo-r#4 alone, linked to no
	// issue of an owned project; wu4 is repo-a#10 alone (owned whole).
	seed("wu1", map[string]float64{"feature_delivery": 1.0}, 10, a+"#pr1", r+"#pr2")
	seed("wu2", map[string]float64{"risk": 1.0}, 8, r+"#pr3", r+"#pr20")
	seed("wu3", map[string]float64{"maintenance": 1.0}, 6, r+"#pr4")
	seed("wu4", map[string]float64{"operational": 1.0}, 20, a+"#pr10")

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	read := func(team string) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s): %v", team, err)
		}
		return result
	}
	weighted := func(fact contextfabric.CanonicalFact) map[string]float64 {
		out := map[string]float64{}
		for _, row := range fact.Fields["theme_breakdown"].Table.Rows {
			out[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
		}
		return out
	}
	want := func(t *testing.T, got map[string]float64, expected map[string]float64) {
		t.Helper()
		for theme, effort := range expected {
			if math.Abs(got[theme]-effort) > 1e-9 {
				t.Fatalf("weighted effort = %v, want %v", got, expected)
			}
		}
	}

	t.Run("owner-only team keeps its owned mix and gains no project fields", func(t *testing.T) {
		fact := factFor(t, read("team-owner"), teamSubject("team-owner"))
		want(t, weighted(fact), map[string]float64{"feature_delivery": 5, "operational": 20, "risk": 0, "maintenance": 0})
		if _, ok := fact.Fields["project_reached_repository_count"]; ok {
			t.Fatalf("a team that owns no project carries project_reached_repository_count")
		}
	})

	t.Run("owned repository whole plus unowned repository by linked pull request", func(t *testing.T) {
		fact := factFor(t, read("team-both"), teamSubject("team-both"))
		// repo-a whole (5 feature + 20 operational) plus repo-r's linked #2 (5
		// feature) and #3 (4 risk); not #20 (4 risk) and not #4 (6 maintenance).
		want(t, weighted(fact), map[string]float64{"feature_delivery": 10, "operational": 20, "risk": 4, "maintenance": 0})
		wantInt(t, fact.Fields, "owned_repository_count", 1)
		wantInt(t, fact.Fields, "project_reached_repository_count", 1)
		if got := stringField(t, fact, "repository_link_basis"); got != "heuristic" {
			t.Fatalf("repository_link_basis = %q, want heuristic (the weakest link tier, never native)", got)
		}
		if got := stringField(t, fact, "attribution_basis"); !strings.HasSuffix(got, "_over_team_repo_ownership_and_project_links") {
			t.Fatalf("attribution_basis = %q, want the project-link basis named", got)
		}
	})

	t.Run("project-only team is served from linked pull requests alone", func(t *testing.T) {
		result := read("team-project-only")
		fact := factFor(t, result, teamSubject("team-project-only"))
		// linked: repo-a#1 (5 feature), repo-r#2 (5 feature), repo-r#3 (4 risk).
		want(t, weighted(fact), map[string]float64{"feature_delivery": 10, "risk": 4, "operational": 0})
		wantInt(t, fact.Fields, "owned_repository_count", 0)
		wantInt(t, fact.Fields, "project_reached_repository_count", 2)
		if strings.Contains(result.Reason, "owns no repository") {
			t.Fatalf("reason = %q, a team that reaches repositories is not told it owns none", result.Reason)
		}
	})

	t.Run("a team with neither ownership nor project is told so", func(t *testing.T) {
		result := read("team-nothing")
		if len(result.Facts) != 0 || !strings.Contains(result.Reason, "owns no repository") {
			t.Fatalf("facts = %d reason = %q, want no fact and the no-owned-repository cause", len(result.Facts), result.Reason)
		}
	})
}
