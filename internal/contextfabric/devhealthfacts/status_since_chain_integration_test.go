package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Two transitions of one item at one instant are ordered by the status chain,
// as the ops readers order them (ops internal/jobs/metrics/workitemmetrics/order.go,
// OrderTransitions): the one that leaves the prior status first, a chain head
// otherwise, string order of (from_status, to_status) with no chain. The last
// of the instant is the status the item entered. tie_ordinal keeps the rows
// apart in the table and is not an order; last_synced is not an order either.
func TestStatusSinceOrdersSameInstantTransitionsByTheStatusChainAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	instant := now.Add(-3 * time.Hour)
	repoID := "5b1c7e0e-2f56-4a53-9d0a-7a1f0c9d0121"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, orgID, "chain/"+repoID, "github", now); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	items := map[string]string{"c-chain": "done", "c-nochain": "zeta", "c-single": "in_progress"}
	var subjects []contextfabric.SubjectRef
	for id, status := range items {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repoID, orgID, "jira", "t", status, now, now, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		subjects = append(subjects, workItemSubject(repoID, id))
	}
	transition := func(id, from, to string, tie uint32, at, synced time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_transitions (repo_id, work_item_id, occurred_at, from_status, to_status, last_synced, org_id, tie_ordinal) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, id, at, from, to, synced, orgID, tie); err != nil {
			t.Fatalf("seed transition %s: %v", id, err)
		}
	}
	// A chain: open to in_progress, then in_progress to done. The first of the
	// chain has the later last_synced, so a pick by last_synced reads the
	// wrong end.
	transition("c-chain", "open", "in_progress", 0, instant, now)
	transition("c-chain", "in_progress", "done", 1, instant, now.Add(-time.Hour))
	// No chain: neither row enters the other's from_status. String order of
	// (from_status, to_status) puts the second row last.
	transition("c-nochain", "alpha", "yankee", 0, instant, now)
	transition("c-nochain", "beta", "zeta", 1, instant, now.Add(-time.Hour))
	// Control: one row at its instant.
	transition("c-single", "todo", "in_progress", 0, instant, now)

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
	for _, id := range []string{"c-chain", "c-nochain", "c-single"} {
		t.Run(id, func(t *testing.T) {
			fields := byLabel[id].Fields
			if basis := fields["status_since_basis"].String; basis == nil || *basis != "transition_observed" {
				t.Fatalf("basis = %+v, want transition_observed", fields["status_since_basis"])
			}
			if since := fields["status_since"].String; since == nil || *since != instant.Format(time.RFC3339) {
				t.Fatalf("status_since = %+v, want %s", fields["status_since"], instant.Format(time.RFC3339))
			}
		})
	}
}
