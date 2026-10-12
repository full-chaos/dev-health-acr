package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// One approver's review id on two merge requests of one repository, read by
// ClickHouse through the production reviews provider and its reader: a
// review subject is answered with exactly its own row, never the other
// merge request's.
func TestReviewFactsReadTheAskedReviewAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	// devhealthschema:not-a-production-replica the table name passed to devhealthschema.DDL selects what to render; the schema is the declaration's
	for _, statement := range devhealthschema.DDL("git_pull_request_reviews") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-review-full-key"
	const repoID = "40000000-0000-4000-8000-000000000004"
	at := ts(2026, 8, 1, 12, 0, 0)
	// devhealthschema:not-a-production-replica rows are inserted into tables whose schema comes only from devhealthschema.DDL, never defined here
	for _, row := range []struct {
		number uint32
		state  string
	}{{1042, "APPROVED"}, {1043, "CHANGES_REQUESTED"}} {
		if err := direct.Exec(ctx, `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"approval-42", repoID, orgID, row.number, row.state, at, at); err != nil {
			t.Fatalf("seed review on %d: %v", row.number, err)
		}
	}
	subject := func(number string) contextfabric.SubjectRef {
		canonical, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{repoID, number, "approval-42"}, nil)
		if err != nil || omitted {
			t.Fatalf("identity.Derive: omitted=%v err=%v", omitted, err)
		}
		return contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonical}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactReviews)
	for _, tc := range []struct{ number, state string }{{"1042", "APPROVED"}, {"1043", "CHANGES_REQUESTED"}} {
		asked := subject(tc.number)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactReviews,
			Subjects: []contextfabric.SubjectRef{asked},
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s) error = %v", tc.number, err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("review on %s: %d facts %#v, want exactly the asked review", tc.number, len(result.Facts), result.Facts)
		}
		fact := result.Facts[0]
		if fact.Subject.CanonicalID != asked.CanonicalID || fact.Fields["state"].String == nil || *fact.Fields["state"].String != tc.state {
			t.Fatalf("review on %s: fact %#v, want state %s", tc.number, fact, tc.state)
		}
		if want := "acr:v1:review.v2:" + repoID + ":" + tc.number + ":approval-42"; len(fact.EvidenceRefIDs) != 1 || fact.EvidenceRefIDs[0] != want {
			t.Fatalf("review on %s: evidence %q, want %q", tc.number, fact.EvidenceRefIDs, want)
		}
	}
}
