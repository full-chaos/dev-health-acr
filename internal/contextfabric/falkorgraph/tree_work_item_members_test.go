package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The work items of a repository, as the writer projects them: a work item id
// is work_item.v2:<the issue's own repo_id>:<work item id>. A repository-less
// issue carries the zero repo_id sentinel of its own row, never the anchor's.
const (
	memberAnchorSlug = "acme/svc"
	memberAnchorID   = "repository:github:acme/svc"
	zeroRepoID       = "00000000-0000-0000-0000-000000000000"
)

var (
	memberGithubIssue = "work_item.v2:11111111-1111-1111-1111-111111111111:42"
	memberGitlabIssue = "work_item.v2:22222222-2222-2222-2222-222222222222:7"
	memberLinearIssue = "work_item.v2:" + zeroRepoID + ":ENG-1"
	memberJiraIssue   = "work_item.v2:" + zeroRepoID + ":PROJ-9"
)

// memberSeed is the topology of one repository anchor.
type memberSeed struct {
	nodes []seededNode
	edges []seededEdge
}

func newMemberSeed() *memberSeed {
	return &memberSeed{nodes: []seededNode{{kind: "repository", id: memberAnchorID, label: memberAnchorSlug, repos: []string{memberAnchorSlug}}}}
}

// issue seeds an issue; nil repos is a repository-less issue.
func (s *memberSeed) issue(id string, repos []string) {
	if repos == nil {
		repos = []string{noRepositoryScope}
	}
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: id, label: id, repos: repos, workItemType: "issue"})
}

// pullRequest seeds a pull request of a repository slug; the anchor's own
// pull requests belong to the anchor.
func (s *memberSeed) pullRequest(id, slug string) {
	repoID := memberAnchorID
	if slug != memberAnchorSlug {
		repoID = "repository:github:" + slug
		s.nodes = append(s.nodes, seededNode{kind: "repository", id: repoID, label: slug, repos: []string{slug}})
	}
	s.nodes = append(s.nodes, seededNode{kind: "pull_request", id: id, label: id, repos: []string{slug}})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "pull_request", id, "repository", repoID, ""})
}

func (s *memberSeed) link(issue, pullRequest, tier string) {
	s.edges = append(s.edges, linkEdge(issue, pullRequest, tier))
}

func (s *memberSeed) walk(t *testing.T, principal storage.Principal, scope contextfabric.RequestedScope, limit int, wrap func(*fakeConn)) contextfabric.TreeWorkItemWalk {
	t.Helper()
	conn := seededGraphConn(s.nodes, s.edges)
	if wrap != nil {
		wrap(conn)
	}
	walk, err := newFakeAdapter(t, conn).TreeWorkItemMembers(context.Background(), principal, contextfabric.ResolvedGraphBinding{}, scope,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}, limit)
	if err != nil {
		t.Fatalf("TreeWorkItemMembers() error = %v", err)
	}
	return walk
}

// memberTiers renders the members as "id=tier" lines joined by a comma, sorted by id
// as the port promises.
func memberTiers(w contextfabric.TreeWorkItemWalk) string {
	var out []string
	for _, m := range w.Members {
		out = append(out, m.Subject.CanonicalID+"="+m.Tier)
	}
	return strings.Join(out, ",")
}

func open() storage.Principal { return storage.Principal{OrgID: "org-1"} }

// perRowOnly withholds the read's grant clause, so the walk's own per-row rule
// decides on its own (and counts what it denies).
var perRowOnly = withQuery(func(params map[string]interface{}) { delete(params, "grantRaw") })

// reversed hands the link read's rows back in the opposite order, so a weaker
// row arrives before a stronger one.
func reversed(conn *fakeConn) {
	inner := conn.queryFunc
	conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
		rows, err := inner(ctx, key, cypher, params, ro)
		if params["linkRel"] != nil && params["limit"] != nil {
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
		return rows, err
	}
}

// TestRepositoryWorkItemsAreTheLinkedIssuesOfEveryProviderShape: the writer's
// issue shapes (own-repository issues of two hosts, repository-less issues of
// two trackers) linked natively to pull requests of two hosts are all members,
// each native, under their projected work_item.v2 ids.
func TestRepositoryWorkItemsAreTheLinkedIssuesOfEveryProviderShape(t *testing.T) {
	s := newMemberSeed()
	s.issue(memberGithubIssue, []string{"acme/gh-issues"})
	s.issue(memberGitlabIssue, []string{"grp/gl-issues"})
	s.issue(memberLinearIssue, nil)
	s.issue(memberJiraIssue, nil)
	s.pullRequest("pull_request:github:acme/svc:1", memberAnchorSlug)
	s.pullRequest("pull_request:gitlab:acme/svc:2", memberAnchorSlug)
	for _, issue := range []string{memberGithubIssue, memberGitlabIssue, memberLinearIssue, memberJiraIssue} {
		s.link(issue, "pull_request:github:acme/svc:1", "native")
		s.link(issue, "pull_request:gitlab:acme/svc:2", "native")
	}
	walk := s.walk(t, open(), contextfabric.RequestedScope{}, 25, nil)
	want := strings.Join([]string{memberLinearIssue + "=native", memberJiraIssue + "=native", memberGithubIssue + "=native", memberGitlabIssue + "=native"}, ",")
	if got := memberTiers(walk); got != want {
		t.Fatalf("members %s, want %s", got, want)
	}
	if walk.PullRequests != 2 || walk.LinkedIssues != 4 || walk.Denied != 0 || walk.Truncated {
		t.Fatalf("pull requests %d, linked issues %d, denied %d, truncated %t; want 2, 4, 0, false", walk.PullRequests, walk.LinkedIssues, walk.Denied, walk.Truncated)
	}
	for _, m := range walk.Members {
		if !strings.HasPrefix(m.Subject.CanonicalID, "work_item.v2:") || m.Subject.Kind != contextfabric.SubjectWorkItem {
			t.Errorf("member %+v is not a work_item.v2 work item", m.Subject)
		}
	}
}

// TestAMembersTierIsTheStrongestOfItsAdmittedLinks: a native and a heuristic
// link give native, whichever row the read returns first; a text-only link is
// explicit_text; an own-repository issue linked only heuristically is
// heuristic and counted.
func TestAMembersTierIsTheStrongestOfItsAdmittedLinks(t *testing.T) {
	s := newMemberSeed()
	for _, id := range []string{"work_item.v2:a:both", "work_item.v2:a:text", "work_item.v2:a:weak"} {
		s.issue(id, []string{memberAnchorSlug})
	}
	s.pullRequest("pull_request:p:1", memberAnchorSlug)
	s.pullRequest("pull_request:p:2", memberAnchorSlug)
	s.link("work_item.v2:a:both", "pull_request:p:1", "native")
	s.link("work_item.v2:a:both", "pull_request:p:2", "heuristic")
	s.link("work_item.v2:a:text", "pull_request:p:1", "explicit_text")
	s.link("work_item.v2:a:weak", "pull_request:p:2", "heuristic")
	want := "work_item.v2:a:both=native,work_item.v2:a:text=explicit_text,work_item.v2:a:weak=heuristic"
	for name, wrap := range map[string]func(*fakeConn){"strongest row first": nil, "weakest row first": reversed} {
		walk := s.walk(t, open(), contextfabric.RequestedScope{}, 25, wrap)
		if got := memberTiers(walk); got != want {
			t.Errorf("%s: members %s, want %s", name, got, want)
		}
	}
	// The count of members linked only heuristically is the walk's own.
	conn := seededGraphConn(s.nodes, s.edges)
	adapter := newFakeAdapter(t, conn)
	raw, err := adapter.treeMembers(context.Background(), "key", "org-1", open(), contextfabric.RequestedScope{},
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID}, treeIssue, 25, newTemporalFilter(contextfabric.TimeContext{}))
	if err != nil {
		t.Fatal(err)
	}
	if raw.heuristicOnly != 1 {
		t.Fatalf("heuristicOnly %d, want 1 (only the issue with a single heuristic link)", raw.heuristicOnly)
	}
}

// TestAnIssueWithoutAnActualLinkToAPullRequestOfTheAnchorIsNeverAMember: the
// issue's own repository, a link to a pull request of another repository, a
// RELATES_TO to a pull-request work item and a link of no or an unknown tier
// each make no membership.
func TestAnIssueWithoutAnActualLinkToAPullRequestOfTheAnchorIsNeverAMember(t *testing.T) {
	s := newMemberSeed()
	s.issue("work_item.v2:n:own-only", []string{memberAnchorSlug})
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item.v2:n:own-only", "repository", memberAnchorID, ""})
	s.issue("work_item.v2:n:elsewhere", []string{"acme/other"})
	s.pullRequest("pull_request:other:1", "acme/other")
	s.link("work_item.v2:n:elsewhere", "pull_request:other:1", "native")
	s.issue("work_item.v2:n:relates", []string{memberAnchorSlug})
	s.nodes = append(s.nodes, seededNode{kind: "work_item", id: "work_item.v2:n:pr-item", label: "pr", repos: []string{memberAnchorSlug}, workItemType: "pr"})
	s.edges = append(s.edges,
		seededEdge{"BELONGS_TO_REPOSITORY", "work_item", "work_item.v2:n:pr-item", "repository", memberAnchorID, ""},
		seededEdge{"RELATES_TO", "work_item", "work_item.v2:n:relates", "work_item", "work_item.v2:n:pr-item", ""})
	s.pullRequest("pull_request:anchor:1", memberAnchorSlug)
	for i, tier := range []string{"", "inferred", "NATIVE"} {
		id := fmt.Sprintf("work_item.v2:n:tier%d", i)
		s.issue(id, []string{memberAnchorSlug})
		s.link(id, "pull_request:anchor:1", tier)
	}
	walk := s.walk(t, open(), contextfabric.RequestedScope{}, 25, nil)
	if len(walk.Members) != 0 || walk.LinkedIssues != 0 {
		t.Fatalf("members %s, linked issues %d; want none", memberTiers(walk), walk.LinkedIssues)
	}
	// The store answers every tier: the walk's own check drops the same links.
	walk = s.walk(t, open(), contextfabric.RequestedScope{}, 25, withQuery(func(params map[string]interface{}) {
		if params["tiers"] != nil {
			params["tiers"] = []interface{}{"", "inferred", "NATIVE", "native"}
		}
	}))
	if len(walk.Members) != 0 || walk.LinkedIssues != 0 {
		t.Fatalf("walk check: members %s, linked issues %d; want none", memberTiers(walk), walk.LinkedIssues)
	}
}

// TestRepositoryWorkItemCountsAreBeforeAuthorization: the pull requests and
// linked issues counted are those the walk reached, whether the caller may see
// them or not; the denied ones are counted apart.
func TestRepositoryWorkItemCountsAreBeforeAuthorization(t *testing.T) {
	s := newMemberSeed()
	s.issue("work_item.v2:c:visible", []string{memberAnchorSlug})
	s.issue("work_item.v2:c:secret", []string{"acme/secret"})
	s.issue("work_item.v2:c:textonly", nil)
	s.pullRequest("pull_request:c:1", memberAnchorSlug)
	s.link("work_item.v2:c:visible", "pull_request:c:1", "heuristic")
	s.link("work_item.v2:c:secret", "pull_request:c:1", "native")
	s.link("work_item.v2:c:textonly", "pull_request:c:1", "explicit_text")
	walk := s.walk(t, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{memberAnchorSlug}}, contextfabric.RequestedScope{}, 25, perRowOnly)
	if got := memberTiers(walk); got != "work_item.v2:c:visible=heuristic" {
		t.Fatalf("members %s, want only the visible issue", got)
	}
	if walk.PullRequests != 1 || walk.LinkedIssues != 3 || walk.Denied != 2 {
		t.Fatalf("pull requests %d, linked issues %d, denied %d; want 1, 3, 2", walk.PullRequests, walk.LinkedIssues, walk.Denied)
	}
	all := s.walk(t, open(), contextfabric.RequestedScope{}, 25, nil)
	if len(all.Members) != 3 || all.LinkedIssues != 3 || all.Denied != 0 {
		t.Fatalf("unrestricted: %d members, %d linked issues, %d denied; want 3, 3, 0", len(all.Members), all.LinkedIssues, all.Denied)
	}
}

// TestRepositoryWorkItemDeniedCountsIssuesNotLinkRows: denied counts the
// distinct issues the link read returned that no authorized link reached:
// issues, never link rows, so an issue admitted through a native link and also
// text-linked is not denied. Linked issues are counted before authorization
// in both modes: with the read's grant clause by a bounded count with none.
func TestRepositoryWorkItemDeniedCountsIssuesNotLinkRows(t *testing.T) {
	s := newMemberSeed()
	s.issue("work_item.v2:c:visible", []string{memberAnchorSlug})
	s.issue("work_item.v2:c:secret", []string{"acme/secret"})
	s.issue("work_item.v2:c:textonly", nil)
	s.issue("work_item.v2:c:both", nil)
	s.pullRequest("pull_request:c:1", memberAnchorSlug)
	s.pullRequest("pull_request:c:2", memberAnchorSlug)
	s.link("work_item.v2:c:visible", "pull_request:c:1", "heuristic")
	s.link("work_item.v2:c:secret", "pull_request:c:1", "native")
	s.link("work_item.v2:c:textonly", "pull_request:c:1", "explicit_text")
	s.link("work_item.v2:c:both", "pull_request:c:1", "native")
	s.link("work_item.v2:c:both", "pull_request:c:2", "explicit_text")
	granted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{memberAnchorSlug}}
	for name, c := range map[string]struct {
		wrap           func(*fakeConn)
		linked, denied int
	}{
		// The grant clause keeps secret and textonly out of the read: the
		// count still sees four linked issues; the two never read are not
		// denied (nothing served counts what the read did not return).
		"pushdown and per-row": {nil, 4, 0},
		// Every link row is returned; the per-row rule rejects secret and
		// textonly; both is admitted through its native link.
		"per-row only": {perRowOnly, 4, 2},
	} {
		walk := s.walk(t, granted, contextfabric.RequestedScope{}, 25, c.wrap)
		if got := memberTiers(walk); got != "work_item.v2:c:both=native,work_item.v2:c:visible=heuristic" {
			t.Errorf("%s: members %s", name, got)
		}
		if walk.PullRequests != 2 || walk.LinkedIssues != c.linked || walk.Denied != c.denied {
			t.Errorf("%s: pull requests %d, linked issues %d, denied %d; want 2, %d, %d", name, walk.PullRequests, walk.LinkedIssues, walk.Denied, c.linked, c.denied)
		}
	}
}

// TestRepositoryWorkItemsFollowTheCallersAuthorization: with the read's grant
// clause and with the walk's per-row rule alone.
func TestRepositoryWorkItemsFollowTheCallersAuthorization(t *testing.T) {
	seed := func(issueRepos []string, tier string) *memberSeed {
		s := newMemberSeed()
		s.issue("work_item.v2:z:issue", issueRepos)
		s.pullRequest("pull_request:z:1", memberAnchorSlug)
		s.link("work_item.v2:z:issue", "pull_request:z:1", tier)
		return s
	}
	for name, wrap := range map[string]func(*fakeConn){"pushdown and per-row": nil, "per-row only": perRowOnly} {
		withAnchor := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{memberAnchorSlug}}
		for _, tier := range []string{"native", "explicit_text", "heuristic"} {
			if got := memberTiers(seed([]string{memberAnchorSlug}, tier).walk(t, withAnchor, contextfabric.RequestedScope{}, 25, wrap)); got != "work_item.v2:z:issue="+tier {
				t.Errorf("%s: own-repository issue, %s link: members %q, want it with that tier", name, tier, got)
			}
		}
		if got := memberTiers(seed(nil, "native").walk(t, withAnchor, contextfabric.RequestedScope{}, 25, wrap)); got != "work_item.v2:z:issue=native" {
			t.Errorf("%s: repository-less issue, native link: members %q, want it", name, got)
		}
		for _, tier := range []string{"explicit_text", "heuristic"} {
			walk := seed(nil, tier).walk(t, withAnchor, contextfabric.RequestedScope{}, 25, wrap)
			if len(walk.Members) != 0 {
				t.Errorf("%s: repository-less issue, %s link: members %s, want none", name, tier, memberTiers(walk))
			}
		}
		if got := seed([]string{"acme/hidden"}, "native").walk(t, withAnchor, contextfabric.RequestedScope{}, 25, wrap); len(got.Members) != 0 {
			t.Errorf("%s: issue of an ungranted repository: members %s, want none", name, memberTiers(got))
		}
		// A caller without the anchor sees no member of it.
		without := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/elsewhere"}}
		walk := seed([]string{"acme/elsewhere"}, "native").walk(t, without, contextfabric.RequestedScope{}, 25, wrap)
		if len(walk.Members) != 0 {
			t.Errorf("%s: caller without the anchor: members %s, want none", name, memberTiers(walk))
		}
		if wrap != nil && walk.Denied == 0 {
			t.Errorf("%s: caller without the anchor: denied 0, want the hidden link counted", name)
		}
	}
	// A requested repository scope narrows a caller with no grant alike.
	narrowed := contextfabric.RequestedScope{RepositorySlugs: []string{memberAnchorSlug}}
	if got := seed(nil, "explicit_text").walk(t, open(), narrowed, 25, nil); len(got.Members) != 0 {
		t.Errorf("requested scope: repository-less issue, text link: members %s, want none", memberTiers(got))
	}
	if got := memberTiers(seed(nil, "native").walk(t, open(), narrowed, 25, nil)); got != "work_item.v2:z:issue=native" {
		t.Errorf("requested scope: repository-less issue, native link: members %q, want it", got)
	}
}

// TestACutKeepsTheStrongestLinksOfRepositoryWorkItems: room for two of three
// linked issues keeps the native and the text-linked, never the heuristic one
// whose id sorts first, and says the walk was cut.
func TestACutKeepsTheStrongestLinksOfRepositoryWorkItems(t *testing.T) {
	s := newMemberSeed()
	for i, tier := range []string{"heuristic", "native", "explicit_text"} {
		id := fmt.Sprintf("work_item.v2:t:%d", i)
		s.issue(id, []string{memberAnchorSlug})
		pr := fmt.Sprintf("pull_request:t:%d", i)
		s.pullRequest(pr, memberAnchorSlug)
		s.link(id, pr, tier)
	}
	walk := s.walk(t, open(), contextfabric.RequestedScope{}, 2, nil)
	if got := memberTiers(walk); got != "work_item.v2:t:1=native,work_item.v2:t:2=explicit_text" || !walk.Truncated {
		t.Fatalf("members %s truncated %t, want the native and text-linked issues and a cut", got, walk.Truncated)
	}
}

// TestRepositoryWorkItemsAreServedForARepositoryAnchorOfAnOrganization.
func TestRepositoryWorkItemsAreServedForARepositoryAnchorOfAnOrganization(t *testing.T) {
	adapter := newFakeAdapter(t, seededGraphConn(newMemberSeed().nodes, nil))
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam, contextfabric.SubjectWorkItem} {
		anchor := contextfabric.SubjectRef{Kind: kind, CanonicalID: string(kind) + ":x", Label: "x"}
		if _, err := adapter.TreeWorkItemMembers(context.Background(), open(), contextfabric.ResolvedGraphBinding{}, contextfabric.RequestedScope{}, anchor, 25); err == nil {
			t.Errorf("anchor kind %s: no error, want a repository-only refusal", kind)
		}
	}
	if _, err := adapter.TreeWorkItemMembers(context.Background(), storage.Principal{OrgID: " "}, contextfabric.ResolvedGraphBinding{}, contextfabric.RequestedScope{}, repo, 25); err == nil {
		t.Error("empty organization: no error, want a refusal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.TreeWorkItemMembers(ctx, open(), contextfabric.ResolvedGraphBinding{}, contextfabric.RequestedScope{}, repo, 25); err == nil {
		t.Error("cancelled context: no error, want ctx.Err()")
	}
}
