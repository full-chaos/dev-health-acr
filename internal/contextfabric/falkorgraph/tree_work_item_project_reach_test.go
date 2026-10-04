package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-8667 E1. A repository-less issue is admitted for a restricted caller
// by a native link OR by the library's project-ownership path: one of its
// current projects is owned by a team that owns a repository the caller is
// granted. A text or heuristic link never grants by itself.

const reachIssue = "work_item.v2:z:issue"

// projectOwner is one OWNED_BY_TEAM edge of a project, with the repositories
// its team owns (the team node's ownership-derived list).
type projectOwner struct {
	repos []string
	ended bool
}

// reachSeed adds projects, their issue edges and their ownership to a member
// seed. The ownership read of project_reach.go (RETURN p.canonical_id AS id)
// is answered from owners, current edges only, as its temporal predicate does.
type reachSeed struct {
	*memberSeed
	owners map[string][]projectOwner
}

func newReachSeed() *reachSeed {
	return &reachSeed{memberSeed: newMemberSeed(), owners: map[string][]projectOwner{}}
}

func (s *reachSeed) project(id string, owners ...projectOwner) {
	s.nodes = append(s.nodes, seededNode{kind: "project", id: id, label: id, repos: []string{"*"}})
	s.owners[id] = owners
}

func (s *reachSeed) inProject(issue, project string) {
	s.edges = append(s.edges, seededEdge{"BELONGS_TO_PROJECT", "work_item", issue, "project", project, ""})
}

// linked seeds the standard case: a repository-less issue of the anchor's
// pull request by tier.
func (s *reachSeed) linked(tier string) {
	s.issue(reachIssue, nil)
	s.pullRequest("pull_request:z:1", memberAnchorSlug)
	s.link(reachIssue, "pull_request:z:1", tier)
}

// counts how many issue -> project reads and project reach reads ran.
type reachCounts struct{ projectReads, reachReads int }

func (s *reachSeed) wrap(counts *reachCounts, extra func(*fakeConn)) func(*fakeConn) {
	return func(conn *fakeConn) {
		if extra != nil {
			extra(conn)
		}
		inner := conn.queryFunc
		conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, ro bool) ([]row, error) {
			if params["rel"] == "BELONGS_TO_PROJECT" {
				counts.projectReads++
			}
			if strings.Contains(cypher, "RETURN p."+propCanonicalID+" AS id") {
				counts.reachReads++
				var rows []row
				for project, owners := range s.owners {
					var repos []string
					for _, o := range owners {
						if !o.ended {
							repos = append(repos, o.repos...)
						}
					}
					if len(repos) > 0 {
						rows = append(rows, row{"id": project, "repos": repos})
					}
				}
				return rows, nil
			}
			rows, err := inner(ctx, key, cypher, params, ro)
			if params["rel"] == "BELONGS_TO_PROJECT" {
				// A stored project node carries the "*" wildcard as a string
				// (projection writes an empty repository list that way).
				for _, r := range rows {
					if n, ok := r["b"].(*node); ok {
						n.Properties[propAuthzRepos] = "*"
					}
				}
			}
			return rows, err
		}
	}
}

func granted() storage.Principal {
	return storage.Principal{OrgID: "org-1", RepositoryScopes: []string{memberAnchorSlug}}
}

func reachMembers(t *testing.T, s *reachSeed, principal storage.Principal, scope contextfabric.RequestedScope, extra func(*fakeConn)) (string, contextfabric.TreeWorkItemWalk, reachCounts) {
	t.Helper()
	var counts reachCounts
	walk := s.walk(t, principal, scope, 25, s.wrap(&counts, extra))
	return memberTiers(walk), walk, counts
}

func TestAnIssueOfAProjectOwnedByATeamOfAGrantedRepositoryIsAMemberOnAnyLinkTier(t *testing.T) {
	for name, extra := range map[string]func(*fakeConn){"pushdown and per-row": nil, "per-row only": perRowOnly} {
		for _, tier := range []string{"explicit_text", "heuristic", "native"} {
			s := newReachSeed()
			s.linked(tier)
			s.project("project:p1", projectOwner{repos: []string{memberAnchorSlug, "acme/other"}})
			s.inProject(reachIssue, "project:p1")
			if got, _, _ := reachMembers(t, s, granted(), contextfabric.RequestedScope{}, extra); got != reachIssue+"="+tier {
				t.Errorf("%s, %s link: members %q, want the issue with that tier", name, tier, got)
			}
		}
	}
}

func TestAnIssueOfAProjectOwnedOnlyByATeamOfAnUngrantedRepositoryIsNotAMember(t *testing.T) {
	for name, extra := range map[string]func(*fakeConn){"pushdown and per-row": nil, "per-row only": perRowOnly} {
		for _, tier := range []string{"explicit_text", "heuristic"} {
			s := newReachSeed()
			s.linked(tier)
			s.project("project:p1", projectOwner{repos: []string{"acme/hidden"}})
			s.inProject(reachIssue, "project:p1")
			got, walk, _ := reachMembers(t, s, granted(), contextfabric.RequestedScope{}, extra)
			if got != "" {
				t.Errorf("%s, %s link: members %q, want none", name, tier, got)
			}
			if name == "per-row only" && walk.Denied == 0 {
				t.Errorf("%s, %s link: denied 0, want the row counted", name, tier)
			}
		}
	}
}

func TestAnEndedProjectOwnershipDoesNotGrant(t *testing.T) {
	for name, extra := range map[string]func(*fakeConn){"pushdown and per-row": nil, "per-row only": perRowOnly} {
		s := newReachSeed()
		s.linked("explicit_text")
		s.project("project:p1", projectOwner{repos: []string{memberAnchorSlug}, ended: true})
		s.inProject(reachIssue, "project:p1")
		if got, _, _ := reachMembers(t, s, granted(), contextfabric.RequestedScope{}, extra); got != "" {
			t.Errorf("%s: members %q, want none for an ended ownership", name, got)
		}
	}
}

func TestAnIssueWithTwoCurrentProjectsIsAMemberWhenOneIsReachable(t *testing.T) {
	s := newReachSeed()
	s.linked("heuristic")
	s.project("project:a", projectOwner{repos: []string{"acme/hidden"}})
	s.project("project:b", projectOwner{repos: []string{memberAnchorSlug}})
	s.inProject(reachIssue, "project:a")
	s.inProject(reachIssue, "project:b")
	if got, _, _ := reachMembers(t, s, granted(), contextfabric.RequestedScope{}, nil); got != reachIssue+"=heuristic" {
		t.Errorf("members %q, want the issue", got)
	}
}

func TestANativeLinkStillAdmitsARepositoryLessIssueWithNoProject(t *testing.T) {
	s := newReachSeed()
	s.linked("native")
	if got, _, counts := reachMembers(t, s, granted(), contextfabric.RequestedScope{}, nil); got != reachIssue+"=native" || counts.projectReads != 0 {
		t.Errorf("members %q after %d project reads, want the issue and no project read (native needs none)", got, counts.projectReads)
	}
}

func TestAnUnrestrictedCallerNeedsNoProjectForARepositoryLessIssue(t *testing.T) {
	s := newReachSeed()
	s.linked("explicit_text")
	s.project("project:p1", projectOwner{repos: []string{"acme/hidden"}})
	s.inProject(reachIssue, "project:p1")
	got, _, counts := reachMembers(t, s, open(), contextfabric.RequestedScope{}, nil)
	if got != reachIssue+"=explicit_text" || counts.projectReads != 0 || counts.reachReads != 0 {
		t.Errorf("members %q, %d project reads, %d reach reads, want the issue and no extra read", got, counts.projectReads, counts.reachReads)
	}
}

// TestTheProjectsOfAPageOfIssuesAreReadOnce: three repository-less issues of
// one page cost one issue -> project read and one reach read, not one each.
func TestTheProjectsOfAPageOfIssuesAreReadOnce(t *testing.T) {
	s := newReachSeed()
	s.project("project:p1", projectOwner{repos: []string{memberAnchorSlug}})
	for i := 0; i < 3; i++ {
		id, pr := fmt.Sprintf("work_item.v2:z:%d", i), fmt.Sprintf("pull_request:z:%d", i)
		s.issue(id, nil)
		s.pullRequest(pr, memberAnchorSlug)
		s.link(id, pr, "explicit_text")
		s.inProject(id, "project:p1")
	}
	got, _, counts := reachMembers(t, s, granted(), contextfabric.RequestedScope{}, nil)
	if strings.Count(got, "=explicit_text") != 3 || counts.projectReads != 1 || counts.reachReads != 1 {
		t.Errorf("members %q, %d project reads, %d reach reads, want 3 members from one of each read", got, counts.projectReads, counts.reachReads)
	}
}

// TestARestrictedLinkReadOfUnreachedIssuesStopsAtItsPages: rows the new rule
// does not admit still page and end the read (the page cap bounds it).
func TestARestrictedLinkReadOfUnreachedIssuesStopsAtItsPages(t *testing.T) {
	s := newReachSeed()
	s.project("project:p1", projectOwner{repos: []string{"acme/hidden"}})
	for i := 0; i < 20; i++ {
		id, pr := fmt.Sprintf("work_item.v2:z:%02d", i), fmt.Sprintf("pull_request:z:%02d", i)
		s.issue(id, nil)
		s.pullRequest(pr, memberAnchorSlug)
		s.link(id, pr, "heuristic")
		s.inProject(id, "project:p1")
	}
	var counts reachCounts
	walk := s.walk(t, granted(), contextfabric.RequestedScope{}, 2, s.wrap(&counts, nil))
	if len(walk.Members) != 0 || counts.projectReads == 0 || counts.projectReads > linkSegmentPageCap {
		t.Errorf("members %s, %d project reads, want none and at most %d reads", memberTiers(walk), counts.projectReads, linkSegmentPageCap)
	}
}
