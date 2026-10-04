package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// liveRouteFixture is two projects in the shape the projection writes: each
// project has one repository-less issue, a pull-request work item that relates
// to it and belongs to the project's own repository, and two deployments of
// that repository. No name shares a word with another subject's, so a term
// retrieves one subject only.
type liveRouteFixture struct {
	entities, relationships contextfabric.ProjectionBatch
	// deployments are the deployment ids per project name.
	deployments map[string][]string
}

func liveRouteString(value string) *string { return &value }

func newLiveRouteFixture(orgID string, observed time.Time) liveRouteFixture {
	fixture := liveRouteFixture{deployments: map[string][]string{}}
	base := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	fixture.entities, fixture.relationships = base, base
	fixture.entities.BatchID, fixture.entities.Cursor, fixture.entities.NextCursor = "batch_project_deployment_route_00000001", "", "cursor-1"
	fixture.relationships.BatchID, fixture.relationships.Cursor, fixture.relationships.NextCursor = "batch_project_deployment_route_00000002", "cursor-1", "cursor-2"

	entity := func(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, properties map[string]contextfabric.ScalarValue, evidence string) {
		fixture.entities.Entities = append(fixture.entities.Entities, contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: authorization,
			EvidenceRefIDs: []string{evidence}, ObservedAt: observed, SourceVersion: "v1",
		})
	}
	relate := func(id string, relation contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope) {
		fixture.relationships.Relationships = append(fixture.relationships.Relationships, contextfabric.RelationshipProjection{
			RelationshipID: "relationship_route_" + id, Type: relation, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_route_edge_" + id},
			ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		})
	}
	for _, p := range []struct{ name, slug, issue, pullRequest string }{
		{"alpha", "acme/billing", "Fix invoice rounding", "Round invoice totals"},
		{"bravo", "acme/search", "Rank stale results lower", "Demote stale hits"},
	} {
		repository := contextfabric.AuthorizationScope{RepositorySlugs: []string{p.slug}}
		project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:" + p.name, Label: p.name}
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + p.slug, Label: p.slug}
		issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:" + p.name + "-1", Label: p.issue}
		pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:ghpr:" + p.name + "-1", Label: p.pullRequest}
		entity(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"project-" + p.name}}, nil, "evidence_route_project_"+p.name)
		entity(repo, repository, nil, "evidence_route_repository_"+p.name)
		entity(issue, contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}},
			map[string]contextfabric.ScalarValue{"type": {String: liveRouteString("issue")}}, "evidence_route_issue_"+p.name)
		entity(pullRequest, repository,
			map[string]contextfabric.ScalarValue{"type": {String: liveRouteString("pr")}}, "evidence_route_pull_request_"+p.name)
		relate(p.name+"_issue_project", contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}})
		relate(p.name+"_pull_request_issue", contractsv1.ContextFabricRelationshipRelatesTo, pullRequest, issue, repository)
		relate(p.name+"_pull_request_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pullRequest, repo, repository)
		for d := 0; d < 2; d++ {
			deployment := contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: fmt.Sprintf("deployment:%s:%d", p.name, d), Label: "production deployment"}
			entity(deployment, repository,
				map[string]contextfabric.ScalarValue{"environment": {String: liveRouteString("production")}}, fmt.Sprintf("evidence_route_deployment_%s_%d", p.name, d))
			relate(fmt.Sprintf("%s_deployment_%d_repository", p.name, d), contractsv1.ContextFabricRelationshipBelongsToRepository, deployment, repo, repository)
			fixture.deployments[p.name] = append(fixture.deployments[p.name], deployment.CanonicalID)
		}
	}
	return fixture
}

// TestTheLiveRouteFixtureIsAValidProjectionBatch keeps the fixture of the
// live arm below valid where no graph store runs.
func TestTheLiveRouteFixtureIsAValidProjectionBatch(t *testing.T) {
	fixture := newLiveRouteFixture("org-1", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for name, batch := range map[string]contextfabric.ProjectionBatch{"entities": fixture.entities, "relationships": fixture.relationships} {
		if err := batch.Validate(); err != nil {
			t.Errorf("%s batch is not a valid projection batch: %v", name, err)
		}
	}
	if len(fixture.deployments["alpha"]) != 2 || len(fixture.deployments["bravo"]) != 2 {
		t.Fatalf("deployments = %v, want two per project", fixture.deployments)
	}
}

// TestLiveNamedProjectsServeTheirOwnDeployments runs the whole route against
// a real graph store: the projection write, the full-text index, the
// resolver's name commit and the walk's four step reads.
func TestLiveNamedProjectsServeTheirOwnDeployments(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-project-deployments-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	fixture := newLiveRouteFixture(orgID, time.Now().UTC())
	for _, batch := range []contextfabric.ProjectionBatch{fixture.entities, fixture.relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	principal := storage.Principal{OrgID: orgID}
	question := func(name string) string { return "list every deployment of project " + name }

	// The fixture must reach the defect: the lexical arm returns the
	// deployments of BOTH projects for the question about one of them.
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	key, err := adapter.effectiveKey(ctx, orgID, binding)
	if err != nil {
		t.Fatalf("effectiveKey() error = %v", err)
	}
	lexical, _, err := adapter.fulltextSearchNodesForKind(ctx, key, orgID, question("alpha"), 25, temporalFilter{}, contextfabric.SubjectDeployment)
	if err != nil {
		t.Fatalf("fulltextSearchNodesForKind() error = %v", err)
	}
	if len(lexical) != 4 {
		t.Fatalf("the lexical arm returned %d deployments for the question, want all 4: the fixture does not offer the foreign deployments the walk must keep out", len(lexical))
	}

	served := map[string][]string{}
	for _, name := range []string{"alpha", "bravo"} {
		answer := investigateAnchorDeployments(t, adapter, principal, contextfabric.SubjectProject, name, question(name))
		requireNameCommit(t, answer, "project.v2:linear:"+name)
		want := append([]string(nil), fixture.deployments[name]...)
		sort.Strings(want)
		if got := answer.members(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("project %s served %v, want exactly its own deployments %v", name, got, want)
		}
		if !answer.result.Cohort.Complete {
			t.Errorf("project %s cohort is not complete", name)
		}
		if len(answer.walkLines) != 1 || answer.walkLines[0]["outcome"] != "members" || answer.walkLines[0]["members"] != float64(2) ||
			answer.walkLines[0]["issues"] != float64(1) || answer.walkLines[0]["linked_pull_requests"] != float64(1) {
			t.Errorf("project %s walk lines = %v, want one line with outcome=members members=2 issues=1 linked_pull_requests=1", name, answer.walkLines)
		}
		served[name] = answer.members()
	}
	if strings.Join(served["alpha"], ",") == strings.Join(served["bravo"], ",") {
		t.Fatalf("two different projects served the same deployments %v", served["alpha"])
	}
}
