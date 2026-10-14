package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The team blocker totals read returns one row per team that has a blocked
// item, under the work-item reader's statement-level max_result_rows. A
// cohort at the cohort cap, every team with a blocked item, must still be
// served rather than fail the read.
const blockerTotalsBoundOrg = "3e9b6a02-71c4-4d85-b0f3-6a1d2c8e5f47"

const blockerTotalsBoundTeams = 250

func TestTeamBlockerTotalsServeACohortAtTheCohortCapOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
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
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', '', NULL)`,
		"blocker-open", repoUUID("repo-0"), blockerTotalsBoundOrg, "blocker", at)
	subjects := make([]contextfabric.SubjectRef, 0, blockerTotalsBoundTeams)
	for i := 0; i < blockerTotalsBoundTeams; i++ {
		repo := fmt.Sprintf("repo-%d", i)
		teamID := fmt.Sprintf("team-%d", i)
		itemID := fmt.Sprintf("blocked-%d", i)
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), blockerTotalsBoundOrg, "acme/"+repo, "github", at)
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			blockerTotalsBoundOrg, "github", teamID, repoUUID(repo), "acme/"+repo, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', '', NULL)`,
			itemID, repoUUID("repo-0"), blockerTotalsBoundOrg, "title "+itemID, at)
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(repo), itemID, uint32(1), float32(1), "native", "", at, blockerTotalsBoundOrg)
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(repo), blockerTotalsBoundOrg, uint32(1), "pr", "open", at, at, "feature", "")
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			blockerTotalsBoundOrg, "blocker-open", itemID, "blocks", at)
		subjects = append(subjects, teamSubject(teamID))
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerTotalsBoundOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts over %d team subjects: %v", blockerTotalsBoundTeams, err)
	}
	for _, subject := range []contextfabric.SubjectRef{subjects[0], subjects[blockerTotalsBoundTeams-1]} {
		wantInt(t, factFor(t, result, subject).Fields, "blocked_work_items_current", 1)
	}
}

const blockerTotalsMixedOrg = "9a4d2c71-5b08-4e36-a1f9-0c7e3d8b6254"

// 250 teams span two chunks of 200 and 50. Teams 0..199 mix repository-owning
// teams (even), project-only teams (odd) and, every tenth even team, a team
// that reaches its blocked item through both arms; teams 200..249 own no
// repository, so their chunk runs the project arm alone.
func TestTeamBlockerTotalsMixedOwnershipAcrossChunksOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
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
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-x"), blockerTotalsMixedOrg, "acme/repo-x", "github", at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', '', NULL)`,
		"blocker-open", repoUUID("repo-x"), blockerTotalsMixedOrg, "blocker", at)
	subjects := make([]contextfabric.SubjectRef, 0, blockerTotalsBoundTeams)
	for i := 0; i < blockerTotalsBoundTeams; i++ {
		teamID := fmt.Sprintf("team-%03d", i)
		itemID := fmt.Sprintf("blocked-%03d", i)
		project := fmt.Sprintf("proj-%03d", i)
		ownsRepo := i < 200 && i%2 == 0
		ownsProject := !ownsRepo || i%10 == 0
		prRepo := fmt.Sprintf("repo-r-%03d", i)
		if ownsRepo {
			prRepo = fmt.Sprintf("repo-o-%03d", i)
		}
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(prRepo), blockerTotalsMixedOrg, "acme/"+prRepo, "github", at)
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			teamID, teamID, at, blockerTotalsMixedOrg, "linear", uint8(1))
		if ownsRepo {
			exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				blockerTotalsMixedOrg, "linear", teamID, repoUUID(prRepo), "acme/"+prRepo, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
		}
		itemProject := ""
		if ownsProject {
			itemProject = project
			exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				project, blockerTotalsMixedOrg, "linear", nil, project, uint8(1), "active", "", at)
			exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				blockerTotalsMixedOrg, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
		}
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			itemID, repoUUID("repo-x"), blockerTotalsMixedOrg, "title "+itemID, at, itemProject)
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(prRepo), itemID, uint32(1), float32(1), "native", "", at, blockerTotalsMixedOrg)
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(prRepo), blockerTotalsMixedOrg, uint32(1), "pr", "open", at, at, "feature", "")
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			blockerTotalsMixedOrg, "blocker-open", itemID, "blocks", at)
		subjects = append(subjects, teamSubject(teamID))
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerTotalsMixedOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts over %d team subjects: %v", blockerTotalsBoundTeams, err)
	}
	// Each team has exactly one blocked item and one blocker edge, whichever
	// arm reaches it; a team reached through both arms is still one item.
	for _, subject := range subjects {
		fields := factFor(t, result, subject).Fields
		wantInt(t, fields, "blocked_work_items_current", 1)
		wantInt(t, fields, "blocker_dependencies_current", 1)
	}
}
