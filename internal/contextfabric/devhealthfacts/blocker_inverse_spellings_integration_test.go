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

const blockerSpellingOrg = "3b8e5f21-6c4d-4a97-b0d2-9e1f7a8c5d34"

// The team blocker rollup counts a blocker stored under any spelling the
// graph projection maps onto a blocks edge, in either direction, once per
// pair. The same topology stored forward-only must give the same totals.
func TestTeamBlockerRollup_CountsEverySpellingOnce(t *testing.T) {
	ctx := context.Background()
	totals := func(spellings map[string][][4]string) (blocked, edges int64) {
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
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-a"), blockerSpellingOrg, "acme/repo-a", "github", at)
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			blockerSpellingOrg, "github", "team-spelling", repoUUID("repo-a"), "acme/repo-a", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
		for i, id := range []string{"blocker", "blocked-1", "blocked-2", "blocked-3", "blocked-4", "blocked-5"} {
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, last_synced, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, ?, '', 'linear', '', NULL)`,
				id, repoUUID("repo-a"), blockerSpellingOrg, "title "+id, at, at)
			if i > 0 {
				exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					repoUUID("repo-a"), id, uint32(i), float32(1), "native", "", at, blockerSpellingOrg)
				exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
					repoUUID("repo-a"), blockerSpellingOrg, uint32(i), "pr", "open", at, at, "feature", "")
			}
		}
		for _, rows := range spellings {
			for _, row := range rows {
				written := at
				if len(row) > 3 && row[3] == "ended" {
					written = at.Add(-24 * time.Hour)
				}
				exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
					blockerSpellingOrg, row[0], row[1], row[2], written)
			}
		}
		provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
		team := teamSubject("team-spelling")
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: blockerSpellingOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{team},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		fields := factFor(t, result, team).Fields
		for _, name := range []string{"blocked_work_items_current", "blocker_dependencies_current"} {
			if fields[name].Integer == nil {
				t.Fatalf("%s missing: %#v", name, fields[name])
			}
		}
		return *fields["blocked_work_items_current"].Integer, *fields["blocker_dependencies_current"].Integer
	}
	forwardBlocked, forwardEdges := totals(map[string][][4]string{"forward": {
		{"blocker", "blocked-1", "blocks", ""}, {"blocker", "blocked-2", "blocks", ""}, {"blocker", "blocked-3", "blocks", ""}, {"blocker", "blocked-4", "blocks", ""},
		{"blocker", "blocked-5", "blocks", "ended"},
	}})
	mixedBlocked, mixedEdges := totals(map[string][][4]string{"mixed": {
		{"blocker", "blocked-1", "blocks", ""},
		{"blocked-2", "blocker", "blocked_by", ""},
		{"blocked-3", "blocker", " IS_BLOCKED_BY ", ""},
		{"blocker", "blocked-4", "blocks", ""}, {"blocked-4", "blocker", "blocked_by", ""},
		{"blocked-5", "blocker", "blocked_by", "ended"},
	}})
	if forwardBlocked != mixedBlocked || forwardEdges != mixedEdges {
		t.Fatalf("mixed spellings: blocked=%d edges=%d, forward-only: blocked=%d edges=%d", mixedBlocked, mixedEdges, forwardBlocked, forwardEdges)
	}
	if mixedBlocked != 4 || mixedEdges != 4 {
		t.Fatalf("blocked=%d edges=%d, want 4 and 4", mixedBlocked, mixedEdges)
	}
}
