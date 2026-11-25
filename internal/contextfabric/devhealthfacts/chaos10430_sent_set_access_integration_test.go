package devhealthfacts_test

import (
	"context"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

const (
	sentSetCallerOrg = "5b7e0c2a-91d4-4f36-8a1e-3c6f9d2b7e40"
	sentSetOtherOrg  = "8d3f1a6e-24c7-4b95-9e02-7a5c1f8b3d61"
)

// The team pull request reads join a set the read built itself from the
// caller organization's link rows, but the set is not the authorization: each
// statement keeps its own org_id predicate on the pull requests it counts, and
// the window and state counts keep the project-reached repository prune. A sent
// pull request of another organization, or outside the reached repositories,
// counts nowhere.
func TestCHAOS10430SentLinkedPullRequestsCountOnlyInTheCallerOrganizationOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("repos", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 11, 1, 0, 0, 0)
	pr := func(org, repoLabel string, number uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(repoLabel), org, number, "pr", "open", at, at, "feature", ""); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	pr(sentSetCallerOrg, "repo-caller", 8)
	pr(sentSetOtherOrg, "repo-other", 7)
	// The other organization holds a repos row under the caller's repository
	// id: the reached read names a repository only from the caller's rows.
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
		repoUUID("repo-caller"), sentSetOtherOrg, "acme/other-org-name", "github", at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	const team = "team-caller"
	callerRow := devhealthfacts.TeamLinkedPullRequestForTest{Team: team, Repo: repoUUID("repo-caller"), Number: "8", Rank: 3}
	otherRow := devhealthfacts.TeamLinkedPullRequestForTest{Team: team, Repo: repoUUID("repo-other"), Number: "7", Rank: 3}
	for _, c := range []struct {
		name         string
		sent         []devhealthfacts.TeamLinkedPullRequestForTest
		reach        []string
		reached      []string
		counts, sets int
	}{
		{"control_own_pull_request_counts", []devhealthfacts.TeamLinkedPullRequestForTest{callerRow}, []string{repoUUID("repo-caller")}, []string{""}, 1, 1},
		{"cross_org_pull_request_counts_nowhere", []devhealthfacts.TeamLinkedPullRequestForTest{otherRow}, []string{repoUUID("repo-other")}, nil, 0, 0},
		{"outside_reach_pull_request_not_counted", []devhealthfacts.TeamLinkedPullRequestForTest{callerRow}, []string{repoUUID("repo-other")}, []string{""}, 0, 0},
		{"unrequested_team_row_counts_nowhere", []devhealthfacts.TeamLinkedPullRequestForTest{{Team: "team-unrequested", Repo: repoUUID("repo-caller"), Number: "8", Rank: 3}}, []string{repoUUID("repo-caller")}, nil, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			reached, counts, states, err := devhealthfacts.TeamLinkedPullRequestReadsForTest(ctx, query, sentSetCallerOrg, []string{team}, c.sent, c.reach)
			if err != nil {
				t.Fatal(err)
			}
			// A reached repository's name is "" here: only the other
			// organization holds a repos row under its id.
			if !slices.Equal(reached, c.reached) || counts != c.counts || states != c.sets {
				t.Errorf("WANT reached names %q, window counts %d, state counts %d rows; got %q %d %d", c.reached, c.counts, c.sets, reached, counts, states)
			}
		})
	}
}
