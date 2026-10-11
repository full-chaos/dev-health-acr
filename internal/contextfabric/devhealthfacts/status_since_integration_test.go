package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// The time-in-state fields read through the production provider against real
// ClickHouse: a re-entered status counts from its latest entry, an item with no
// transition and an item whose latest transition went elsewhere are null with
// the no-transition basis, and a transition of the same work item id in another
// repository or another organization is never used.
func TestStatusSinceAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	repoID := "5b1c7e0e-2f56-4a53-9d0a-7a1f0c9d0101"
	otherRepoID := "5b1c7e0e-2f56-4a53-9d0a-7a1f0c9d0102"
	for _, id := range []string{repoID, otherRepoID} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, id, orgID, "since/"+id, "github", now); err != nil {
			t.Fatalf("seed repository: %v", err)
		}
	}
	items := map[string]string{"s-reentered": "in_progress", "s-none": "todo", "s-elsewhere": "in_progress", "s-foreign": "in_progress"}
	var subjects []contextfabric.SubjectRef
	for id, status := range items {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repoID, orgID, "jira", "t", status, now, now, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		subjects = append(subjects, workItemSubject(repoID, id))
	}
	transition := func(org, repo, id, _, to string, at time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_transitions (repo_id, work_item_id, occurred_at, to_status, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
			repo, id, at, to, now, org); err != nil {
			t.Fatalf("seed transition %s: %v", id, err)
		}
	}
	transition(orgID, repoID, "s-reentered", "todo", "in_progress", now.Add(-72*time.Hour))
	transition(orgID, repoID, "s-reentered", "in_progress", "blocked", now.Add(-48*time.Hour))
	transition(orgID, repoID, "s-reentered", "blocked", "in_progress", now.Add(-2*time.Hour))
	transition(orgID, repoID, "s-elsewhere", "todo", "in_progress", now.Add(-5*time.Hour))
	transition(orgID, repoID, "s-elsewhere", "in_progress", "done", now.Add(-1*time.Hour))
	transition(orgID, otherRepoID, "s-foreign", "todo", "in_progress", now.Add(-9*time.Hour))
	transition("another-org", repoID, "s-foreign", "todo", "in_progress", now.Add(-9*time.Hour))

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactStatus)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactStatus, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != len(items) {
		t.Fatalf("facts = %d, want %d (state %s)", len(result.Facts), len(items), result.State)
	}
	byLabel := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byLabel[fact.Subject.Label] = fact
	}
	re := byLabel["s-reentered"].Fields
	if b := re["status_since_basis"].String; b == nil || *b != "transition_observed" {
		t.Fatalf("s-reentered basis = %+v", re["status_since_basis"])
	}
	secs := re["status_duration_seconds"].Integer
	if secs == nil || *secs < int64((2*time.Hour).Seconds()) || *secs > int64((2*time.Hour+10*time.Minute).Seconds()) {
		t.Fatalf("s-reentered duration = %v, want about 2h from the latest entry", secs)
	}
	for id, want := range map[string]string{"s-none": "no_transition_recorded", "s-elsewhere": "transition_inconsistent", "s-foreign": "no_transition_recorded"} {
		f := byLabel[id].Fields
		if b := f["status_since_basis"].String; b == nil || *b != want || !f["status_since"].Null || !f["status_duration_seconds"].Null {
			t.Fatalf("%s: fields = %+v, want null since/duration with no_transition_recorded", id, f)
		}
	}
}

// The statement runs unchanged against the server for the two scope shapes,
// and the raw server error is the failure text: the provider path reports a
// sanitized failure, which would hide a malformed statement.
func TestStatusSinceStatementRunsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	scopes := map[string]readers.AuthorizationScope{
		"zero":     {},
		"ids":      {GrantedRepositoryIDs: []string{"5b1c7e0e-2f56-4a53-9d0a-7a1f0c9d0101"}},
		"selector": {RepositorySelectors: &readers.RepositorySelectorScope{Granted: readers.RepositorySelectorSet{ExactSlugs: []string{"acme/a"}}}},
	}
	for name, scope := range scopes {
		sql, bindings := devhealthfacts.StatusSinceStatementForTest(scope, readers.Settings{})
		args := []any{clickhousedriver.Named("org_id", "org-direct"), clickhousedriver.Named("ids", []string{"5b1c7e0e-2f56-4a53-9d0a-7a1f0c9d0101:w-1"})}
		for _, b := range bindings {
			args = append(args, clickhousedriver.Named(b.Name, b.Value))
		}
		rows, err := direct.Query(ctx, sql, args...)
		if err != nil {
			t.Fatalf("%s: server rejected the statement: %v\n%s", name, err, strings.TrimSpace(sql))
		}
		_ = rows.Close()
	}
}
