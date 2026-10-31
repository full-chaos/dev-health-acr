package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveATeamNarrowingCountsTheStatusOutsideSetInsideTheNarrowing
// (CHAOS-12352, review r1): "acme/api's started projects" walks repository ->
// pull request -> linked issue -> project, and counts the projects whose state
// is outside the closed set. acme/api's issues belong to project:linked (no
// state) and one also to project:odd (state "weird"); neither is owned by the
// requested team, so under the narrowing neither is counted.
func TestLiveATeamNarrowingCountsTheStatusOutsideSetInsideTheNarrowing(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-12352-outside-set-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	batch, edges, _, _, _ := memberStatusCapFixture(orgID, time.Now().UTC())
	created := time.Now().UTC().Add(-48 * time.Hour)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	weird := "weird"
	odd := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:odd", Label: "Odd"}
	batch.Entities = append(batch.Entities, contextfabric.EntityProjection{Subject: odd, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Properties: map[string]contextfabric.ScalarValue{"state": {String: &weird}}, Authorization: scope, EvidenceRefIDs: []string{"evidence_project_odd"},
		ObservedAt: time.Now().UTC(), ValidFrom: &created, SourceVersion: "v1"})
	edges.Relationships = append(edges.Relationships, contextfabric.RelationshipProjection{RelationshipID: "relationship_issue_001_project_odd",
		Type: contractsv1.ContextFabricRelationshipBelongsToProject, From: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:acme:001", Label: "Issue 1"}, To: odd,
		Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope,
		EvidenceRefIDs: []string{"evidence_issue_001_project_odd"}, ObservedAt: time.Now().UTC(), ValidFrom: &created, SourceVersion: "v1"})
	for _, b := range []contextfabric.ProjectionBatch{batch, edges} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	read := func(t *testing.T, teams []string) contextfabric.MemberStatusOutsideSet {
		t.Helper()
		scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{repository.Label}, MemberKind: contextfabric.SubjectProject,
			MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: "started"}
		bases := contextfabric.CommitBasisSet{}
		bases.Record(repository, contextfabric.CommitBasisAuthoritativeIdentity)
		request := contextfabric.GraphDiscoveryRequest{
			Request: contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12352_outside_set",
				Question: "which projects of acme/api are started?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
				Options:        contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144},
				Consumer:       contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			},
			Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
			Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repository}, Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_anchor", Subject: repository, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{repository.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}}},
			Frame: &contextfabric.QuestionFrame{
				Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
				Temporal:          contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
			},
			ScopeAnchorKind: repository.Kind, Bases: bases,
		}
		gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
		if err != nil {
			t.Fatalf("teams %v: DiscoverContext() error = %v", teams, err)
		}
		return gc.MemberStatusOutsideSet
	}
	unnarrowed := read(t, nil)
	t.Logf("no narrowing: %+v", unnarrowed)
	if unnarrowed.Count < 2 {
		t.Fatalf("no narrowing: outside set %+v, want at least project:linked and project:odd counted (the fixture must reach them)", unnarrowed)
	}
	if got := read(t, []string{"team-other"}); got.Count != 0 {
		t.Fatalf("teams [team-other]: outside set %+v, want 0: no reached project is owned by the requested team", got)
	}
}
