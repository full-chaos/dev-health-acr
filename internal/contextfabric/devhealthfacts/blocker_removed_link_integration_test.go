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

const removedLinkOrg = "5c1e8a47-2d96-4b03-9f7a-61d0e3b8c5a2"

type removedLinkEdge struct {
	blocker, blocked, raw string
	relationSynced        time.Duration
	blockerSynced         time.Duration
	blockedSynced         time.Duration
	startedDaysAgo        int
	counted               bool
}

// A blocks-link the provider removed leaves its row behind. It must be counted
// nowhere and must not name the oldest blocker age, whichever read serves it:
// the team and project rollups and the work item's own blockers.
func TestRemovedBlockerLinksAreNotCountedOnRealClickHouse(t *testing.T) {
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
	now := time.Now().UTC()
	at := now.Add(-72 * time.Hour).Truncate(time.Second)
	daysAgo := func(days int) time.Time { return now.Add(-time.Duration(days)*24*time.Hour - 3*time.Hour) }
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-rel"), removedLinkOrg, "acme/repo-rel", "github", at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-rel"), removedLinkOrg, uint32(1), "pr", "open", at, at, "feature", "")
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-rel", "team-rel", at, removedLinkOrg, "linear", uint8(1))
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		removedLinkOrg, "linear", "team-rel", repoUUID("repo-rel"), "acme/repo-rel", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)

	hour := time.Hour
	edges := []removedLinkEdge{
		{"x-live", "b-live", "blocks", 0, 0, 0, 5, true},
		{"x-gone", "b-gone", "blocks", -hour, 0, 0, 30, false},
		{"x-back", "b-back", "blocks", 0, 0, 0, 2, true},
		{"gh:acme/api#1", "gh:acme/api#2", "blocked by #1", -hour, 0, -hour, 3, true},
		{"gh:acme/api#3", "gh:acme/api#4", "blocked by #3", -hour, 0, 0, 50, false},
		{"gitlab:acme/api#5", "gitlab:acme/api#6", "blocks", -hour, 0, -hour, 4, true},
		{"gitlab:acme/api#7", "gitlab:acme/api#8", "blocks", -hour, 0, 0, 60, false},
	}
	counted := int64(0)
	for _, e := range edges {
		for _, item := range []struct {
			id     string
			synced time.Duration
		}{{e.blocker, e.blockerSynced}, {e.blocked, e.blockedSynced}} {
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, last_synced) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'github', '', NULL, ?)`,
				item.id, repoUUID("repo-rel"), removedLinkOrg, "title "+item.id, at, at.Add(item.synced))
		}
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID("repo-rel"), e.blocked, uint32(1), float32(1), "native", "", at, removedLinkOrg)
		if e.blocked == "b-back" {
			// The link was removed and later added again: the first row is
			// replaced by the newer one.
			exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				removedLinkOrg, e.blocker, e.blocked, "blocks", e.raw, at.Add(-2*hour), daysAgo(40))
		}
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			removedLinkOrg, e.blocker, e.blocked, "blocks", e.raw, at.Add(e.relationSynced), daysAgo(e.startedDaysAgo))
		if e.counted {
			counted++
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	principal := storage.Principal{OrgID: removedLinkOrg, RepositoryScopes: []string{"*"}}
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

	team := teamSubject("team-rel")
	fields := factFor(t, read(team), team).Fields
	wantInt(t, fields, "blocked_work_items_current", counted)
	wantInt(t, fields, "blocker_dependencies_current", counted)
	if _, served := fields["oldest_blocked_age_days"]; !served {
		t.Logf("age not served: reason=%+v basis=%+v", fields["oldest_blocked_age_unavailable_reason"], fields["oldest_blocked_age_basis"])
	}
	wantAgeDays(t, "oldest open blocker after the removed links", fields, 5)
	// The ended links read once and bound give the same answer as the end rule
	// kept inside each statement (the fallback above the bound).
	restoreBound := devhealthfacts.SetBlockerEndedLinksBoundForTest(0)
	inStatement := factFor(t, read(team), team).Fields
	restoreBound()
	for _, name := range []string{"blocked_work_items_current", "blocker_dependencies_current", "blocker_dependencies_excluded_by_status_current", "blocker_dependencies_writer_unresolved_current", "oldest_blocked_age_days"} {
		a, b := fields[name], inStatement[name]
		if (a.Integer == nil) != (b.Integer == nil) || (a.Integer != nil && *a.Integer != *b.Integer) {
			t.Errorf("%s differs between the bound ended links and the in-statement end rule: %+v vs %+v", name, a, b)
		}
	}
	// One kept link is a text relation whose writer cannot be told; it is
	// counted and flagged, never dropped silently.
	wantInt(t, fields, "blocker_dependencies_writer_unresolved_current", 1)

	for _, e := range edges {
		repoID := repoUUID("repo-rel")
		result := read(workItemSubject(repoID, e.blocked))
		got := 0
		for _, fact := range result.Facts {
			if v, ok := fact.Fields["blocked_by_work_item_id"]; ok && v.String != nil && *v.String == e.blocker {
				got++
			}
		}
		want := 0
		if e.counted {
			want = 1
		}
		if got != want {
			t.Errorf("work item %s blocked by %s: served %d times, want %d", e.blocked, e.blocker, got, want)
		}
	}
}

// The project rollup reads the same rule: a removed link is not a blocker of
// the project's issue, a re-added link is, and a link whose provider still
// reports it is unchanged.
func TestRemovedBlockerLinksAreNotCountedInTheProjectRollup(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	for _, item := range []struct{ id, project string }{
		{"i-live", "proj-p"}, {"i-gone", "proj-p"}, {"i-back", "proj-p"}, {"b-live", ""}, {"b-gone", ""}, {"b-back", ""},
	} {
		s.workItem(item.id, "repo-a", "issue", "open", item.project)
	}
	s.pullRequest("repo-a", 1, s.at, nil, nil)
	for _, issue := range []string{"i-live", "i-gone", "i-back"} {
		s.link("repo-a", issue, 1, "native")
	}
	removedAt := s.at.Add(-time.Hour)
	s.exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectRollupOrg, "b-live", "i-live", "blocks", "blocks", s.at, s.at.Add(-24*time.Hour))
	s.exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectRollupOrg, "b-gone", "i-gone", "blocks", "blocks", removedAt, s.at.Add(-24*time.Hour))
	s.exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectRollupOrg, "b-back", "i-back", "blocks", "blocks", removedAt.Add(-time.Hour), s.at.Add(-48*time.Hour))
	s.exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectRollupOrg, "b-back", "i-back", "blocks", "blocks", s.at, s.at.Add(-12*time.Hour))

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	project := projectRollupRef(t, "proj-p")
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{project},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
	}
	fields := factFor(t, result, project).Fields
	wantInt(t, fields, "blocked_work_items_current", 2)
	wantInt(t, fields, "blocker_dependencies_current", 2)
	wantInt(t, fields, "blocker_dependencies_writer_unresolved_current", 0)
}
