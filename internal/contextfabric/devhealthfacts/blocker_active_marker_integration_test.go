package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const activeMarkerOrg = "b7e20c4d-5a18-4f6e-8c93-0d2a41e7f3b6"

// A work item's blocker facts name whether each link is in the team's blocker
// count. Two of the three links here are excluded from that count by the
// blocker's status; the item facts must say so, and the active ones must add up
// to the team's current count.
func TestBlockerFactsMarkLinksTheTeamCountExcludesOnRealClickHouse(t *testing.T) {
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
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	repo := repoUUID("repo-active")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo, activeMarkerOrg, "acme/repo-active", "github", at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repo, activeMarkerOrg, uint32(1), "pr", "open", at, at, "feature", "")
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-active", "team-active", at, activeMarkerOrg, "linear", uint8(1))
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		activeMarkerOrg, "linear", "team-active", repo, "acme/repo-active", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)

	links := []struct {
		blocker, blocked, blockerStatus string
		active                          bool
	}{
		{"x-open", "b-open", "open", true},
		{"x-done", "b-done", "done", false},
		{"x-canceled", "b-canceled", "canceled", false},
	}
	for _, l := range links {
		for _, item := range []struct{ id, status string }{{l.blocker, l.blockerStatus}, {l.blocked, "open"}} {
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, last_synced) VALUES (?, ?, ?, ?, ?, '', ?, '', 'github', '', NULL, ?)`,
				item.id, repo, activeMarkerOrg, "title "+item.id, item.status, at, at)
		}
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repo, l.blocked, uint32(1), float32(1), "native", "", at, activeMarkerOrg)
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			activeMarkerOrg, l.blocker, l.blocked, "blocks", "blocks", at, at)
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	principal := storage.Principal{OrgID: activeMarkerOrg, RepositoryScopes: []string{"*"}}
	read := func(subject contextfabric.SubjectRef) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
		}
		return result
	}

	team := teamSubject("team-active")
	teamFields := factFor(t, read(team), team).Fields
	wantInt(t, teamFields, "blocker_dependencies_current", 1)
	wantInt(t, teamFields, "blocker_dependencies_excluded_by_status_current", 2)

	var active, excluded int64
	for _, l := range links {
		result := read(workItemSubject(repo, l.blocked))
		if len(result.Facts) != 1 {
			t.Fatalf("%s: facts = %d, want 1", l.blocked, len(result.Facts))
		}
		fields := result.Facts[0].Fields
		marker := fields["blocker_active"].Boolean
		if marker == nil || *marker != l.active {
			t.Errorf("%s (blocker %s): blocker_active = %+v, want %v", l.blocked, l.blockerStatus, fields["blocker_active"], l.active)
		}
		if basis := fields["blocker_status_basis"].String; basis == nil || *basis != "open" {
			t.Errorf("%s: blocker_status_basis = %+v, want open", l.blocked, fields["blocker_status_basis"])
		}
		if l.active {
			active++
		} else {
			excluded++
		}
	}
	if active != 1 || excluded != 2 {
		t.Fatalf("item facts mark %d active and %d excluded links; the team counts say 1 and 2", active, excluded)
	}
}
