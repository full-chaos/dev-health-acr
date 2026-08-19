package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func pullRequestSubject(repoID string, number string) contextfabric.SubjectRef {
	id := "pull_request:" + repoID + ":" + number
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: id, Label: id}
}

// reviewSubject mints a CHAOS-3898 "pull_request_review.v2:<repo_id>:<number>:<review_id>"
// subject via identity.Derive, mirroring workItemSubject's rationale
// (identity_test.go). The pull request number is fixed at "1" -- v2Index
// (shared.go) only ever recovers the FIRST segment (repo_id) and the LAST
// segment (review_id) for this kind's devhealthfacts lookups, so the
// number segment's exact value is inert here.
func reviewSubject(repoID, reviewID string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{repoID, "1", reviewID}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("reviewSubject(%q, %q): identity.Derive failed: omitted=%v err=%v", repoID, reviewID, omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonicalID, Label: reviewID}
}

func TestPullRequestsProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		// uint32, matching the production column type -- an int64 fixture here
		// is what let the reader's own int64 scan pass for so long.
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(1042), "open"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{pullRequestSubject("repo-1", "1042")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	if result.Facts[0].Fields["state"].String == nil || *result.Facts[0].Fields["state"].String != "open" {
		t.Fatalf("fields = %#v", result.Facts[0].Fields)
	}
}

func TestPullRequestsProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_requests", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{pullRequestSubject("repo-1", "1042")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestPullRequestsProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_requests", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{pullRequestSubject("repo-1", "1042")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestPullRequestsProviderOrgScoped(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_requests", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-3"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{pullRequestSubject("repo-1", "1042")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-3" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1:1042" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
}

func TestReviewsProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_request_reviews", rows: [][]any{{"review-1", "approved", "repo-1"}}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReviews)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReviews, Subjects: []contextfabric.SubjectRef{reviewSubject("repo-1", "review-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Fields["state"].String == nil || *result.Facts[0].Fields["state"].String != "approved" {
		t.Fatalf("facts = %#v", result.Facts)
	}
}

func TestReviewsProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_request_reviews", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReviews)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReviews, Subjects: []contextfabric.SubjectRef{reviewSubject("repo-1", "review-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestReviewsProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_request_reviews", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReviews)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReviews, Subjects: []contextfabric.SubjectRef{reviewSubject("repo-1", "review-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}
