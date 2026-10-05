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

// liveMemberLink is one issue linked to one pull request of a repository.
type liveMemberLink struct {
	// issue is the issue's canonical id; issueRepository is its own
	// repository, empty for a repository-less issue.
	issue, issueRepository string
	// pullRequest is the pull request's id and slug its repository.
	pullRequest, slug string
	tier              string
}

// liveMemberFixture seeds repository <- pull request <- issue as the writer
// projects it, with the same entity and relationship shapes as
// newLiveTierFixture.
func newLiveMemberFixture(orgID string, observed time.Time, extra func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)), links []liveMemberLink) (entities, relationships contextfabric.ProjectionBatch) {
	base := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	entities, relationships = base, base
	entities.BatchID, entities.Cursor, entities.NextCursor = "batch_tree_members_0001", "", "cursor-1"
	relationships.BatchID, relationships.Cursor, relationships.NextCursor = "batch_tree_members_0002", "cursor-1", "cursor-2"
	name := strings.NewReplacer(":", "_", "/", "_", ".", "_", "-", "_", "#", "_")
	add := func(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, kind string) {
		var properties map[string]contextfabric.ScalarValue
		if kind != "" {
			properties = map[string]contextfabric.ScalarValue{"type": {String: liveRouteString(kind)}}
		}
		entities.Entities = append(entities.Entities, contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Properties: properties,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_" + name.Replace(subject.CanonicalID)},
			ObservedAt: observed, SourceVersion: "v1",
		})
	}
	relate := func(id string, relation contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, tier string) {
		relationships.Relationships = append(relationships.Relationships, contextfabric.RelationshipProjection{
			RelationshipID: "relationship_tree_members_" + name.Replace(id), Type: relation, From: from, To: to,
			Properties: liveLinkProperties(relation, tier),
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_tree_members_" + name.Replace(id)}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		})
	}
	repositories := map[string]contextfabric.SubjectRef{}
	repositoryOf := func(slug string) contextfabric.SubjectRef {
		if repo, ok := repositories[slug]; ok {
			return repo
		}
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + slug, Label: slug}
		repositories[slug] = repo
		add(repo, contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}}, "")
		return repo
	}
	noRepository := contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}}
	issues, pullRequests := map[string]bool{}, map[string]bool{}
	for _, l := range links {
		repository := contextfabric.AuthorizationScope{RepositorySlugs: []string{l.slug}}
		repo := repositoryOf(l.slug)
		issueScope := noRepository
		if l.issueRepository != "" {
			issueScope = contextfabric.AuthorizationScope{RepositorySlugs: []string{l.issueRepository}}
		}
		issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: l.issue, Label: "issue"}
		pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: l.pullRequest, Label: "pull request"}
		if !issues[l.issue] {
			issues[l.issue] = true
			add(issue, issueScope, "issue")
		}
		if !pullRequests[l.pullRequest] {
			pullRequests[l.pullRequest] = true
			add(pullRequest, repository, "")
			relate(l.pullRequest+"_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pullRequest, repo, repository, "")
		}
		relate(l.issue+"_"+l.pullRequest+"_link", contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pullRequest, repository, l.tier)
	}
	if extra != nil {
		extra(add, relate)
	}
	return entities, relationships
}

// liveMemberLinks is the provider and tier matrix of the repository acme/svc:
// own-repository issues of two hosts and repository-less issues of two
// trackers, linked natively to a pull request of each host; plus the tier
// cases and the issue linked only to a pull request of another repository.
func liveMemberLinks() []liveMemberLink {
	var links []liveMemberLink
	for _, issue := range []struct{ id, repository string }{
		{memberGithubIssue, "acme/gh-issues"}, {memberGitlabIssue, "grp/gl-issues"}, {memberLinearIssue, ""}, {memberJiraIssue, ""},
	} {
		for _, pr := range []string{"pull_request:github:acme/svc:1", "pull_request:gitlab:acme/svc:2"} {
			links = append(links, liveMemberLink{issue: issue.id, issueRepository: issue.repository, pullRequest: pr, slug: memberAnchorSlug, tier: "native"})
		}
	}
	return append(links,
		liveMemberLink{issue: "work_item.v2:a:both", issueRepository: memberAnchorSlug, pullRequest: "pull_request:t:1", slug: memberAnchorSlug, tier: "native"},
		liveMemberLink{issue: "work_item.v2:a:both", issueRepository: memberAnchorSlug, pullRequest: "pull_request:t:2", slug: memberAnchorSlug, tier: "heuristic"},
		liveMemberLink{issue: "work_item.v2:a:text", issueRepository: memberAnchorSlug, pullRequest: "pull_request:t:1", slug: memberAnchorSlug, tier: "explicit_text"},
		liveMemberLink{issue: "work_item.v2:a:weak", issueRepository: memberAnchorSlug, pullRequest: "pull_request:t:2", slug: memberAnchorSlug, tier: "heuristic"},
		liveMemberLink{issue: "work_item.v2:a:text-less", pullRequest: "pull_request:t:1", slug: memberAnchorSlug, tier: "explicit_text"},
		liveMemberLink{issue: "work_item.v2:a:elsewhere", issueRepository: "acme/other", pullRequest: "pull_request:other:1", slug: "acme/other", tier: "native"},
		liveMemberLink{issue: "work_item.v2:a:unknown", issueRepository: memberAnchorSlug, pullRequest: "pull_request:t:1", slug: memberAnchorSlug, tier: "inferred"})
}

func TestTheLiveMemberFixtureIsAValidProjectionBatch(t *testing.T) {
	entities, relationships := newLiveMemberFixture("org-1", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), nil, liveMemberLinks())
	for name, batch := range map[string]contextfabric.ProjectionBatch{"entities": entities, "relationships": relationships} {
		if err := batch.Validate(); err != nil {
			t.Errorf("%s batch is not a valid projection batch: %v", name, err)
		}
	}
}

// TestLiveRepositoryWorkItemsAreTheIssuesLinkedToItsPullRequests runs the
// repository work item walk on a real graph store: the provider matrix in the
// writer's shape, the strongest tier of each member, the issues that are never
// members (own repository only, a pull request of another repository, a
// RELATES_TO to a pull-request work item, an unknown tier), and a restricted
// caller. Written, not run by the lane that wrote it.
func TestLiveRepositoryWorkItemsAreTheIssuesLinkedToItsPullRequests(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-tree-members-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	entities, relationships := newLiveMemberFixture(orgID, observed, func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		own := contextfabric.AuthorizationScope{RepositorySlugs: []string{memberAnchorSlug}}
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
		// An issue of the anchor repository with no link at all.
		ownOnly := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:a:own-only", Label: "issue"}
		add(ownOnly, own, "issue")
		relate("own_only_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, ownOnly, repo, own, "")
		// An issue related by RELATES_TO to a pull-request work item of the anchor.
		relates := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:a:relates", Label: "issue"}
		prItem := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:a:pr-item", Label: "pull request item"}
		add(relates, own, "issue")
		add(prItem, own, "pr")
		relate("pr_item_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, prItem, repo, own, "")
		relate("relates", contractsv1.ContextFabricRelationshipRelatesTo, relates, prItem, own, "")
	}, liveMemberLinks())
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	binding, err := adapter.ResolveInvestigationBinding(ctx, storage.Principal{OrgID: orgID})
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	members := func(principal storage.Principal, limit int) contextfabric.TreeWorkItemWalk {
		principal.OrgID = orgID
		walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{}, anchor, limit)
		if err != nil {
			t.Fatalf("TreeWorkItemMembers() error = %v", err)
		}
		return walk
	}
	open := members(storage.Principal{}, 25)
	got := memberTiers(open)
	for _, line := range []string{
		memberGithubIssue + "=native", memberGitlabIssue + "=native", memberLinearIssue + "=native", memberJiraIssue + "=native",
		"work_item.v2:a:both=native", "work_item.v2:a:text=explicit_text", "work_item.v2:a:weak=heuristic", "work_item.v2:a:text-less=explicit_text",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("unrestricted members %s lack %s", got, line)
		}
	}
	for _, never := range []string{"own-only", "elsewhere", "relates", "unknown"} {
		if strings.Contains(got, "work_item.v2:a:"+never) {
			t.Errorf("unrestricted members %s include %s, which has no link of record to a pull request of the anchor", got, never)
		}
	}
	if len(open.Members) != 8 || open.Truncated {
		t.Errorf("unrestricted: %d members, truncated %t; want 8, false", len(open.Members), open.Truncated)
	}
	// A caller granted the anchor's repository: own-repository issues by any tier,
	// repository-less issues by a native link only.
	restricted := members(storage.Principal{RepositoryScopes: []string{memberAnchorSlug}}, 25)
	rgot := memberTiers(restricted)
	if strings.Contains(rgot, "work_item.v2:a:text-less") {
		t.Errorf("restricted members %s include a repository-less issue linked only by explicit_text", rgot)
	}
	for _, line := range []string{memberLinearIssue + "=native", memberJiraIssue + "=native", "work_item.v2:a:text=explicit_text", "work_item.v2:a:weak=heuristic"} {
		if !strings.Contains(rgot, line) {
			t.Errorf("restricted members %s lack %s", rgot, line)
		}
	}
	// The issues of the other providers' own repositories are not granted.
	for _, id := range []string{memberGithubIssue, memberGitlabIssue} {
		if strings.Contains(rgot, id) {
			t.Errorf("restricted members %s include %s, an issue of an ungranted repository", rgot, id)
		}
	}
	if without := members(storage.Principal{RepositoryScopes: []string{"acme/elsewhere"}}, 25); len(without.Members) != 0 {
		t.Errorf("a caller without the anchor got members %s", memberTiers(without))
	}
}
