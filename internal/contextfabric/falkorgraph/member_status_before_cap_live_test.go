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

// TestLiveMemberStatusIsAppliedBeforeTheMemberCap projects, into a real
// FalkorDB, a team that owns a repository with 30 pull requests (the first 26
// by id merged, the last four open, one of them approved), a project whose 30
// issues each link one of those pull requests, and 30 projects the team owns
// (the first 26 completed, the last four started). The member read is cut at
// 25, so a status applied after the cut finds no match; the production reader
// applies it inside the read and serves the matches, complete.
func TestLiveMemberStatusIsAppliedBeforeTheMemberCap(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-member-status-before-cap-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	batch, edges, team, project := memberStatusCapFixture(orgID, time.Now().UTC())
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("entities ApplyProjectionBatch() error = %v", err)
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, edges); err != nil {
		t.Fatalf("relationships ApplyProjectionBatch() error = %v", err)
	}

	pullRequestID, ownedProjectID := memberStatusCapPullRequestID, memberStatusCapProjectID
	ids := func(id func(int) string, numbers ...int) []string {
		out := []string{}
		for _, n := range numbers {
			out = append(out, id(n))
		}
		return out
	}
	cases := []struct {
		name   string
		anchor contextfabric.SubjectRef
		kind   contextfabric.SubjectKind
		status string
		want   []string
	}{
		{"team pull requests open", team, contextfabric.SubjectPullRequest, "open", ids(pullRequestID, 27, 28, 29, 30)},
		{"team pull requests awaiting review", team, contextfabric.SubjectPullRequest, "awaiting_review", ids(pullRequestID, 27, 28, 30)},
		{"team pull requests approved", team, contextfabric.SubjectPullRequest, "approved", ids(pullRequestID, 29)},
		{"project pull requests open", project, contextfabric.SubjectPullRequest, "open", ids(pullRequestID, 27, 28, 29, 30)},
		{"project pull requests awaiting review", project, contextfabric.SubjectPullRequest, "awaiting_review", ids(pullRequestID, 27, 28, 30)},
		{"team projects started", team, contextfabric.SubjectProject, "started", ids(ownedProjectID, 27, 28, 29, 30)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{tc.anchor.Label}, MemberKind: tc.kind,
				MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: tc.status}
			bases := contextfabric.CommitBasisSet{}
			bases.Record(tc.anchor, contextfabric.CommitBasisAuthoritativeIdentity)
			request := contextfabric.GraphDiscoveryRequest{
				Request: contextfabric.InvestigationRequest{
					SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_member_status_before_cap",
					Question: "which members of the anchor are in that state?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
					Options: contextfabric.InvestigationOptions{
						MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
						MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
					},
					Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
				},
				Interpretation: contextfabric.InterpretedQuestion{
					Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				},
				Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{tc.anchor}, Candidates: []contextfabric.SubjectCandidate{{
					ReceiptID: "receipt_anchor", Subject: tc.anchor, State: contextfabric.ResolutionCommitted,
					MatchedTerms: []string{tc.anchor.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
				}}},
				Frame: &contextfabric.QuestionFrame{
					Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
					SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
					Temporal:          contextfabric.TemporalIntentCurrent,
					Version:           contextfabric.QuestionFrameVersion,
				},
				ScopeAnchorKind: tc.anchor.Kind,
				Bases:           bases,
			}
			graphContext, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if graphContext.Cohort == nil {
				t.Fatalf("no member set served, want %v", tc.want)
			}
			got := []string{}
			for _, member := range graphContext.Cohort.Members {
				if member.Subject.Kind == tc.kind {
					got = append(got, member.Subject.CanonicalID)
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("served %v, want %v", got, tc.want)
			}
			if !graphContext.Cohort.Complete || graphContext.Cohort.Truncated {
				t.Fatalf("cohort complete=%v truncated=%v, want a complete set: the cap applies to the filtered pool", graphContext.Cohort.Complete, graphContext.Cohort.Truncated)
			}
		})
	}
}

// memberStatusCapFixture is the projection of TestLiveMemberStatusIsAppliedBeforeTheMemberCap:
// the entity batch, the relationship batch, the team and the linked project.
func memberStatusCapFixture(orgID string, observed time.Time) (contextfabric.ProjectionBatch, contextfabric.ProjectionBatch, contextfabric.SubjectRef, contextfabric.SubjectRef) {
	created := observed.Add(-48 * time.Hour)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	text := func(value string) contextfabric.ScalarValue { return contextfabric.ScalarValue{String: &value} }
	entity := func(subject contextfabric.SubjectRef, properties map[string]contextfabric.ScalarValue, validFrom *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: validFrom, SourceVersion: "v1",
		}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, properties map[string]contextfabric.ScalarValue) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: typ, From: from, To: to, Properties: properties,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	rank := int64(3)
	native := map[string]contextfabric.ScalarValue{"link_provenance": text("native"), "link_provenance_rank": {Integer: &rank}}

	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:alpha", Label: "Alpha"}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:linked", Label: "Linked"}
	entities := []contextfabric.EntityProjection{entity(team, nil, nil), entity(repository, nil, nil), entity(project, nil, nil)}
	relationships := []contextfabric.RelationshipProjection{relationship("relationship_repository_team", contractsv1.ContextFabricRelationshipOwnedByTeam, repository, team, nil)}
	pullRequestID, ownedProjectID := memberStatusCapPullRequestID, memberStatusCapProjectID
	for i := 1; i <= 30; i++ {
		state, projectState := "merged", "completed"
		if i > 26 {
			state, projectState = "open", "started"
		}
		pull := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: pullRequestID(i), Label: fmt.Sprintf("PR #%d", i)}
		issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:acme:%03d", i), Label: fmt.Sprintf("Issue %d", i)}
		owned := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: ownedProjectID(i), Label: fmt.Sprintf("Owned %d", i)}
		entities = append(entities,
			entity(pull, map[string]contextfabric.ScalarValue{"state": text(state)}, &created),
			entity(issue, map[string]contextfabric.ScalarValue{"type": text("issue")}, &created),
			entity(owned, map[string]contextfabric.ScalarValue{"state": text(projectState)}, &created))
		relationships = append(relationships,
			relationship(fmt.Sprintf("relationship_pr_%03d_repository", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pull, repository, nil),
			relationship(fmt.Sprintf("relationship_issue_%03d_project", i), contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, nil),
			relationship(fmt.Sprintf("relationship_issue_%03d_pr", i), contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pull, native),
			relationship(fmt.Sprintf("relationship_project_%03d_team", i), contractsv1.ContextFabricRelationshipOwnedByTeam, owned, team, nil))
	}
	approved := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(contractsv1.ContextFabricSubjectPullRequestReview), CanonicalID: "pull_request_review:acme-api:029:0", Label: "PR #29 review"}
	submitted := observed.Add(-time.Hour)
	entities = append(entities, entity(approved, map[string]contextfabric.ScalarValue{"state": text("APPROVED")}, &submitted))
	relationships = append(relationships, relationship("relationship_review_029", contractsv1.ContextFabricRelationshipBelongsToPullRequest, approved, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: pullRequestID(29), Label: "PR #29"}, nil))

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_member_status_cap_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	edges := batch
	edges.BatchID, edges.Cursor, edges.NextCursor = "batch_member_status_cap_00000002", "cursor-1", "cursor-2"
	edges.Entities, edges.Relationships = []contextfabric.EntityProjection{}, relationships
	return batch, edges, team, project
}

func memberStatusCapPullRequestID(i int) string { return fmt.Sprintf("pull_request:acme-api:%03d", i) }

func memberStatusCapProjectID(i int) string { return fmt.Sprintf("project:owned-%03d", i) }

// TestTheMemberStatusCapFixtureIsAValidProjectionBatch keeps the live fixture
// valid where no graph store runs.
func TestTheMemberStatusCapFixtureIsAValidProjectionBatch(t *testing.T) {
	entities, edges, _, _ := memberStatusCapFixture("org-1", time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	for name, batch := range map[string]contextfabric.ProjectionBatch{"entities": entities, "relationships": edges} {
		if err := batch.Validate(); err != nil {
			t.Errorf("%s batch is not a valid projection batch: %v", name, err)
		}
	}
	if len(entities.Entities) != 3+3*30+1 || len(edges.Relationships) != 1+4*30+1 {
		t.Fatalf("fixture holds %d entities and %d relationships", len(entities.Entities), len(edges.Relationships))
	}
}
