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
//
// CHAOS-7080: the repository wildcard no longer admits a repository-restricted
// caller. Such a caller sees a project only when the project reaches a granted
// repository through a current OWNED_BY_TEAM edge to a team whose repository
// list holds it. Project Alpha has no owning team, so every restricted case on
// it is denied; Project Beta is owned by a team that owns acme/api.
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
	beta := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:project-beta", Label: "Project Beta"}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}
	batch.Entities = append(batch.Entities,
		contextfabric.EntityProjection{
			Subject: beta, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization:  contextfabric.AuthorizationScope{ProjectIDs: []string{"project-beta"}},
			EvidenceRefIDs: []string{"evidence_project_anchor_beta"}, ObservedAt: observed, SourceVersion: "v1",
		},
		contextfabric.EntityProjection{
			Subject: team, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}},
			EvidenceRefIDs: []string{"evidence_team_platform"}, ObservedAt: observed, SourceVersion: "v1",
		},
	)
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	ownedFrom := observed.Add(-time.Hour)
	ownership := batch
	ownership.BatchID = "batch_project_anchor_member_00000002"
	ownership.Cursor, ownership.NextCursor = "cursor-1", "cursor-2"
	ownership.Entities = []contextfabric.EntityProjection{}
	ownership.Relationships = []contextfabric.RelationshipProjection{{
		RelationshipID: "relationship_beta_owned_by_platform", Type: "OWNED_BY_TEAM", From: beta, To: team,
		Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
		Authorization: contextfabric.AuthorizationScope{ProjectIDs: []string{"project-beta"}}, EvidenceRefIDs: []string{"evidence_beta_owned_by_platform"},
		ObservedAt: observed.Add(time.Minute), ValidFrom: &ownedFrom, SourceVersion: "v1",
	}}
	if _, err := adapter.ApplyProjectionBatch(ctx, ownership); err != nil {
		t.Fatalf("ownership ApplyProjectionBatch() error = %v", err)
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
		anchor                     string
		wantExists, wantAuthorized bool
		wantUnverifiable           bool
	}{
		{
			name: "restricted caller is denied a wildcard project with no owning team", principal: matchingPrincipal, scope: matchingScope, binding: binding,
			wantExists: true,
		},
		{
			name: "another restricted grant is denied the project's wildcard repository scope", principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/other"}}, scope: matchingScope, binding: binding,
			wantExists: true,
		},
		{
			name: "requested repository filter does not reopen the project's wildcard repository scope", principal: matchingPrincipal, scope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/other"}, ProjectIDs: matchingScope.ProjectIDs, TeamIDs: matchingScope.TeamIDs}, binding: binding,
			wantExists: true,
		},
		{
			name: "requested project filter does not authorize the project anchor", principal: matchingPrincipal, scope: contextfabric.RequestedScope{RepositorySlugs: matchingScope.RepositorySlugs, ProjectIDs: []string{"project-other"}, TeamIDs: matchingScope.TeamIDs}, binding: binding,
			wantExists: true,
		},
		{
			name: "requested team filter does not reopen the project for a restricted caller", principal: matchingPrincipal, scope: contextfabric.RequestedScope{RepositorySlugs: matchingScope.RepositorySlugs, ProjectIDs: matchingScope.ProjectIDs, TeamIDs: []string{"team-other"}}, binding: binding,
			wantExists: true,
		},
		{
			name: "restricted caller is admitted to a project whose owning team owns a granted repository", principal: matchingPrincipal,
			scope:   contextfabric.RequestedScope{RepositorySlugs: []string{"acme/api"}, ProjectIDs: []string{"project-beta"}, TeamIDs: matchingScope.TeamIDs},
			binding: binding, anchor: beta.CanonicalID,
			wantExists: true, wantAuthorized: true,
		},
		{
			name: "restricted caller without the owned repository is denied the owned project", principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/other"}},
			scope:   contextfabric.RequestedScope{ProjectIDs: []string{"project-beta"}},
			binding: binding, anchor: beta.CanonicalID,
			wantExists: true,
		},
		{
			name: "unrestricted caller is admitted to the wildcard project", principal: storage.Principal{OrgID: orgID}, scope: contextfabric.RequestedScope{ProjectIDs: matchingScope.ProjectIDs}, binding: binding,
			wantExists: true, wantAuthorized: true,
		},
		{
			name: "binding for an unavailable epoch is unverifiable", principal: matchingPrincipal, scope: matchingScope,
			binding:          contextfabric.ResolvedGraphBinding{GraphKey: "cf_" + orgID + "_epoch_999999_definitely_never_written", Epoch: 999999},
			wantUnverifiable: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anchor := tc.anchor
			if anchor == "" {
				anchor = project.CanonicalID
			}
			result, err := adapter.AnchorMember(ctx, tc.principal, tc.scope, tc.binding, contextfabric.SubjectProject, anchor)
			if err != nil {
				t.Fatalf("AnchorMember() error = %v", err)
			}
			if result.Exists != tc.wantExists || result.Authorized != tc.wantAuthorized || result.Unverifiable != tc.wantUnverifiable {
				t.Fatalf("AnchorMember() = %+v, want Exists=%t Authorized=%t Unverifiable=%t", result, tc.wantExists, tc.wantAuthorized, tc.wantUnverifiable)
			}
		})
	}
}
