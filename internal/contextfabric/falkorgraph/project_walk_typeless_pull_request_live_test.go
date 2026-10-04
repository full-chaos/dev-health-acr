package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveAPullRequestWithoutATypeIsNotALinkOnTheProjectWalk runs the real link
// read against a real graph store. A pull-request work item whose projected
// row carries no `type` property is not recognised as a pull request: a project
// whose only link goes to one reads as unlinked, and a project with a typed
// link reads only that link.
func TestLiveAPullRequestWithoutATypeIsNotALinkOnTheProjectWalk(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-typeless-pr-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	entities := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		BatchID: "batch_typeless_pr_00000001", Cursor: "", NextCursor: "cursor-1",
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	relationships := entities
	relationships.BatchID, relationships.Cursor, relationships.NextCursor = "batch_typeless_pr_00000002", "cursor-1", "cursor-2"
	entity := func(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, kind string) {
		var properties map[string]contextfabric.ScalarValue
		if kind != "" {
			properties = map[string]contextfabric.ScalarValue{"type": {String: liveRouteString(kind)}}
		}
		entities.Entities = append(entities.Entities, contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Properties: properties,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_" + strings.NewReplacer(":", "_", "/", "_", ".", "_", "-", "_").Replace(subject.CanonicalID)},
			ObservedAt: observed, SourceVersion: "v1",
		})
	}
	relate := func(id string, relation contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope) {
		relationships.Relationships = append(relationships.Relationships, contextfabric.RelationshipProjection{
			RelationshipID: "relationship_typeless_pr_" + id, Type: relation, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_typeless_pr_" + id}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		})
	}
	repository := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/ledger"}}
	noRepository := contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/ledger", Label: "acme/ledger"}
	entity(repo, repository, "")
	deployment := contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: "deployment:ledger:0", Label: "deployment"}
	entity(deployment, repository, "")
	relate("deployment", contractsv1.ContextFabricRelationshipBelongsToRepository, deployment, repo, repository)
	projects := map[string]contextfabric.SubjectRef{}
	for _, c := range []struct {
		name  string
		types []string
	}{{"typed", []string{"pr", ""}}, {"typeless", []string{""}}} {
		project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:" + c.name, Label: c.name}
		projects[c.name] = project
		entity(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"project-" + c.name}}, "")
		for i, kind := range c.types {
			issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:" + c.name + "-" + string(rune('a'+i)), Label: "issue"}
			entity(issue, noRepository, "issue")
			relate(c.name+"_"+string(rune('a'+i))+"_project", contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, noRepository)
			pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:ghpr:" + c.name + "-" + string(rune('a'+i)), Label: "pull request"}
			entity(pullRequest, repository, kind)
			relate(c.name+"_"+string(rune('a'+i))+"_link", contractsv1.ContextFabricRelationshipRelatesTo, pullRequest, issue, repository)
			relate(c.name+"_"+string(rune('a'+i))+"_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pullRequest, repo, repository)
		}
	}
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if err := batch.Validate(); err != nil {
			t.Fatalf("batch %s is not valid: %v", batch.BatchID, err)
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	key, err := adapter.effectiveKey(ctx, orgID, binding)
	if err != nil {
		t.Fatalf("effectiveKey() error = %v", err)
	}
	for windowName, temporal := range walkWindows(time.Now().UTC()) {
		typed, err := adapter.projectDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, projects["typed"], 25, temporal)
		if err != nil || len(typed.nodes) != 1 || typed.linkedPullRequests != 1 || typed.truncated {
			t.Fatalf("%s, typed: walk = %d members, %d links, truncated %v, error %v; want 1 member, only the typed link, uncut", windowName, len(typed.nodes), typed.linkedPullRequests, typed.truncated, err)
		}
		typeless, err := adapter.projectDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, projects["typeless"], 25, temporal)
		if err != nil || len(typeless.nodes) != 0 || typeless.linkedPullRequests != 0 || typeless.truncated {
			t.Fatalf("%s, typeless: walk = %d members, %d links, truncated %v, error %v; want no member, no link, uncut", windowName, len(typeless.nodes), typeless.linkedPullRequests, typeless.truncated, err)
		}
		if outcome := projectDeploymentWalkOutcome(typeless, false, nil); outcome != ProjectDeploymentWalkUnlinked {
			t.Fatalf("%s, typeless: outcome = %q, want unlinked", windowName, outcome)
		}
	}
}
