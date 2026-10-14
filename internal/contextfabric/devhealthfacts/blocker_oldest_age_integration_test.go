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

const blockerAgeOrg = "3b7d9e41-6a2c-4f58-8d13-0c9e5a7b2f64"

func wantAgeString(t *testing.T, label string, fields map[string]contextfabric.FactValue, name, want string) {
	t.Helper()
	value, ok := fields[name]
	if want == "" {
		if ok {
			t.Errorf("%s: %s is served, want it absent", label, name)
		}
		return
	}
	if !ok || value.String == nil || *value.String != want {
		t.Errorf("%s: %s = %+v, want %q", label, name, value, want)
	}
}

func wantAgeDays(t *testing.T, label string, fields map[string]contextfabric.FactValue, want int64) {
	t.Helper()
	value, ok := fields["oldest_blocked_age_days"]
	if want < 0 {
		if ok {
			t.Errorf("%s: oldest_blocked_age_days is served (%+v), want it absent, never zero", label, value)
		}
		return
	}
	if !ok || value.Integer == nil || *value.Integer != want {
		t.Errorf("%s: oldest_blocked_age_days = %+v, want %d", label, value, want)
	}
}

// The oldest open blocker edge of a team is served in whole days from the
// provider's own time of the link, else from the first sync that saw it; an
// edge with neither start never turns into an age of zero, and a caller who
// cannot read an edge's blocker is served no age from it.
func TestTeamBlockerOldestAgeOnRealClickHouse(t *testing.T) {
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
	now := time.Now().UTC()
	ago := func(days int, extra time.Duration) *time.Time {
		moment := now.Add(-time.Duration(days)*24*time.Hour - extra)
		return &moment
	}
	for _, repo := range []string{"repo-exact", "repo-seen", "repo-mixed", "repo-nostart", "repo-closed", "repo-secret", "repo-secret-owner"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), blockerAgeOrg, "acme/"+repo, "github", at)
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(repo), blockerAgeOrg, uint32(1), "pr", "open", at, at, "feature", "")
	}
	type edge struct {
		team, repo, blocker, blocked, blockerRepo, blockerStatus string
		startedAt, firstSeen                                     *time.Time
	}
	edges := []edge{
		{"team-exact", "repo-exact", "exact-blocker", "exact-blocked", "repo-exact", "open", ago(12, 3*time.Hour), nil},
		{"team-seen", "repo-seen", "seen-blocker", "seen-blocked", "repo-seen", "open", nil, ago(5, 2*time.Hour)},
		{"team-mixed", "repo-mixed", "mixed-blocker-known", "mixed-blocked-known", "repo-mixed", "open", nil, ago(3, time.Hour)},
		{"team-mixed", "repo-mixed", "mixed-blocker-unknown", "mixed-blocked-unknown", "repo-mixed", "open", nil, nil},
		{"team-nostart", "repo-nostart", "nostart-blocker", "nostart-blocked", "repo-nostart", "open", nil, nil},
		{"team-closed", "repo-closed", "closed-blocker", "closed-blocked", "repo-closed", "done", ago(30, time.Hour), nil},
		{"team-secret", "repo-secret-owner", "secret-blocker", "secret-blocked", "repo-secret", "open", ago(20, time.Hour), nil},
		{"team-secret", "repo-secret-owner", "kept-blocker", "kept-blocked", "repo-secret-owner", "open", nil, nil},
	}
	owned := map[string]bool{}
	for _, e := range edges {
		if !owned[e.team] {
			owned[e.team] = true
			exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
				e.team, e.team, at, blockerAgeOrg, "linear", uint8(1))
			exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				blockerAgeOrg, "linear", e.team, repoUUID(e.repo), "acme/"+e.repo, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
		}
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, ?, '', ?, '', 'github', '', NULL)`,
			e.blocker, repoUUID(e.blockerRepo), blockerAgeOrg, "title "+e.blocker, e.blockerStatus, at)
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'github', '', NULL)`,
			e.blocked, repoUUID(e.repo), blockerAgeOrg, "title "+e.blocked, at)
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(e.repo), e.blocked, uint32(1), float32(1), "native", "", at, blockerAgeOrg)
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			blockerAgeOrg, e.blocker, e.blocked, "blocks", "blocks", at, e.startedAt)
		if e.firstSeen != nil {
			exec(`INSERT INTO work_item_dependency_first_seen (org_id, source_work_item_id, target_work_item_id, relationship_type, first_seen_at) VALUES (?, ?, ?, ?, ?)`,
				blockerAgeOrg, e.blocker, e.blocked, "blocks", *e.firstSeen)
			exec(`INSERT INTO work_item_dependency_first_seen (org_id, source_work_item_id, target_work_item_id, relationship_type, first_seen_at) VALUES (?, ?, ?, ?, ?)`,
				blockerAgeOrg, e.blocker, e.blocked, "blocks", e.firstSeen.Add(48*time.Hour))
		}
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	read := func(principal storage.Principal, team string) map[string]contextfabric.FactValue {
		t.Helper()
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("ReadFacts %s: %v: %s", team, err, readFailureCause(err))
		}
		return factFor(t, result, teamSubject(team)).Fields
	}
	everyone := storage.Principal{OrgID: blockerAgeOrg, RepositoryScopes: []string{"*"}}

	fields := read(everyone, "team-exact")
	wantAgeDays(t, "provider time", fields, 12)
	wantAgeString(t, "provider time", fields, "oldest_blocked_age_basis", "exact")

	fields = read(everyone, "team-seen")
	wantAgeDays(t, "first sync", fields, 5)
	wantAgeString(t, "first sync", fields, "oldest_blocked_age_basis", "lower_bound")

	fields = read(everyone, "team-mixed")
	wantAgeDays(t, "one edge without a start", fields, 3)
	wantAgeString(t, "one edge without a start", fields, "oldest_blocked_age_basis", "lower_bound")
	wantAgeString(t, "one edge without a start", fields, "oldest_blocked_age_unavailable_reason", "")

	fields = read(everyone, "team-nostart")
	wantAgeDays(t, "no start", fields, -1)
	wantAgeString(t, "no start", fields, "oldest_blocked_age_basis", "")
	wantAgeString(t, "no start", fields, "oldest_blocked_age_unavailable_reason", "no_relation_start")

	fields = read(everyone, "team-closed")
	wantAgeDays(t, "closed blocker", fields, -1)
	wantAgeString(t, "closed blocker", fields, "oldest_blocked_age_unavailable_reason", "none_open")

	fields = read(everyone, "team-secret")
	wantAgeDays(t, "unrestricted caller", fields, 20)
	restricted := storage.Principal{OrgID: blockerAgeOrg, RepositoryScopes: []string{"acme/repo-secret-owner"}}
	fields = read(restricted, "team-secret")
	wantAgeDays(t, "restricted caller", fields, -1)
	wantAgeString(t, "restricted caller", fields, "oldest_blocked_age_unavailable_reason", "no_relation_start")
}
