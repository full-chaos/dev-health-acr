package falkorgraph

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// liveTierFixture is one project whose issues link pull requests of several
// repositories by links of every provenance tier, each repository holding one
// deployment. The deployment of a repository is deployment:<slug>:0.
type liveTierFixture struct {
	entities, relationships contextfabric.ProjectionBatch
	project                 contextfabric.SubjectRef
}

type liveTierLink struct {
	issue string
	// issueRepository is the issue's own repository; empty is a
	// repository-less issue.
	issueRepository string
	// slug is the pull request's repository; tier is the link's stored tier.
	slug, tier string
}

func newLiveTierFixture(orgID string, observed time.Time, links []liveTierLink) liveTierFixture {
	f := liveTierFixture{project: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:tiers", Label: "tiers"}}
	base := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	f.entities, f.relationships = base, base
	f.entities.BatchID, f.entities.Cursor, f.entities.NextCursor = "batch_link_tier_00000001", "", "cursor-1"
	f.relationships.BatchID, f.relationships.Cursor, f.relationships.NextCursor = "batch_link_tier_00000002", "cursor-1", "cursor-2"
	name := strings.NewReplacer(":", "_", "/", "_", ".", "_", "-", "_")
	entity := func(subject contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, kind string) {
		var properties map[string]contextfabric.ScalarValue
		if kind != "" {
			properties = map[string]contextfabric.ScalarValue{"type": {String: liveRouteString(kind)}}
		}
		f.entities.Entities = append(f.entities.Entities, contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Properties: properties,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_" + name.Replace(subject.CanonicalID)},
			ObservedAt: observed, SourceVersion: "v1",
		})
	}
	relate := func(id string, relation contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, authorization contextfabric.AuthorizationScope, tier string) {
		f.relationships.Relationships = append(f.relationships.Relationships, contextfabric.RelationshipProjection{
			RelationshipID: "relationship_link_tier_" + name.Replace(id), Type: relation, From: from, To: to,
			Properties: liveLinkProperties(relation, tier),
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: authorization, EvidenceRefIDs: []string{"evidence_link_tier_" + name.Replace(id)}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		})
	}
	noRepository := contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositoryScope}}
	entity(f.project, contextfabric.AuthorizationScope{ProjectIDs: []string{"project-tiers"}}, "")
	repositories := map[string]bool{}
	for _, l := range links {
		repository := contextfabric.AuthorizationScope{RepositorySlugs: []string{l.slug}}
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + l.slug, Label: l.slug}
		if !repositories[l.slug] {
			repositories[l.slug] = true
			entity(repo, repository, "")
			deployment := contextfabric.SubjectRef{Kind: contextfabric.SubjectDeployment, CanonicalID: "deployment:" + l.slug + ":0", Label: "deployment"}
			entity(deployment, repository, "")
			relate("deployment_"+l.slug, contractsv1.ContextFabricRelationshipBelongsToRepository, deployment, repo, repository, "")
		}
		issueScope := noRepository
		if l.issueRepository != "" {
			issueScope = contextfabric.AuthorizationScope{RepositorySlugs: []string{l.issueRepository}}
		}
		issue := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:" + l.issue, Label: "issue"}
		pullRequest := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:ghpr:" + l.issue, Label: "pull request"}
		entity(issue, issueScope, "issue")
		entity(pullRequest, repository, "")
		relate(l.issue+"_project", contractsv1.ContextFabricRelationshipBelongsToProject, issue, f.project, issueScope, "")
		relate(l.issue+"_link", contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pullRequest, repository, l.tier)
		relate(l.issue+"_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pullRequest, repo, repository, "")
	}
	return f
}

// liveTierLinks: every tier counts for an unrestricted caller; a repository-less
// issue is admitted for a restricted caller by a native link only; an issue of
// a granted repository is admitted by any tier; an unknown tier is no link.
var liveTierLinks = []liveTierLink{
	{issue: "native-less", slug: "acme/native", tier: "native"},
	{issue: "text-less", slug: "acme/text", tier: "explicit_text"},
	{issue: "heuristic-less", slug: "acme/heuristic", tier: "heuristic"},
	{issue: "heuristic-own", issueRepository: "acme/own", slug: "acme/own", tier: "heuristic"},
	{issue: "unknown-less", slug: "acme/unknown", tier: "inferred"},
}

func TestTheLiveTierFixtureIsAValidProjectionBatch(t *testing.T) {
	f := newLiveTierFixture("org-1", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), liveTierLinks)
	for name, batch := range map[string]contextfabric.ProjectionBatch{"entities": f.entities, "relationships": f.relationships} {
		if err := batch.Validate(); err != nil {
			t.Errorf("%s batch is not a valid projection batch: %v", name, err)
		}
	}
}

func liveWalkMembers(t *testing.T, adapter *Adapter, key, orgID string, principal storage.Principal, project contextfabric.SubjectRef) string {
	t.Helper()
	walk, err := adapter.anchorDeploymentMembers(context.Background(), key, orgID, principal, contextfabric.RequestedScope{}, project, 25, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatalf("anchorDeploymentMembers() error = %v", err)
	}
	ids := walkMemberIDs(walk)
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func liveTierAdapter(t *testing.T, links []liveTierLink) (*Adapter, string, string, liveTierFixture) {
	t.Helper()
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-link-tier-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	f := newLiveTierFixture(orgID, time.Now().UTC(), links)
	for _, batch := range []contextfabric.ProjectionBatch{f.entities, f.relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	binding, err := adapter.ResolveInvestigationBinding(ctx, storage.Principal{OrgID: orgID})
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	key, err := adapter.effectiveKey(ctx, orgID, binding)
	if err != nil {
		t.Fatalf("effectiveKey() error = %v", err)
	}
	return adapter, key, orgID, f
}

// TestLiveEveryTierCountsAndARepositoryLessIssueNeedsANativeLink runs the
// link read on a real graph store: the tier filter on the stored edge
// property, and the restricted grant clause with its native-only arm for an
// issue with no repository.
//
// Assertions that would FAIL on the pre-change walk (it read RELATES_TO
// between work items and finds no LINKS_PULL_REQUEST edge): every assertion
// that expects a deployment. The text-only grant, which expects none, passes
// on it vacuously; the old read had no tier, so the native-only rule has no
// old counterpart and is pinned by the grants that mix tiers ("acme/*").
func TestLiveEveryTierCountsAndARepositoryLessIssueNeedsANativeLink(t *testing.T) {
	adapter, key, orgID, f := liveTierAdapter(t, liveTierLinks)
	open := liveWalkMembers(t, adapter, key, orgID, storage.Principal{OrgID: orgID}, f.project)
	if want := "deployment:acme/heuristic:0,deployment:acme/native:0,deployment:acme/own:0,deployment:acme/text:0"; open != want {
		t.Errorf("unrestricted caller reached %q, want %q: every tier is a link, and the unknown tier is none", open, want)
	}
	for name, c := range map[string]struct {
		grants []string
		want   string
	}{
		"owner wildcard: native link of a repository-less issue, heuristic link of an own-repository issue": {[]string{"acme/*"}, "deployment:acme/native:0,deployment:acme/own:0"},
		"only the repository of the text link: the repository-less issue is not admitted":                   {[]string{"acme/text"}, ""},
		"only the repository of the native link":                                                            {[]string{"acme/native"}, "deployment:acme/native:0"},
		"own repository granted: any tier admits its issue":                                                 {[]string{"acme/own"}, "deployment:acme/own:0"},
	} {
		got := liveWalkMembers(t, adapter, key, orgID, storage.Principal{OrgID: orgID, RepositoryScopes: c.grants}, f.project)
		if got != c.want {
			t.Errorf("%s: grants %v reached %q, want %q", name, c.grants, got, c.want)
		}
	}
}

// TestLiveGrantsMatchByTheRuleOfScopeMatch: a restricted caller whose grant is
// an owner wildcard, and one whose grant differs from the stored slug only by
// case, each get exactly their entitled deployments from the real graph store.
//
// Assertions that would FAIL on the old literal pushdown (s IN $grants), on
// this topology: the two owner wildcards of acme, the wildcard of elsewhere and
// the case-differing grant (a wildcard never equals a stored slug, and the
// stored slug is lower-cased). The exact grant and the non-prefix wildcard pass
// on it, and are here to show the new clause admits no more than the rule.
func TestLiveGrantsMatchByTheRuleOfScopeMatch(t *testing.T) {
	links := []liveTierLink{
		{issue: "a", slug: "acme/billing", tier: "native"},
		{issue: "b", slug: "acme/search", tier: "native"},
		{issue: "c", slug: "elsewhere/billing", tier: "native"},
	}
	adapter, key, orgID, f := liveTierAdapter(t, links)
	for name, c := range map[string]struct {
		grants []string
		want   string
	}{
		"owner wildcard":                {[]string{"acme/*"}, "deployment:acme/billing:0,deployment:acme/search:0"},
		"owner wildcard cased":          {[]string{"ACME/*"}, "deployment:acme/billing:0,deployment:acme/search:0"},
		"grant differing by case":       {[]string{"ACME/Billing"}, "deployment:acme/billing:0"},
		"another owner's wildcard":      {[]string{"elsewhere/*"}, "deployment:elsewhere/billing:0"},
		"exact grant of one":            {[]string{"acme/search"}, "deployment:acme/search:0"},
		"a granted owner, not a prefix": {[]string{"acm/*"}, ""},
	} {
		got := liveWalkMembers(t, adapter, key, orgID, storage.Principal{OrgID: orgID, RepositoryScopes: c.grants}, f.project)
		if got != c.want {
			t.Errorf("%s: grants %v reached %q, want %q", name, c.grants, got, c.want)
		}
	}
}
