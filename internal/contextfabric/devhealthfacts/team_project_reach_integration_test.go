package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team reaches work through the repositories it owns AND through the
// projects it owns: a project reaches repositories only through its issues'
// linked pull request rows. Every row below is seeded so that reading the
// owned repositories alone gives a different answer.
const projectReachOrg = "3f6e2d18-95a4-4c07-b1d2-8a40e7c91b55"

func TestTeamRollups_ReachWorkThroughOwnedProjectLinks(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership",
		"work_item_team_attributions", "deploy_metrics_daily",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, label := range []string{"repo-a", "repo-r", "repo-x"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), projectReachOrg, "acme/"+label, "github", at)
	}
	for _, project := range []string{"proj-1", "proj-2"} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, projectReachOrg, "linear", nil, project, uint8(1), "active", "", at)
	}
	for _, team := range []string{"team-both", "team-project-only", "team-ended"} {
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, projectReachOrg, "linear", uint8(1))
		// team-ended's ownership of proj-1 ended inside the past window below.
		var validTo any
		if team == "team-ended" {
			validTo = ts(2026, 6, 20, 0, 0, 0)
		}
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			projectReachOrg, "linear", team, "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), validTo, at)
	}
	// team-both also owns repo-a directly; team-project-only owns no repository.
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectReachOrg, "linear", "team-both", repoUUID("repo-a"), "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)

	item := func(id, project string) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			id, repoUUID("repo-x"), projectReachOrg, "title "+id, at, project)
	}
	link := func(id, prRepo string, number uint32, tier string) {
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(prRepo), id, number, float32(1), tier, "", at, projectReachOrg)
	}
	pullRequest := func(repo string, number uint32) {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(repo), projectReachOrg, number, "pr", "open", at, at, "feature", "")
	}
	blocks := func(source, target string) {
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			projectReachOrg, source, target, "blocks", at)
	}
	item("i-own", "proj-1")   // linked into owned repo-a AND in an owned project
	item("i-reach", "proj-1") // linked only into repo-r, which no team owns
	item("i-weak", "proj-1")  // heuristic link into repo-r
	item("i-other", "proj-2") // a project no team owns, linked into repo-r
	item("blocker", "")       // the blocking item, in no project
	link("i-own", "repo-a", 1, "native")
	link("i-reach", "repo-r", 2, "native")
	link("i-weak", "repo-r", 3, "heuristic")
	link("i-other", "repo-r", 4, "native")
	link("i-stale", "repo-r", 99, "native")
	link("i-hist", "repo-r", 30, "native")
	link("pr-typed", "repo-r", 2, "native")
	// repo-a: #1 linked, #10 not linked (owned: both count). repo-r: #2, #3
	// linked to owned-project issues, #4 linked to another project's issue,
	// #20 unlinked (reached: only #2 and #3 count).
	for _, n := range []struct {
		repo   string
		number uint32
	}{{"repo-a", 1}, {"repo-a", 10}, {"repo-r", 2}, {"repo-r", 3}, {"repo-r", 4}, {"repo-r", 20}} {
		pullRequest(n.repo, n.number)
	}
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-r"), projectReachOrg, uint32(30), "pr", "open", at, ts(2026, 6, 10, 0, 0, 0), "feature", "")
	for _, target := range []string{"i-own", "i-reach", "i-weak", "i-other", "i-stale", "pr-typed"} {
		blocks("blocker", target)
	}

	providers := devhealthfacts.NewProviders(query)
	principal := storage.Principal{OrgID: projectReachOrg, RepositoryScopes: []string{"*"}}
	window := contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59))
	read := func(kind contextfabric.FactKind, team string) contextfabric.FactProviderResult {
		t.Helper()
		result, err := findProvider(t, providers, kind).ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time: window, Kind: kind, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("%s ReadFacts: %v", kind, err)
		}
		return result
	}
	str := func(fields map[string]contextfabric.FactValue, name string) string {
		if v, ok := fields[name]; ok && v.String != nil {
			return *v.String
		}
		return ""
	}

	t.Run("pull requests: owned repositories whole, reached repositories by linked pull request", func(t *testing.T) {
		for _, team := range []string{"team-both", "team-project-only"} {
			fields := factFor(t, read(contextfabric.FactPullRequests, team), teamSubject(team)).Fields
			wantInt(t, fields, "project_reached_repository_count", map[string]int64{"team-both": 1, "team-project-only": 2}[team])
		}
		both := factFor(t, read(contextfabric.FactPullRequests, "team-both"), teamSubject("team-both")).Fields
		// repo-a whole (#1, #10) plus repo-r's linked #2 and #3 only.
		wantInt(t, both, "pull_requests_opened_window", 4)
		rows := wantTableRows(t, both, "repository_breakdown")
		wantInt(t, rows[repoUUID("repo-a")], "pull_requests_opened_window", 2)
		wantInt(t, rows[repoUUID("repo-r")], "pull_requests_opened_window", 2)
		if got := str(rows[repoUUID("repo-r")], "reach_basis"); got != "owned_project_link" {
			t.Fatalf("repo-r reach_basis = %q, want owned_project_link", got)
		}
		if got := str(rows[repoUUID("repo-a")], "reach_basis"); got != "owned_repository" {
			t.Fatalf("repo-a reach_basis = %q, want owned_repository", got)
		}
		// repo-r is reached by a native and a heuristic link: named at its weakest.
		if got := str(rows[repoUUID("repo-r")], "link_tier"); got != "heuristic" {
			t.Fatalf("repo-r link_tier = %q, want heuristic", got)
		}
		if got := str(both, "repository_link_basis"); got != "heuristic" {
			t.Fatalf("repository_link_basis = %q, want heuristic", got)
		}
		// A team that owns only the project reaches repo-a and repo-r through it.
		only := factFor(t, read(contextfabric.FactPullRequests, "team-project-only"), teamSubject("team-project-only")).Fields
		wantInt(t, only, "owned_repository_count", 0)
		wantInt(t, only, "pull_requests_opened_window", 3)
	})

	t.Run("blockers: an item in an owned repository and an owned project counts once", func(t *testing.T) {
		both := factFor(t, read(contextfabric.FactBlockers, "team-both"), teamSubject("team-both")).Fields
		// i-own (both arms), i-reach, i-weak. i-other is in no owned project.
		wantInt(t, both, "blocked_work_items_current", 3)
		wantInt(t, both, "blocker_dependencies_current", 3)
		wantInt(t, both, "project_blocked_work_items_current", 3)
		if got := str(both, "repository_link_basis"); got != "heuristic" {
			t.Fatalf("repository_link_basis = %q, want heuristic", got)
		}
		only := factFor(t, read(contextfabric.FactBlockers, "team-project-only"), teamSubject("team-project-only")).Fields
		wantInt(t, only, "owned_repository_count", 0)
		wantInt(t, only, "blocked_work_items_current", 3)
		// The stale link (no such pull request) and the pull-request-typed item
		// are outside the admitted links: not counted, and the breakdown
		// limitation is stated beside the project items.
		if got := str(both, "repository_breakdown_limitation"); got == "" {
			t.Fatalf("repository_breakdown_limitation missing beside project_blocked_work_items_current")
		}
	})

	t.Run("pull requests: a past window keeps project ownership that ended inside it", func(t *testing.T) {
		result, err := findProvider(t, providers, contextfabric.FactPullRequests).ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time: pastRangeWindow(), Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{teamSubject("team-ended")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		fields := factFor(t, result, teamSubject("team-ended")).Fields
		wantInt(t, fields, "pull_requests_opened_window", 1)
		wantInt(t, fields, "project_reached_repository_count", 2)
	})

	t.Run("deployments: project-reached repositories contribute no repo-level rows, named", func(t *testing.T) {
		result := read(contextfabric.FactDeployments, "team-project-only")
		if result.State != contextfabric.SourceNotApplicable {
			t.Fatalf("state = %q, want not_applicable (reason %q)", result.State, result.Reason)
		}
		if !strings.Contains(result.Reason, "reached through owned projects") || !strings.Contains(result.Reason, "2 repositories") {
			t.Fatalf("reason = %q, want the project-reached repositories named", result.Reason)
		}
	})
}
