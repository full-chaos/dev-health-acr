package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveAnchorMember is CHAOS-4042 PR3's own live proof for the pinned-
// epoch graph-side primitive (graphrank.GraphAnchorMemberFunc's production
// implementation): a real FalkorDB read, not a fake, covering the four
// outcomes VerifyAnchorClaimantMembership's own unit tests exercise through
// a fake.
func TestLiveAnchorMember(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-anchor-member-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Now().UTC()
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository_widget_service", Label: "widget-service"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_anchor_member_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: repo, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/widget-service"}}, EvidenceRefIDs: []string{"evidence_widget_service"},
			ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}

	t.Run("exists and authorized under a matching scope", func(t *testing.T) {
		authorizedPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"full-chaos/widget-service"}}
		result, err := adapter.AnchorMember(ctx, authorizedPrincipal, contextfabric.RequestedScope{}, binding, contextfabric.SubjectRepository, "repository_widget_service")
		if err != nil {
			t.Fatalf("AnchorMember() error = %v", err)
		}
		if !result.Exists || !result.Authorized || result.Unverifiable {
			t.Fatalf("AnchorMember() = %+v, want Exists=true Authorized=true Unverifiable=false", result)
		}
	})

	t.Run("exists but not authorized under a non-matching scope", func(t *testing.T) {
		unauthorizedPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"full-chaos/some-other-repo"}}
		result, err := adapter.AnchorMember(ctx, unauthorizedPrincipal, contextfabric.RequestedScope{}, binding, contextfabric.SubjectRepository, "repository_widget_service")
		if err != nil {
			t.Fatalf("AnchorMember() error = %v", err)
		}
		if !result.Exists || result.Authorized || result.Unverifiable {
			t.Fatalf("AnchorMember() = %+v, want Exists=true Authorized=false Unverifiable=false", result)
		}
	})

	t.Run("does not exist: a canonical id that was never written", func(t *testing.T) {
		result, err := adapter.AnchorMember(ctx, principal, contextfabric.RequestedScope{}, binding, contextfabric.SubjectRepository, "repository_never_written")
		if err != nil {
			t.Fatalf("AnchorMember() error = %v", err)
		}
		if result.Exists || result.Unverifiable {
			t.Fatalf("AnchorMember() = %+v, want Exists=false Unverifiable=false", result)
		}
	})

	t.Run("unverifiable: a graph key that does not exist at all", func(t *testing.T) {
		staleBinding := contextfabric.ResolvedGraphBinding{GraphKey: "cf_" + orgID + "_epoch_999999_definitely_never_written", Epoch: 999999}
		result, err := adapter.AnchorMember(ctx, principal, contextfabric.RequestedScope{}, staleBinding, contextfabric.SubjectRepository, "repository_widget_service")
		if err != nil {
			t.Fatalf("AnchorMember() error = %v", err)
		}
		if !result.Unverifiable {
			t.Fatalf("AnchorMember() = %+v, want Unverifiable=true for a graph key that was never created", result)
		}
	})
}
