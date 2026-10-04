package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// tierLink is one issue of a project linked to one pull request of its own
// repository (one deployment), by a link of a tier.
type tierLink struct {
	issue      string
	issueRepos []string // nil: a repository-less issue
	slug, tier string
}

func tierProject(links ...tierLink) projectSeed {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "project", id: projectAnchorID, label: "payments"})
	for i, l := range links {
		repoID := s.repository(l.slug, 1)
		s.link(l.tier, "work_item:"+l.issue, l.issueRepos, fmt.Sprintf("pull_request:pr:%s:%d", l.issue, i), l.tier, repoID, l.slug)
	}
	return s
}

func reachedBy(t *testing.T, s projectSeed, principal storage.Principal, limit int, wrap func(*fakeConn)) treeWalk {
	t.Helper()
	conn := seededGraphConn(s.nodes, s.edges)
	if wrap != nil {
		wrap(conn)
	}
	adapter := newFakeAdapter(t, conn)
	walk, err := adapter.anchorDeploymentMembers(context.Background(), "key", "org-1", principal, contextfabric.RequestedScope{},
		contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID}, limit, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatalf("anchorDeploymentMembers() error = %v", err)
	}
	return walk
}

func members(walk treeWalk) string { return strings.Join(walkMemberIDs(walk), ",") }

// withQuery rewrites the params of every read before the double answers it.
func withQuery(edit func(params map[string]interface{})) func(*fakeConn) {
	return func(conn *fakeConn) {
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
			edit(params)
			return inner(ctx, key, cypher, params, ro)
		}
	}
}

// TestAHigherTierLinkSurvivesACutBeforeLowerTierLinksThatSortFirst: with room
// for one link, the native link is kept over heuristic and text links whose
// ids sort ahead of it.
func TestAHigherTierLinkSurvivesACutBeforeLowerTierLinksThatSortFirst(t *testing.T) {
	s := tierProject(
		tierLink{issue: "a1", slug: "acme/a1", tier: "heuristic"},
		tierLink{issue: "a2", slug: "acme/a2", tier: "heuristic"},
		tierLink{issue: "a3", slug: "acme/a3", tier: "explicit_text"},
		tierLink{issue: "z9", slug: "acme/z9", tier: "native"})
	walk := reachedBy(t, s, storage.Principal{OrgID: "org-1"}, 1, nil)
	if got := members(walk); got != "deployment:acme/z9:0" || !walk.truncated {
		t.Fatalf("members %q truncated %t, want only the native link's deployment and a cut", got, walk.truncated)
	}
	walk = reachedBy(t, s, storage.Principal{OrgID: "org-1"}, 2, nil)
	if got := members(walk); got != "deployment:acme/a3:0,deployment:acme/z9:0" {
		t.Fatalf("members %q, want the native and the text link of a budget of two", got)
	}
}

// TestEveryTierOfTheTableIsALink: a link of any admitted tier reaches its
// deployments, alone.
func TestEveryTierOfTheTableIsALink(t *testing.T) {
	for _, tier := range linkTiers {
		walk := reachedBy(t, tierProject(tierLink{issue: "one", slug: "acme/one", tier: tier.name}), storage.Principal{OrgID: "org-1"}, 25, nil)
		if got := members(walk); got != "deployment:acme/one:0" {
			t.Errorf("tier %s: members %q, want its deployment", tier.name, got)
		}
	}
}

// TestAnEdgeWithAMissingOrUnknownTierIsNotALink: neither the read nor the
// walk's own check follows it, so an unlinked project stays unlinked.
func TestAnEdgeWithAMissingOrUnknownTierIsNotALink(t *testing.T) {
	for _, tier := range []string{"", "inferred", "NATIVE"} {
		s := tierProject(tierLink{issue: "one", slug: "acme/one", tier: tier})
		walk := reachedBy(t, s, storage.Principal{OrgID: "org-1"}, 25, nil)
		if got := members(walk); got != "" || walk.linkTargets != 0 || projectDeploymentWalkOutcome(walk, false, nil) != ProjectDeploymentWalkUnlinked {
			t.Errorf("read filter, tier %q: members %q, link targets %d; want no link", tier, got, walk.linkTargets)
		}
		// The store answers every tier: the walk's own check still drops it.
		walk = reachedBy(t, s, storage.Principal{OrgID: "org-1"}, 25, withQuery(func(params map[string]interface{}) {
			if params["tiers"] != nil {
				params["tiers"] = []interface{}{"", "inferred", "NATIVE", "native"}
			}
		}))
		if got := members(walk); got != "" || walk.linkTargets != 0 {
			t.Errorf("walk check, tier %q: members %q, link targets %d; want no link", tier, got, walk.linkTargets)
		}
	}
}

// TestALinkGrantsAuthorityOnlyWhenNative: a repository-less issue is admitted
// to a restricted caller by a native link to a pull request the caller is
// granted, never by a text or heuristic link. An issue of a granted repository
// is admitted through any tier. Each case runs twice: with the read's grant
// clause (the pushdown) and with the clause withheld, so the walk's per-row
// rule holds on its own.
func TestALinkGrantsAuthorityOnlyWhenNative(t *testing.T) {
	granted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/granted"}}
	for name, wrap := range map[string]func(*fakeConn){
		"pushdown and per-row": nil,
		"per-row only":         withQuery(func(params map[string]interface{}) { delete(params, "grantRaw") }),
	} {
		for _, c := range []struct {
			link tierLink
			want string
		}{
			{tierLink{issue: "r", slug: "acme/granted", tier: "native"}, "deployment:acme/granted:0"},
			{tierLink{issue: "r", slug: "acme/granted", tier: "explicit_text"}, ""},
			{tierLink{issue: "r", slug: "acme/granted", tier: "heuristic"}, ""},
			{tierLink{issue: "o", issueRepos: []string{"acme/granted"}, slug: "acme/granted", tier: "heuristic"}, "deployment:acme/granted:0"},
			{tierLink{issue: "o", issueRepos: []string{"acme/granted"}, slug: "acme/granted", tier: "explicit_text"}, "deployment:acme/granted:0"},
			{tierLink{issue: "h", issueRepos: []string{"acme/hidden"}, slug: "acme/granted", tier: "native"}, ""},
		} {
			walk := reachedBy(t, tierProject(c.link), granted, 25, wrap)
			if got := members(walk); got != c.want {
				t.Errorf("%s: issue %v (%s link): members %q, want %q", name, c.link.issueRepos, c.link.tier, got, c.want)
			}
			if c.want == "" {
				if outcome := projectDeploymentWalkOutcome(walk, true, nil); outcome != ProjectDeploymentWalkDenied {
					t.Errorf("%s: %s link of a repository-less issue: outcome %q, want denied", name, c.link.tier, outcome)
				}
			}
		}
	}
	// A caller with no repository grant who narrows the request to a
	// repository is narrowed too: a repository-less issue needs a native link.
	for tier, want := range map[string]string{"native": "deployment:acme/granted:0", "explicit_text": ""} {
		adapter := newFakeAdapter(t, seededGraphConn(tierProject(tierLink{issue: "r", slug: "acme/granted", tier: tier}).nodes, tierProject(tierLink{issue: "r", slug: "acme/granted", tier: tier}).edges))
		walk, err := adapter.anchorDeploymentMembers(context.Background(), "key", "org-1", storage.Principal{OrgID: "org-1"},
			contextfabric.RequestedScope{RepositorySlugs: []string{"acme/granted"}},
			contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectAnchorID}, 25, newTemporalFilter(contextfabric.TimeContext{}))
		if err != nil {
			t.Fatal(err)
		}
		if got := members(walk); got != want {
			t.Errorf("requested repository scope, %s link of a repository-less issue: members %q, want %q", tier, got, want)
		}
	}
	// An unrestricted caller needs no authority from a link: any tier reaches.
	for _, tier := range []string{"explicit_text", "heuristic"} {
		walk := reachedBy(t, tierProject(tierLink{issue: "r", slug: "acme/granted", tier: tier}), storage.Principal{OrgID: "org-1"}, 25, nil)
		if got := members(walk); got != "deployment:acme/granted:0" {
			t.Errorf("unrestricted, %s link: members %q, want the deployment", tier, got)
		}
	}
}

// TestGrantsMatchByTheRuleOfScopeMatchThroughTheWalk: an owner wildcard and a
// grant that differs only by case each get the links they are entitled to and
// nothing more. The links are native links of repository-less issues, so only
// the pull request's stored repository meets the grants.
func TestGrantsMatchByTheRuleOfScopeMatchThroughTheWalk(t *testing.T) {
	s := tierProject(
		tierLink{issue: "1", slug: "acme/svc", tier: "native"},
		tierLink{issue: "2", slug: "acme/other", tier: "native"},
		tierLink{issue: "3", slug: "elsewhere/svc", tier: "native"})
	for name, c := range map[string]struct {
		grants []string
		want   string
	}{
		"owner wildcard":               {[]string{"acme/*"}, "deployment:acme/other:0,deployment:acme/svc:0"},
		"owner wildcard cased":         {[]string{" ACME/* "}, "deployment:acme/other:0,deployment:acme/svc:0"},
		"grant differing by case":      {[]string{"ACME/Svc"}, "deployment:acme/svc:0"},
		"grant with surrounding space": {[]string{"  acme/SVC "}, "deployment:acme/svc:0"},
		"exact grant":                  {[]string{"acme/svc"}, "deployment:acme/svc:0"},
		"two owners":                   {[]string{"elsewhere/*", "acme/svc"}, "deployment:acme/svc:0,deployment:elsewhere/svc:0"},
		"another owner's wildcard":     {[]string{"elsewhere/*"}, "deployment:elsewhere/svc:0"},
		"owner prefix is not an owner": {[]string{"acm/*"}, ""},
	} {
		walk := reachedBy(t, s, storage.Principal{OrgID: "org-1", RepositoryScopes: c.grants}, 25, nil)
		if got := members(walk); got != c.want {
			t.Errorf("%s %v: members %q, want %q", name, c.grants, got, c.want)
		}
	}
	// The stored slug differs by case from the grant, the other way round.
	cased := tierProject(tierLink{issue: "1", slug: "Acme/Svc", tier: "native"})
	walk := reachedBy(t, cased, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/svc"}}, 25, nil)
	if got := members(walk); got != "deployment:Acme/Svc:0" {
		t.Errorf("stored Acme/Svc, grant acme/svc: members %q, want its deployment", got)
	}
}

// TestThePushdownIsANecessaryConditionOfScopeMatch: for every (grant, entry)
// pair, wherever graphrank.ScopeMatch admits the entry, the precomputed lists
// the read binds admit it too, so the read never drops a row the per-row rule
// would admit.
func TestThePushdownIsANecessaryConditionOfScopeMatch(t *testing.T) {
	grants := []string{"acme/svc", "ACME/Svc", "  acme/svc ", "acme/*", "ACME/*", " Acme/* ", "acme/", "acme/not/real", "a/b/*", "other/*", "/*", "*", "", "Acme", "ACME/SVC"}
	entries := []string{"acme/svc", "ACME/Svc", " acme/svc ", "acme/other", "Acme/Other", "acme/", "acme/not/real", "a/b/c", "a/b", "other/x", "acmex/svc", "acme", "ACME/SVC", "", "*", "acme/svc\n"}
	admitted := 0
	for _, grant := range grants {
		lists := newRepositoryGrants([]string{grant})
		if strings.TrimSpace(grant) == "*" {
			// A "*" grant makes the caller unrestricted: the read has no grant clause.
			if needsProjectReach(storage.Principal{RepositoryScopes: []string{grant}}) {
				t.Errorf("grant %q: caller reads as restricted", grant)
			}
			continue
		}
		for _, entry := range entries {
			if !graphrank.ScopeMatch([]string{entry}, grant, graphrank.ScopeValueRepositoryName) {
				continue
			}
			admitted++
			if !grantsAdmitEntry(lists.raw, lists.norm, lists.owners, entry) {
				t.Errorf("grant %q admits entry %q by ScopeMatch, but the pushdown lists (%v %v %v) drop it", grant, entry, lists.raw, lists.norm, lists.owners)
			}
		}
	}
	if admitted < 15 {
		t.Fatalf("only %d admitted pairs: the table does not exercise the rule", admitted)
	}
}

// TestAVetoedTierIsNotALink: the tier table is the only place tiers are
// listed. Removing a row from a copy of it, and nothing else, makes a link of
// that tier no link, while the other tiers still reach.
func TestAVetoedTierIsNotALink(t *testing.T) {
	saved := linkTiers
	t.Cleanup(func() { linkTiers = saved })
	for _, vetoed := range []string{"native", "explicit_text", "heuristic"} {
		var kept []linkTier
		for _, tier := range saved {
			if tier.name != vetoed {
				kept = append(kept, tier)
			}
		}
		linkTiers = kept
		for _, tier := range saved {
			walk := reachedBy(t, tierProject(tierLink{issue: "one", slug: "acme/one", tier: tier.name}), storage.Principal{OrgID: "org-1"}, 25, nil)
			want := "deployment:acme/one:0"
			if tier.name == vetoed {
				want = ""
			}
			if got := members(walk); got != want {
				t.Errorf("tier %s vetoed: a %s link reaches %q, want %q", vetoed, tier.name, got, want)
			}
		}
	}
}
