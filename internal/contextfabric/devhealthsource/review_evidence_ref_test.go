package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// One review id on two pull requests of one repository (a provider that
// names an approval by its approver repeats the id on every pull request the
// approver approves) is two git_pull_request_reviews rows, keyed (org_id,
// repo_id, number, review_id). Each projected review and its
// BELONGS_TO_PULL_REQUEST edge cite a ref naming exactly its own row.
func TestReviewEvidenceRefNamesOneReviewRow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := append(baseTables(at), fakeTable{match: "FROM git_pull_request_reviews AS r", rows: [][]any{
		{"approval-42", "repo-1", uint32(1042), "APPROVED", at, "example-org/widget-service", at, uint8(0), zeroTime, "PR A"},
		{"approval-42", "repo-1", uint32(1043), "APPROVED", at, "example-org/widget-service", at, uint8(0), zeroTime, "PR B"},
	}})
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	want := map[string]string{
		"pull_request_review.v2:repo-1:1042:approval-42": "acr:v1:review.v2:repo-1:1042:approval-42",
		"pull_request_review.v2:repo-1:1043:approval-42": "acr:v1:review.v2:repo-1:1043:approval-42",
	}
	got := map[string][]string{}
	for _, entity := range batch.Entities {
		if entity.Subject.Kind == contractsv1.ContextFabricSubjectPullRequestReview {
			got[entity.Subject.CanonicalID] = append(got[entity.Subject.CanonicalID], entity.EvidenceRefIDs...)
		}
	}
	for _, relationship := range batch.Relationships {
		if relationship.Type == "BELONGS_TO_PULL_REQUEST" {
			got[relationship.From.CanonicalID] = append(got[relationship.From.CanonicalID], relationship.EvidenceRefIDs...)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("review subjects = %v, want %v", got, want)
	}
	for id, ref := range want {
		if refs := got[id]; len(refs) != 2 || refs[0] != ref || refs[1] != ref {
			t.Fatalf("%s cites %v (entity, then edge), want %q twice", id, refs, ref)
		}
	}
}
