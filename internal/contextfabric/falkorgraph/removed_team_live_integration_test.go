package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func liveTeamBatch(orgID, batchID string, observed time.Time, goneAt contextfabric.ScalarValue) contextfabric.ProjectionBatch {
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: team,
			Properties: map[string]contextfabric.ScalarValue{
				"is_active": {Boolean: ptrBool(true)},
				"gone_at":   goneAt,
			},
			Authorization:  contextfabric.AuthorizationScope{TeamIDs: []string{"tango"}, RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_team_tango_1"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

func ptrBool(v bool) *bool { return &v }

func TestLiveRemovedTeamFlagClearsWhenTheSourceClearsGoneAt(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-removed-team-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}
	principal := storage.Principal{OrgID: orgID}
	discover := func() []contextfabric.SubjectRef {
		t.Helper()
		request := liveInvestigationRequest()
		request.Question = "How is Tango doing?"
		graphContext, err := adapter.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{
			Request: request,
			Interpretation: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{team.Label},
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
			},
			Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{team}},
		})
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		return graphContext.RemovedTeams
	}

	observed := time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	gone := "2026-10-09 17:30:00.000000"
	if _, err := adapter.ApplyProjectionBatch(ctx, liveTeamBatch(orgID, "batch_live_team_0001", observed, contextfabric.ScalarValue{String: &gone})); err != nil {
		t.Fatalf("project removed team: %v", err)
	}
	if got := discover(); len(got) != 1 || got[0] != team {
		t.Fatalf("RemovedTeams after a gone_at projection = %v, want [%v]", got, team)
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, liveTeamBatch(orgID, "batch_live_team_0002", observed.Add(time.Hour), contextfabric.ScalarValue{Null: true})); err != nil {
		t.Fatalf("re-project team with gone_at cleared: %v", err)
	}
	if got := discover(); len(got) != 0 {
		t.Fatalf("RemovedTeams after the source cleared gone_at = %v, want none: the stored property must be cleared, not kept", got)
	}
}
