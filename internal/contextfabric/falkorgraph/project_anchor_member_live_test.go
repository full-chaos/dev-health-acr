package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveProjectAnchorMember proves that the live FalkorDB adapter reads a
// project anchor from its resolved epoch and applies the principal repository
// grant plus each requested scope dimension to the projected authorization
// attributes. The production project producer emits ProjectIDs only; the
// FalkorDB projection therefore stores wildcard repository and team scopes.
func TestLiveProjectAnchorMember(t *testing.T) {
	ctx := context.Background()
	adapter := newLiveAdapter(t, ctx)
	orgID := "live-project-anchor-member-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Now().UTC()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:project-alpha", Label: "Project Alpha"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_project_anchor_member_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: contextfabric.AuthorizationScope{
				ProjectIDs: []string{"project-alpha"},
			},
			EvidenceRefIDs: []string{"evidence_project_anchor_alpha"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	matchingPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/api"}}
	binding, err := adapter.ResolveInvestigationBinding(ctx, matchingPrincipal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	matchingScope := contextfabric.RequestedScope{
		RepositorySlugs: []string{"acme/api"},
		ProjectIDs:      []string{"project-alpha"},
		TeamIDs:         []string{"team-platform"},
	}

	for _, tc := range []struct {
		name                       string
		principal                  storage.Principal
		scope                      contextfabric.RequestedScope
		binding                    contextfabric.ResolvedGraphBinding
		wantExists, wantAuthorized bool
		wantUnverifiable           bool
	}{
		{
			name: "matching binding and every grant authorizes the project anchor", principal: matchingPrincipal, scope: matchingScope, binding: binding,
			wantExists: true, wantAuthorized: true,
		},
		{
			name: "current principal repository grant matches the project's wildcard repository scope", principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/other"}}, scope: matchingScope, binding: binding,
			wantExists: true, wantAuthorized: true,
		},
		{
			name: "requested repository filter matches the project's wildcard repository scope", principal: matchingPrincipal, scope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/other"}, ProjectIDs: matchingScope.ProjectIDs, TeamIDs: matchingScope.TeamIDs}, binding: binding,
			wantExists: true, wantAuthorized: true,
		},
		{
			name: "requested project filter does not authorize the project anchor", principal: matchingPrincipal, scope: contextfabric.RequestedScope{RepositorySlugs: matchingScope.RepositorySlugs, ProjectIDs: []string{"project-other"}, TeamIDs: matchingScope.TeamIDs}, binding: binding,
			wantExists: true,
		},
		{
			name: "requested team filter matches the project's wildcard team scope", principal: matchingPrincipal, scope: contextfabric.RequestedScope{RepositorySlugs: matchingScope.RepositorySlugs, ProjectIDs: matchingScope.ProjectIDs, TeamIDs: []string{"team-other"}}, binding: binding,
			wantExists: true, wantAuthorized: true,
		},
		{
			name: "binding for an unavailable epoch is unverifiable", principal: matchingPrincipal, scope: matchingScope,
			binding:          contextfabric.ResolvedGraphBinding{GraphKey: "cf_" + orgID + "_epoch_999999_definitely_never_written", Epoch: 999999},
			wantUnverifiable: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := adapter.AnchorMember(ctx, tc.principal, tc.scope, tc.binding, contextfabric.SubjectProject, project.CanonicalID)
			if err != nil {
				t.Fatalf("AnchorMember() error = %v", err)
			}
			if result.Exists != tc.wantExists || result.Authorized != tc.wantAuthorized || result.Unverifiable != tc.wantUnverifiable {
				t.Fatalf("AnchorMember() = %+v, want Exists=%t Authorized=%t Unverifiable=%t", result, tc.wantExists, tc.wantAuthorized, tc.wantUnverifiable)
			}
		})
	}
}
