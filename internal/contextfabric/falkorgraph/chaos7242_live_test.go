package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveReferencedTeamStubFailsClosedUntilTheEntityArrives is CHAOS-7242 on a
// real FalkorDB (codex #724 r3): page 0 carries an ownership edge and NO team
// entity. Before the fix the projector's team stub copied the edge's
// one-repository scope, so a principal scoped to that repository resolved the
// team node from the stub; now the stub fails closed, and once the canonical
// entity merges its own decision applies (owner admitted, non-owner denied).
func TestLiveReferencedTeamStubFailsClosedUntilTheEntityArrives(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-chaos7242-stub-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T7242", Label: "Team T7242"}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/r", Label: "acme/r"}

	relBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_7242_0001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_7242_1", Type: contractsv1.ContextFabricRelationshipOwnedByTeam, From: repo, To: team,
			Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicInferred,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/r"}, TeamIDs: []string{"T7242"}},
			EvidenceRefIDs: []string{"evidence_7242_edge"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, relBatch); err != nil {
		t.Fatalf("page-0 ApplyProjectionBatch() error = %v", err)
	}

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{team.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: team.Kind, ID: team.CanonicalID, Label: team.Label, Source: "live-test"}}
	resolve := func(scopes ...string) int {
		t.Helper()
		principal := storage.Principal{OrgID: orgID, RepositoryScopes: scopes}
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("ResolveSubjects(%v) error = %v", scopes, err)
		}
		return len(resolution.Committed)
	}

	if got := resolve("acme/r"); got != 0 {
		t.Fatalf("page 0 (edge, NO entity): a principal scoped to the edge's repository resolved the team stub (%d committed), want it denied", got)
	}
	if got := resolve(); got != 1 {
		t.Fatalf("page 0: an unrestricted principal must still resolve the stub, got %d", got)
	}

	entityBatch := relBatch
	entityBatch.BatchID = "batch_7242_0002"
	entityBatch.Cursor, entityBatch.NextCursor = "cursor-1", "cursor-2"
	entityBatch.Relationships = []contextfabric.RelationshipProjection{}
	entityBatch.Entities = []contextfabric.EntityProjection{{
		Subject: team, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Authorization:  contextfabric.AuthorizationScope{TeamIDs: []string{"T7242"}, RepositorySlugs: []string{"acme/r", "acme/s"}},
		EvidenceRefIDs: []string{"evidence_7242_team"}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
	}}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}
	if got := resolve("acme/r"); got != 1 {
		t.Fatalf("after the entity merged: an owner (acme/r) must resolve the team, got %d", got)
	}
	if got := resolve("acme/other"); got != 0 {
		t.Fatalf("after the entity merged: a non-owner must be denied, got %d", got)
	}
}
