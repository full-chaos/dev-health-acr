package falkorgraph_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveATeamNarrowingStatusPopulationCountsOnlyTheTeamsMembers (CHAOS-12327,
// review r1): an anchored member set with a status ("Alpha's open pull
// requests") under a team narrowing that reaches none of them. Team Alpha owns
// acme/api and its 4 open pull requests, but its team node names no team id,
// so a requested team reaches nothing: the member set is empty, and its
// population must not count the 4 pull requests outside the narrowing.
func TestLiveATeamNarrowingStatusPopulationCountsOnlyTheTeamsMembers(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-12327-status-population-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	batch, edges, team, _, _ := memberStatusCapFixture(orgID, time.Now().UTC())
	for _, b := range []contextfabric.ProjectionBatch{batch, edges} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	read := func(t *testing.T, teams []string) contextfabric.GraphContext {
		t.Helper()
		scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{team.Label}, MemberKind: contextfabric.SubjectPullRequest,
			MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: "open"}
		bases := contextfabric.CommitBasisSet{}
		bases.Record(team, contextfabric.CommitBasisAuthoritativeIdentity)
		request := contextfabric.GraphDiscoveryRequest{
			Request: contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12327_status_population",
				Question: "which pull requests of Alpha are open?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
				Options: contextfabric.InvestigationOptions{
					MaxSubjectCandidates: 10, MaxCohortMembers: 2, MaxRelationshipPaths: 50,
					MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
				},
				Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			},
			Interpretation: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			},
			Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{team}, Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_anchor", Subject: team, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{team.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}}},
			Frame: &contextfabric.QuestionFrame{
				Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
				Temporal:          contextfabric.TemporalIntentCurrent,
				Version:           contextfabric.QuestionFrameVersion,
			},
			ScopeAnchorKind: team.Kind,
			Bases:           bases,
		}
		gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
		if err != nil {
			t.Fatalf("teams %v: DiscoverContext() error = %v", teams, err)
		}
		return gc
	}
	members := func(gc contextfabric.GraphContext) int {
		if gc.Cohort == nil {
			return 0
		}
		return len(gc.Cohort.Members)
	}
	t.Run("no narrowing: 4 open, 2 listed under the member cap", func(t *testing.T) {
		gc := read(t, nil)
		if members(gc) != 2 || gc.CohortPopulation != 4 {
			t.Fatalf("members=%d population=%d, want 2 listed of a population of 4", members(gc), gc.CohortPopulation)
		}
	})
	t.Run("a narrowing that reaches none of them: no member, no population", func(t *testing.T) {
		gc := read(t, []string{"team-other"})
		if members(gc) != 0 || gc.CohortPopulation != 0 {
			t.Fatalf("members=%d population=%d, want 0 and 0: the population counts only members inside the narrowing", members(gc), gc.CohortPopulation)
		}
	})
}

// TestLiveATeamNarrowingKeepsTheStatusPopulationALowerBoundWhereOwnershipIsMissing
// (CHAOS-12327, review r1): the organization's open pull requests under a team
// narrowing, when the status census is cut past the member read. team-t owns
// acme/owned and its 30 open pull requests; acme/orphan has no owner and one
// open pull request, first by id. The read serves 25: the orphan's ownership is
// missing (undecided, out of scope) and 24 are the team's. The status count
// decides all 31 matches, so the population is the team's 30 -- a lower bound,
// because one match's ownership was missing, even though the count itself was
// complete.
func TestLiveATeamNarrowingKeepsTheStatusPopulationALowerBoundWhereOwnershipIsMissing(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-12327-status-floor-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	entities, edges := teamStatusFloorFixture(orgID, time.Now().UTC())
	for _, b := range []contextfabric.ProjectionBatch{entities, edges} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Acme"}, MemberKind: contextfabric.SubjectPullRequest,
		MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: "open"}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12327_status_floor",
			Question: "which pull requests are open?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{TeamIDs: []string{"team-t"}},
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
	gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if gc.Cohort == nil {
		t.Fatal("no member set served, want the team's open pull requests")
	}
	for _, m := range gc.Cohort.Members {
		if m.Subject.CanonicalID == teamStatusFloorOrphanPR {
			t.Fatalf("served %s, whose repository has no owner: out of the narrowing", teamStatusFloorOrphanPR)
		}
	}
	if gc.CohortPopulation != 30 || !gc.CohortPopulationLowerBound {
		t.Fatalf("population=%d lower bound=%v (members %d), want 30 and a lower bound: the orphan's ownership was missing",
			gc.CohortPopulation, gc.CohortPopulationLowerBound, len(gc.Cohort.Members))
	}
}

const teamStatusFloorOrphanPR = "pull_request:aaa-orphan:001"

// teamStatusFloorFixture: team-t owns acme/owned (30 open pull requests);
// acme/orphan has no owner (one open pull request, first by id).
func teamStatusFloorFixture(orgID string, observed time.Time) (contextfabric.ProjectionBatch, contextfabric.ProjectionBatch) {
	created := observed.Add(-48 * time.Hour)
	text := func(value string) contextfabric.ScalarValue { return contextfabric.ScalarValue{String: &value} }
	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, properties map[string]contextfabric.ScalarValue) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: typ, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	owned := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}}
	orphanScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/orphan"}}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-t", Label: "Team T"}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-owned", Label: "acme/owned"}
	orphan := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-orphan", Label: "acme/orphan"}
	entities := []contextfabric.EntityProjection{
		entity(team, contextfabric.AuthorizationScope{TeamIDs: []string{"team-t"}}, nil),
		entity(repo, owned, nil), entity(orphan, orphanScope, nil),
	}
	relationships := []contextfabric.RelationshipProjection{
		relationship("relationship_owned_team", contractsv1.ContextFabricRelationshipOwnedByTeam, repo, team,
			contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}, TeamIDs: []string{"team-t"}}),
	}
	open := map[string]contextfabric.ScalarValue{"state": text("open")}
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: teamStatusFloorOrphanPR, Label: "PR orphan"}
	entities = append(entities, entity(pr, orphanScope, open))
	relationships = append(relationships, relationship("relationship_orphan_pr_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pr, orphan, orphanScope))
	for i := 1; i <= 30; i++ {
		pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:owned:%03d", i), Label: fmt.Sprintf("PR #%d", i)}
		entities = append(entities, entity(pr, owned, open))
		relationships = append(relationships, relationship(fmt.Sprintf("relationship_owned_pr_%03d_repository", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pr, repo, owned))
	}
	base := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor, e.Entities = "batch_12327_status_floor_0001", "", "cursor-1", entities
	r.BatchID, r.Cursor, r.NextCursor, r.Relationships = "batch_12327_status_floor_0002", "cursor-1", "cursor-2", relationships
	return e, r
}
