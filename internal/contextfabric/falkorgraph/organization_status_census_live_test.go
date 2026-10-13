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

// TestLiveOrganizationPullRequestStatusIsReadByTheStatusCensus projects, into
// a real FalkorDB, an organization's pull requests: five open ones of a
// repository a restricted caller cannot read first by id, then 30 merged and
// ten open ones of a readable repository, one of them approved. Every read
// that is not narrowed by the status stops at 25 before the open ones; the
// production reader reads them through the status census, with the anchor
// named and with none, and for a restricted caller only the readable ones.
func TestLiveOrganizationPullRequestStatusIsReadByTheStatusCensus(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-organization-status-census-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	entities, edges := organizationStatusCensusFixture(orgID, time.Now().UTC())
	if _, err := adapter.ApplyProjectionBatch(ctx, entities); err != nil {
		t.Fatalf("entities ApplyProjectionBatch() error = %v", err)
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationships ApplyProjectionBatch() error = %v", err)
	}

	ids := func(id func(int) string, from, to int, except ...int) []string {
		out := []string{}
		for i := from; i <= to; i++ {
			if !containsInt(except, i) {
				out = append(out, id(i))
			}
		}
		return out
	}
	unrestricted := storage.Principal{OrgID: orgID}
	restricted := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/api"}}
	readableOpen := ids(organizationStatusCensusPullRequestID, 31, 40)
	cases := []struct {
		name      string
		anchored  bool
		principal storage.Principal
		status    string
		want      []string
	}{
		{"anchored open", true, unrestricted, "open", append(ids(organizationStatusCensusHiddenID, 1, 5), readableOpen...)},
		{"anchored open restricted", true, restricted, "open", readableOpen},
		{"anchored awaiting review restricted", true, restricted, "awaiting_review", ids(organizationStatusCensusPullRequestID, 31, 40, 33)},
		{"unanchored open restricted", false, restricted, "open", readableOpen},
		{"unanchored awaiting review", false, unrestricted, "awaiting_review", append(ids(organizationStatusCensusHiddenID, 1, 5), ids(organizationStatusCensusPullRequestID, 31, 40, 33)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Acme"}, MemberKind: contextfabric.SubjectPullRequest,
				MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: tc.status}
			request := contextfabric.GraphDiscoveryRequest{
				Request: contextfabric.InvestigationRequest{
					SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_organization_status_census",
					Question: "which pull requests are in that state?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
					Options: contextfabric.InvestigationOptions{
						MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
						MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
					},
					Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
				},
				Interpretation: contextfabric.InterpretedQuestion{
					Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				},
				Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}, Candidates: []contextfabric.SubjectCandidate{}},
				Frame: &contextfabric.QuestionFrame{
					Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
					SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
					Temporal:          contextfabric.TemporalIntentCurrent,
					Version:           contextfabric.QuestionFrameVersion,
				},
			}
			if tc.anchored {
				anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:" + orgID, Label: "Acme"}
				bases := contextfabric.CommitBasisSet{}
				bases.Record(anchor, contextfabric.CommitBasisAuthoritativeIdentity)
				request.Resolution = contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{{
					ReceiptID: "receipt_anchor", Subject: anchor, State: contextfabric.ResolutionCommitted,
					MatchedTerms: []string{"Acme"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
				}}}
				request.ScopeAnchorKind, request.ScopeAnchorResolved, request.Bases = contextfabric.SubjectOrganization, true, bases
			}
			graphContext, err := adapter.DiscoverContext(ctx, tc.principal, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if graphContext.Cohort == nil {
				t.Fatalf("no member set served, want %v", tc.want)
			}
			got := []string{}
			for _, member := range graphContext.Cohort.Members {
				got = append(got, member.Subject.CanonicalID)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("served %v, want %v", got, want)
			}
			if !graphContext.Cohort.Complete || graphContext.Cohort.Truncated {
				t.Fatalf("cohort complete=%v truncated=%v, want a complete set", graphContext.Cohort.Complete, graphContext.Cohort.Truncated)
			}
		})
	}
}

func containsInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func organizationStatusCensusPullRequestID(i int) string {
	return fmt.Sprintf("pull_request:acme-api:%03d", i)
}

func organizationStatusCensusHiddenID(i int) string {
	return fmt.Sprintf("pull_request:aaa-hidden:%03d", i)
}

// organizationStatusCensusFixture is the projection of
// TestLiveOrganizationPullRequestStatusIsReadByTheStatusCensus: the entity
// batch and the relationship batch.
func organizationStatusCensusFixture(orgID string, observed time.Time) (contextfabric.ProjectionBatch, contextfabric.ProjectionBatch) {
	created := observed.Add(-48 * time.Hour)
	text := func(value string) contextfabric.ScalarValue { return contextfabric.ScalarValue{String: &value} }
	entity := func(subject contextfabric.SubjectRef, repo string, properties map[string]contextfabric.ScalarValue, validFrom *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{repo}}, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: validFrom, SourceVersion: "v1",
		}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, repo string) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: typ, From: from, To: to, Properties: map[string]contextfabric.ScalarValue{},
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{repo}}, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	hidden := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-hidden", Label: "acme/hidden"}
	entities := []contextfabric.EntityProjection{entity(repository, "acme/api", nil, nil), entity(hidden, "acme/hidden", nil, nil)}
	relationships := []contextfabric.RelationshipProjection{}
	for i := 1; i <= 5; i++ {
		pull := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: organizationStatusCensusHiddenID(i), Label: fmt.Sprintf("Hidden PR #%d", i)}
		entities = append(entities, entity(pull, "acme/hidden", map[string]contextfabric.ScalarValue{"state": text("open")}, &created))
		relationships = append(relationships, relationship(fmt.Sprintf("relationship_hidden_pr_%03d_repository", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pull, hidden, "acme/hidden"))
	}
	for i := 1; i <= 40; i++ {
		state := "merged"
		if i > 30 {
			state = "open"
		}
		pull := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: organizationStatusCensusPullRequestID(i), Label: fmt.Sprintf("PR #%d", i)}
		entities = append(entities, entity(pull, "acme/api", map[string]contextfabric.ScalarValue{"state": text(state)}, &created))
		relationships = append(relationships, relationship(fmt.Sprintf("relationship_pr_%03d_repository", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pull, repository, "acme/api"))
	}
	approved := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(contractsv1.ContextFabricSubjectPullRequestReview), CanonicalID: "pull_request_review:acme-api:033:0", Label: "PR #33 review"}
	submitted := observed.Add(-time.Hour)
	entities = append(entities, entity(approved, "acme/api", map[string]contextfabric.ScalarValue{"state": text("APPROVED")}, &submitted))
	relationships = append(relationships, relationship("relationship_review_033", contractsv1.ContextFabricRelationshipBelongsToPullRequest, approved,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: organizationStatusCensusPullRequestID(33), Label: "PR #33"}, "acme/api"))

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_organization_status_census_01", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_organization_status_census_02", "cursor-1", "cursor-2"
	edges.Entities, edges.Relationships = []contextfabric.EntityProjection{}, relationships
	return batch, edges
}

// TestTheOrganizationStatusCensusFixtureIsAValidProjectionBatch keeps the live
// fixture valid where no graph store runs.
func TestTheOrganizationStatusCensusFixtureIsAValidProjectionBatch(t *testing.T) {
	entities, edges := organizationStatusCensusFixture("org-1", time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	for name, batch := range map[string]contextfabric.ProjectionBatch{"entities": entities, "relationships": edges} {
		if err := batch.Validate(); err != nil {
			t.Errorf("%s batch is not a valid projection batch: %v", name, err)
		}
	}
	if len(entities.Entities) != 2+5+40+1 || len(edges.Relationships) != 5+40+1 {
		t.Fatalf("fixture holds %d entities and %d relationships", len(entities.Entities), len(edges.Relationships))
	}
}
