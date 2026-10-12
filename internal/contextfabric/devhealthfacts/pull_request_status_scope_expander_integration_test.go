package devhealthfacts_test

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestScopeExpanderReadsOnlyThePullRequestsHoldingTheStatusBeforeItsLimit
// adds, to the team fixture, 25 merged pull requests that sort before eight
// open ones on repo A. The expansion's limit (20) cuts the read in
// (repository, number) order, so a status applied after the read would see
// only merged pull requests; the statement carries the status and returns the
// ones that hold it, with the rule of contextfabric.PullRequestStatusMatches,
// and the review expansion reads only their reviews.
func TestScopeExpanderReadsOnlyThePullRequestsHoldingTheStatusBeforeItsLimit(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)
	pull := func(number uint32, state string) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			chaos4101RepoAID, chaos4099OrgID, number, "pull request", state, at, at, "branch", ""); err != nil {
			t.Fatalf("seed pull request %d: %v", number, err)
		}
	}
	review := func(id string, number uint32, state string, submitted time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, chaos4101RepoAID, chaos4099OrgID, number, state, submitted); err != nil {
			t.Fatalf("seed review %s: %v", id, err)
		}
	}
	for number := uint32(10); number < 35; number++ {
		pull(number, "merged")
	}
	for number := uint32(100); number <= 107; number++ {
		pull(number, "open")
	}
	review("review-104", 104, "APPROVED", at.Add(-2*time.Hour))
	review("review-105-approved", 105, "APPROVED", at.Add(-3*time.Hour))
	review("review-105-changes", 105, "CHANGES_REQUESTED", at.Add(-2*time.Hour))
	review("review-106", 106, "COMMENTED", at.Add(-2*time.Hour))
	review("review-107", 107, "PENDING", at.Add(-2*time.Hour))
	review("review-tie-approved", 103, "APPROVED", at.Add(-time.Hour))
	review("review-tie-changes", 103, "CHANGES_REQUESTED", at.Add(-time.Hour))

	expand := func(t *testing.T, policy contextfabric.FactScopePolicy, target contextfabric.SubjectKind, status string) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:         storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4101RepoASlug}},
			RequirementKind:   contextfabric.FactPullRequests,
			Origins:           []contextfabric.SubjectRef{chaos4101TeamSubject()},
			Policy:            policy,
			TargetKind:        target,
			PullRequestStatus: status,
			Limit:             20,
		})
		if err != nil {
			t.Fatalf("ExpandFactScope: %v", err)
		}
		return result
	}
	numbers := func(result contextfabric.FactScopeExpansionResult) []string {
		out := []string{}
		for _, target := range result.Targets {
			out = append(out, target.CanonicalID)
		}
		sort.Strings(out)
		return out
	}
	pr := func(ns ...int) []string {
		out := []string{}
		for _, n := range ns {
			out = append(out, "pull_request:"+chaos4101RepoAID+":"+strconv.Itoa(n))
		}
		sort.Strings(out)
		return out
	}
	// Pull request 1 (open) carries the fixture's lower-case "approved"
	// review: approved, not awaiting a review.
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{"open", pr(1, 100, 101, 102, 103, 104, 105, 106, 107)},
		{"awaiting_review", pr(100, 101, 102, 107)},
		{"approved", pr(1, 104)},
		{"changes_requested", pr(105)},
	} {
		t.Run(tc.status, func(t *testing.T) {
			result := expand(t, contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequest, contextfabric.SubjectPullRequest, tc.status)
			if got := numbers(result); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
			if result.Counts.Truncated {
				t.Fatalf("a status read under its limit reported truncation")
			}
		})
	}
	t.Run("merged is cut at the limit", func(t *testing.T) {
		result := expand(t, contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequest, contextfabric.SubjectPullRequest, "merged")
		if len(result.Targets) != 21 {
			t.Fatalf("targets = %d, want the limit plus its overflow row (21) for the resolver to cut", len(result.Targets))
		}
	})
	t.Run("reviews of the pull requests holding the status", func(t *testing.T) {
		result := expand(t, contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequestReview, contextfabric.SubjectKind(contractsv1.ContextFabricSubjectPullRequestReview), "changes_requested")
		if len(result.Targets) != 2 {
			t.Fatalf("review targets = %+v, want the two reviews of pull request 105", result.Targets)
		}
	})
}
