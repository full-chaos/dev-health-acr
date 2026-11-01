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
// state, no owner) and one also to project:odd (state "weird", owned by team
// Beta). Under a narrowing, an outside-set project counts only when it is
// owned by a requested team; one with no owner is undecided, so the count is a
// lower bound. Neither is a status match, so neither floors the population.
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
	beta := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:beta", Label: "Beta"}
	batch.Entities = append(batch.Entities, contextfabric.EntityProjection{Subject: beta, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Authorization: contextfabric.AuthorizationScope{TeamIDs: []string{"team-beta"}}, EvidenceRefIDs: []string{"evidence_team_beta"},
		ObservedAt: time.Now().UTC(), ValidFrom: &created, SourceVersion: "v1"})
	edges.Relationships = append(edges.Relationships, contextfabric.RelationshipProjection{RelationshipID: "relationship_project_odd_team_beta",
		Type: contractsv1.ContextFabricRelationshipOwnedByTeam, From: odd, To: beta,
		Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope,
		EvidenceRefIDs: []string{"evidence_project_odd_team_beta"}, ObservedAt: time.Now().UTC(), ValidFrom: &created, SourceVersion: "v1"})
	for _, b := range []contextfabric.ProjectionBatch{batch, edges} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-api", Label: "acme/api"}
	read := func(t *testing.T, teams []string) contextfabric.GraphContext {
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
		return gc
	}
	if got := read(t, nil).MemberStatusOutsideSet; got.Count != 2 || got.AtLeast {
		t.Fatalf("no narrowing: outside set %+v, want exactly 2 (project:linked and project:odd; the fixture must reach them)", got)
	}
	for _, tc := range []struct {
		name    string
		teams   []string
		count   int
		atLeast bool
	}{
		{"the owner of project:odd: it is counted; ownerless project:linked makes the count a lower bound", []string{"team-beta"}, 1, true},
		{"a team that owns neither: project:odd is out of scope; project:linked keeps the count a lower bound", []string{"team-other"}, 0, true},
	} {
		gc := read(t, tc.teams)
		if got := gc.MemberStatusOutsideSet; got.Count != tc.count || got.AtLeast != tc.atLeast {
			t.Fatalf("%s: outside set %+v, want count %d, at least %v", tc.name, got, tc.count, tc.atLeast)
		}
		if gc.CohortPopulationLowerBound {
			t.Fatalf("%s: the population is a lower bound; an outside-set project is not a status match, so its missing ownership must not floor the cohort", tc.name)
		}
	}
}
