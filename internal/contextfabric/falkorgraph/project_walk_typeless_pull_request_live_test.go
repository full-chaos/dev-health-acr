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

// TestLiveAPullRequestsTypeDecidesNothingOnTheProjectWalk runs the real link
// read against a real graph store. The pull-request position is the
// pull_request node, which carries no work-item `type`, so the link of an
// issue reaches it whether the issue's own type is "issue" or absent. The type
// of a WORK ITEM still decides who is an issue: a work item typed as a pull
// request that belongs to the project is no issue, and the link it holds is
// not followed (a project whose only such link it is reads as unlinked).
func TestLiveAPullRequestsTypeDecidesNothingOnTheProjectWalk(t *testing.T) {
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
			Properties: liveLinkProperties(relation, "native"),
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
	}{{"plain", []string{"issue"}}, {"typeless", []string{""}}, {"typedpr", []string{"pr"}}} {
		project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:" + c.name, Label: c.name}
		projects[c.name] = project
		entity(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"project-" + c.name}}, "")
		for i, kind := range c.types {
			issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:" + c.name + "-" + string(rune('a'+i)), Label: "issue"}
			entity(issue, noRepository, kind)
			relate(c.name+"_"+string(rune('a'+i))+"_project", contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, noRepository)
			pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:ghpr:" + c.name + "-" + string(rune('a'+i)), Label: "pull request"}
			entity(pullRequest, repository, "")
			relate(c.name+"_"+string(rune('a'+i))+"_link", contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pullRequest, repository)
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
		for _, name := range []string{"plain", "typeless"} {
			walk, err := adapter.anchorDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, projects[name], 25, temporal)
			if err != nil || len(walk.nodes) != 1 || walk.linkTargets != 1 || walk.truncated {
				t.Fatalf("%s, %s: walk = %d members, %d links, truncated %v, error %v; want 1 member through its link, uncut", windowName, name, len(walk.nodes), walk.linkTargets, walk.truncated, err)
			}
		}
		typedPR, err := adapter.anchorDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, projects["typedpr"], 25, temporal)
		if err != nil || len(typedPR.nodes) != 0 || typedPR.linkTargets != 0 || typedPR.truncated {
			t.Fatalf("%s, typed pull-request work item: walk = %d members, %d links, truncated %v, error %v; want no member, no link, uncut", windowName, len(typedPR.nodes), typedPR.linkTargets, typedPR.truncated, err)
		}
		if outcome := projectDeploymentWalkOutcome(typedPR, false, nil); outcome != ProjectDeploymentWalkUnlinked {
			t.Fatalf("%s, typed pull-request work item: outcome = %q, want unlinked", windowName, outcome)
		}
	}
}
