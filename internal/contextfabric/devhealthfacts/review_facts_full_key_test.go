package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A review id recurs across one repository's pull requests (one provider
// names an approval by its approver). A review subject reads, and is
// answered from, exactly its own git_pull_request_reviews row: the key it
// sends carries the pull request number, a row of another pull request with
// the same review id is never its fact, and its evidence names its own row.
func TestReviewFactsReadTheSubjectsOwnPullRequest(t *testing.T) {
	t.Parallel()
	review1042, _, err := identity.Derive(identity.KindPullRequestReview, []string{"repo-1", "1042", "approval-42"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM git_pull_request_reviews", rows: [][]any{
		{"approval-42", "CHANGES_REQUESTED", "repo-1", uint32(1043)},
		{"approval-42", "APPROVED", "repo-1", uint32(1042)},
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReviews)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactReviews,
		Subjects: []contextfabric.SubjectRef{{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: review1042}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if ids := client.idsBinding(); len(ids) != 1 || ids[0] != "repo-1:1042:approval-42" {
		t.Fatalf("ids binding = %q, want [repo-1:1042:approval-42]", ids)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly the subject's own review", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Subject.CanonicalID != review1042 || fact.Fields["state"].String == nil || *fact.Fields["state"].String != "APPROVED" {
		t.Fatalf("fact = %#v, want PR 1042's APPROVED review", fact)
	}
	if len(fact.EvidenceRefIDs) != 1 || fact.EvidenceRefIDs[0] != "acr:v1:review.v2:repo-1:1042:approval-42" {
		t.Fatalf("evidence = %q", fact.EvidenceRefIDs)
	}
}
