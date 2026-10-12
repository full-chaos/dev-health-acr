package falkorgraph_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLivePullRequestStatusFilterSelectsByStateAndReviews projects a
// repository's pull requests with their stored state and review rows into a
// real FalkorDB, then asks for the repository's pull requests in each status
// of the closed set through the production reader: the state read and the
// review hop run as written, and each status serves exactly the pull requests
// that hold it.
func TestLivePullRequestStatusFilterSelectsByStateAndReviews(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-pull-request-status-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Now().UTC()
	repoScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	entity := func(subject contextfabric.SubjectRef, properties map[string]contextfabric.ScalarValue, validFrom *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: repoScope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: validFrom, SourceVersion: "v1",
		}
	}
	state := func(value string) map[string]contextfabric.ScalarValue {
		return map[string]contextfabric.ScalarValue{"state": {String: &value}}
	}
	relationship := func(id, typ string, from, to contextfabric.SubjectRef, validFrom *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: contextfabric.RelationshipType(typ), From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: repoScope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: validFrom, SourceVersion: "v1",
		}
	}
	created := observed.Add(-48 * time.Hour)
	type review struct {
		state string
		at    time.Duration
	}
	pulls := []struct {
		number  int
		state   string
		reviews []review
	}{
		{1, "open", nil},
		{2, "open", []review{{"CHANGES_REQUESTED", -3 * time.Hour}, {"APPROVED", -2 * time.Hour}}},
		{3, "open", []review{{"APPROVED", -3 * time.Hour}, {"CHANGES_REQUESTED", -2 * time.Hour}, {"COMMENTED", -1 * time.Hour}}},
		{4, "open", []review{{"COMMENTED", -2 * time.Hour}}},
		{5, "merged", []review{{"APPROVED", -2 * time.Hour}}},
		{6, "closed", nil},
	}
	entities := []contextfabric.EntityProjection{entity(repository, nil, nil)}
	var relationships []contextfabric.RelationshipProjection
	ids := map[int]string{}
	for _, pull := range pulls {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:acme-api:%d", pull.number), Label: fmt.Sprintf("PR #%d", pull.number)}
		ids[pull.number] = subject.CanonicalID
		entities = append(entities, entity(subject, state(pull.state), &created))
		relationships = append(relationships, relationship(fmt.Sprintf("relationship_pr_%d_repository", pull.number), "BELONGS_TO_REPOSITORY", subject, repository, &created))
		for i, r := range pull.reviews {
			submitted := observed.Add(r.at)
			reviewSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(contractsv1.ContextFabricSubjectPullRequestReview), CanonicalID: fmt.Sprintf("pull_request_review:acme-api:%d:%d", pull.number, i), Label: fmt.Sprintf("PR #%d review", pull.number)}
			entities = append(entities, entity(reviewSubject, state(r.state), &submitted))
			relationships = append(relationships, relationship(fmt.Sprintf("relationship_review_%d_%d", pull.number, i), "BELONGS_TO_PULL_REQUEST", reviewSubject, subject, &submitted))
		}
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_pull_request_status_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("entities ApplyProjectionBatch() error = %v", err)
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_pull_request_status_00000002", "cursor-1", "cursor-2"
	edges.Entities, edges.Relationships = []contextfabric.EntityProjection{}, relationships
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationships ApplyProjectionBatch() error = %v", err)
	}

	cases := []struct {
		status string
		want   []int
	}{
		{"", []int{1, 2, 3, 4, 5, 6}},
		{"open", []int{1, 2, 3, 4}},
		{"merged", []int{5}},
		{"closed", []int{6}},
		{"awaiting_review", []int{1}},
		{"approved", []int{2}},
		{"changes_requested", []int{3}},
	}
	for _, tc := range cases {
		t.Run("status="+tc.status, func(t *testing.T) {
			scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{"acme/api"}, MemberKind: contextfabric.SubjectPullRequest}
			if tc.status != "" {
				scoped.MemberQualifier, scoped.MemberQualifierValue = contextfabric.MemberQualifierStatus, tc.status
			}
			bases := contextfabric.CommitBasisSet{}
			bases.Record(repository, contextfabric.CommitBasisAuthoritativeIdentity)
			request := contextfabric.GraphDiscoveryRequest{
				Request: contextfabric.InvestigationRequest{
					SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_pull_request_status",
					Question: "which pull requests of acme/api are in that state?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
					Options: contextfabric.InvestigationOptions{
						MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
						MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
					},
					Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
				},
				Interpretation: contextfabric.InterpretedQuestion{
					Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				},
				Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repository}, Candidates: []contextfabric.SubjectCandidate{{
					ReceiptID: "receipt_anchor", Subject: repository, State: contextfabric.ResolutionCommitted,
					MatchedTerms: []string{"acme/api"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
				}}},
				Frame: &contextfabric.QuestionFrame{
					Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
					SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
					Temporal:          contextfabric.TemporalIntentCurrent,
					Version:           contextfabric.QuestionFrameVersion,
				},
				ScopeAnchorKind: contextfabric.SubjectRepository,
				Bases:           bases,
			}
			graphContext, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			got := []string{}
			if graphContext.Cohort != nil {
				for _, member := range graphContext.Cohort.Members {
					if member.Subject.Kind == contextfabric.SubjectPullRequest {
						got = append(got, member.Subject.CanonicalID)
					}
				}
			}
			want := []string{}
			for _, number := range tc.want {
				want = append(want, ids[number])
			}
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("served pull requests = %v, want %v", got, want)
			}
		})
	}
}
