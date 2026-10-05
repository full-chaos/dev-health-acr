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
// project has one repository-less issue, a pull request the issue links by a
// native LINKS_PULL_REQUEST edge (the pull request belongs to the project's own
// repository), and two deployments of that repository. No name shares a word with another subject's, so a term
// retrieves one subject only.
type liveRouteFixture struct {
	entities, relationships contextfabric.ProjectionBatch
	// deployments are the deployment ids per project name.
	deployments map[string][]string
}

func liveRouteString(value string) *string { return &value }

// liveLinkProperties are the properties the projection writes on a
// LINKS_PULL_REQUEST edge: the provenance tier and its rank. Other
// relationships carry none.
func liveLinkProperties(relation contractsv1.ContextFabricRelationshipType, tier string) map[string]contextfabric.ScalarValue {
	if relation != contractsv1.ContextFabricRelationshipLinksPullRequest {
		return nil
	}
	rank := map[string]int64{"native": 3, "explicit_text": 2, "heuristic": 1}[tier]
	return map[string]contextfabric.ScalarValue{
		linkTierProperty: {String: liveRouteString(tier)},
		linkRankProperty: {Integer: &rank},
	}
}

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
			Properties: liveLinkProperties(relation, "native"),
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
		pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:ghpr:" + p.name + "-1", Label: p.pullRequest}
		entity(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"project-" + p.name}}, nil, "evidence_route_project_"+p.name)
		entity(repo, repository, nil, "evidence_route_repository_"+p.name)
		entity(issue, contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}},
			map[string]contextfabric.ScalarValue{"type": {String: liveRouteString("issue")}}, "evidence_route_issue_"+p.name)
		entity(pullRequest, repository, nil, "evidence_route_pull_request_"+p.name)
		relate(p.name+"_issue_project", contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}})
		relate(p.name+"_issue_pull_request", contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pullRequest, repository)
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

	// Each step read runs on the graph store, in the current view and under
	// a window, and returns exactly the next frontier. The raw driver error is
	// printed on a refusal: the adapter's own error never carries it.
	for windowName, temporal := range walkWindows(time.Now().UTC()) {
		feed, link := projectLinkHops(t)
		alpha := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:alpha"}
		countCypher := linkSourceCountCypher(feed, temporal)
		rows, err := adapter.api.query(ctx, key, countCypher, linkSegmentParams(orgID, alpha, feed, link, 0, 1, temporal), true)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s, issue count: the graph store refused the read: %v (%d rows)\nquery: %s", windowName, err, len(rows), countCypher)
		}
		linkCypher := linkSegmentCypher(feed, link, temporal, false)
		rows, err = adapter.api.query(ctx, key, linkCypher, linkSegmentParams(orgID, alpha, feed, link, 0, 26, temporal), true)
		if err != nil {
			t.Fatalf("%s, link read: the graph store refused the read: %v\nquery: %s", windowName, err, linkCypher)
		}
		var frontier []string
		for _, r := range rows {
			issue := walkNode(r["m"])
			pullRequest := walkNode(r["b"])
			if issue == nil || pullRequest == nil || canonicalIDOf(issue) != "work_item:linear:alpha-1" {
				t.Fatalf("%s, link read: row %v, want the alpha issue and its pull request", windowName, r)
			}
			frontier = append(frontier, canonicalIDOf(pullRequest))
		}
		if strings.Join(frontier, ",") != "pull_request:ghpr:alpha-1" {
			t.Fatalf("%s, link read: reached %v, want the one linked pull request", windowName, frontier)
		}
		wants := [][]string{{"repository:github:acme/billing"}, fixture.deployments["alpha"]}
		for i, s := range walkStepsInOrder() {
			ids := make([]interface{}, 0, len(frontier))
			for _, id := range frontier {
				ids = append(ids, id)
			}
			cypher := walkStepCypher(s.step, temporal)
			rows, err := adapter.api.query(ctx, key, cypher, walkStepParams(orgID, ids, s.step, 26, temporal), true)
			if err != nil {
				t.Fatalf("%s, %s: the graph store refused the read: %v\nquery: %s", windowName, s.name, err, cypher)
			}
			frontier = frontier[:0]
			for _, r := range rows {
				if n := walkNode(r["b"]); n != nil {
					frontier = append(frontier, canonicalIDOf(n))
				}
			}
			sort.Strings(frontier)
			want := append([]string(nil), wants[i]...)
			sort.Strings(want)
			if strings.Join(frontier, ",") != strings.Join(want, ",") {
				t.Fatalf("%s, %s: reached %v, want %v", windowName, s.name, frontier, want)
			}
		}
		walk, err := adapter.anchorDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{},
			contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:alpha", Label: "alpha"}, 25, temporal)
		if err != nil || len(walk.nodes) != 2 || walk.linkSources != 1 || walk.linkTargets != 1 || walk.truncated {
			t.Fatalf("%s: walk = %d members, %d issues, %d links, truncated %v, error %v; want 2 members from 1 issue and 1 link", windowName, len(walk.nodes), walk.linkSources, walk.linkTargets, walk.truncated, err)
		}
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

// TestLiveTheLinkReadFindsLinksPastTheBudgetAndCountsAnUnlinkedProject runs the
// walk's link read and issue count, and the team and repository reach, on a
// real graph store: a project with more
// issues than the read budget whose only link sorts last reaches its
// deployments uncut, and a project with no link at all is unlinked with its
// exact issue count.
func TestLiveTheLinkReadFindsLinksPastTheBudgetAndCountsAnUnlinkedProject(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-project-link-read-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	entities := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		BatchID: "batch_project_link_read_00000001", Cursor: "", NextCursor: "cursor-1",
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	relationships := entities
	relationships.BatchID, relationships.Cursor, relationships.NextCursor = "batch_project_link_read_00000002", "cursor-1", "cursor-2"
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
			RelationshipID: "relationship_link_read_" + id, Type: relation, From: from, To: to,
			Properties: liveLinkProperties(relation, "native"),
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_link_read_" + id}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		})
	}
	repository := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/ledger"}}
	noRepository := contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/ledger", Label: "acme/ledger"}
	entity(repo, repository, "")
	for d := 0; d < 2; d++ {
		deployment := contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: fmt.Sprintf("deployment:ledger:%d", d), Label: "deployment"}
		entity(deployment, repository, "")
		relate(fmt.Sprintf("deployment_%d", d), contractsv1.ContextFabricRelationshipBelongsToRepository, deployment, repo, repository)
	}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:kilo", Label: "kilo"}
	entity(team, repository, "")
	relate("ledger_owned_by_kilo", contractsv1.ContextFabricRelationshipOwnedByTeam, repo, team, repository)
	// A repository the team owned once: its ownership edge ended, and its
	// deployments are not the team's now.
	archiveScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/archive"}}
	archive := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:acme/archive", Label: "acme/archive"}
	entity(archive, archiveScope, "")
	for d := 0; d < 2; d++ {
		deployment := contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: fmt.Sprintf("deployment:archive:%d", d), Label: "deployment"}
		entity(deployment, archiveScope, "")
		relate(fmt.Sprintf("archive_deployment_%d", d), contractsv1.ContextFabricRelationshipBelongsToRepository, deployment, archive, archiveScope)
	}
	relate("archive_owned_by_kilo", contractsv1.ContextFabricRelationshipOwnedByTeam, archive, team, archiveScope)
	ownedFrom, ownedTo := observed.Add(-72*time.Hour), observed.Add(-36*time.Hour)
	relationships.Relationships[len(relationships.Relationships)-1].ValidFrom = &ownedFrom
	relationships.Relationships[len(relationships.Relationships)-1].ValidTo = &ownedTo
	projects := map[string]contextfabric.SubjectRef{}
	for _, name := range []string{"gamma", "delta"} {
		project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:" + name, Label: name}
		projects[name] = project
		entity(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"project-" + name}}, "")
		for i := 0; i < 30; i++ {
			issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:linear:%s-%03d", name, i), Label: "issue"}
			entity(issue, noRepository, "issue")
			relate(fmt.Sprintf("%s_%03d_project", name, i), contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, noRepository)
			if name == "gamma" && i == 29 {
				pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:ghpr:ledger-1", Label: "pull request"}
				entity(pullRequest, repository, "")
				relate("gamma_link", contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pullRequest, repository)
				relate("gamma_pull_request_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pullRequest, repo, repository)
			}
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
		gamma, err := adapter.anchorDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, projects["gamma"], 25, temporal)
		if err != nil || len(gamma.nodes) != 2 || gamma.linkSources != 30 || gamma.linkTargets != 1 || gamma.truncated {
			t.Fatalf("%s, gamma: walk = %d members, %d issues, %d links, truncated %v, error %v; want 2 members, 30 issues, 1 link, uncut", windowName, len(gamma.nodes), gamma.linkSources, gamma.linkTargets, gamma.truncated, err)
		}
		delta, err := adapter.anchorDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, projects["delta"], 25, temporal)
		if err != nil || len(delta.nodes) != 0 || delta.linkSources != 30 || delta.linkTargets != 0 || delta.truncated {
			t.Fatalf("%s, delta: walk = %d members, %d issues, %d links, truncated %v, error %v; want no member, 30 issues, no link, uncut", windowName, len(delta.nodes), delta.linkSources, delta.linkTargets, delta.truncated, err)
		}
		if outcome := projectDeploymentWalkOutcome(delta, false, nil); outcome != ProjectDeploymentWalkUnlinked {
			t.Fatalf("%s, delta: outcome = %q, want unlinked", windowName, outcome)
		}
		// The restricted link read carries the grant clause: the store runs it,
		// keeps the link the grant admits, and drops the one it does not.
		for _, c := range []struct {
			grant   string
			members int
		}{{"acme/ledger", 2}, {"acme/other", 0}} {
			restricted := storage.Principal{OrgID: orgID, RepositoryScopes: []string{c.grant}}
			walk, err := adapter.anchorDeploymentMembers(ctx, key, orgID, restricted, contextfabric.RequestedScope{}, projects["gamma"], 25, temporal)
			if err != nil || len(walk.nodes) != c.members || walk.truncated {
				t.Fatalf("%s, gamma granted %s: walk = %d members, truncated %v, error %v; want %d members, uncut", windowName, c.grant, len(walk.nodes), walk.truncated, err, c.members)
			}
		}
		for _, anchor := range []contextfabric.SubjectRef{team, repo} {
			reach, err := adapter.anchorDeploymentMembers(ctx, key, orgID, principal, contextfabric.RequestedScope{}, anchor, 25, temporal)
			if err != nil || len(reach.nodes) != 2 || reach.truncated {
				t.Fatalf("%s, %s anchor: reach = %d members, truncated %v, error %v; want the ledger's 2 deployments, uncut", windowName, anchor.Kind, len(reach.nodes), reach.truncated, err)
			}
		}
	}
}
