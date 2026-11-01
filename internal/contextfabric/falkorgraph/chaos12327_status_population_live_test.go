package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
