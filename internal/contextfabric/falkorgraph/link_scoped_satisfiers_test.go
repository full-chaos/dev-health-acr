package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12505: the keyed link read of a requested repository scope. Its rule is
// the repository walk's; the real-store differential
// (scoped_census_walk_live_test.go) proves the two agree. These rows pin what
// the read does with the rows a store returns.

func keyedNode(kind, id string, repositories ...string) map[string]interface{} {
	return map[string]interface{}{
		propKind: kind, propCanonicalID: id, propLabel: id,
		propAuthzRepos: repositories, propAuthzProjects: []string{"*"}, propAuthzTeams: []string{"*"},
	}
}

func keyedRow(repository, pullRequest, item map[string]interface{}, tier string) row {
	return row{"a": repository, "m": pullRequest, "b": item, "rl": map[string]interface{}{propPropertyPrefix + linkTierProperty: tier, propRelationshipID: "rel"}}
}

// keyedFake answers the keyed read with rows, as a store does: at most $limit
// of them. It records each statement.
func keyedFake(rows []row) (*fakeConn, *[]string) {
	var seen []string
	return &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		if _, keyed := params["ids"]; !keyed || params["scopeRaw"] == nil {
			return nil, nil
		}
		seen = append(seen, cypher)
		if limit, ok := params["limit"].(int); ok && limit < len(rows) {
			return rows[:limit], nil
		}
		return rows, nil
	}}, &seen
}

func TestTheKeyedLinkReadDecidesEachRowByTheWalksRule(t *testing.T) {
	t.Parallel()
	svc := keyedNode("repository", "repository:svc", "acme/svc")
	other := keyedNode("repository", "repository:other", "acme/other")
	pr1 := keyedNode("pull_request", "pull_request:svc:1", "acme/svc")
	pr2 := keyedNode("pull_request", "pull_request:svc:2", "acme/svc")
	prOther := keyedNode("pull_request", "pull_request:other:1", "acme/other")
	repoLess := keyedNode("work_item", "work_item.v2:0:linear:X-1", noRepositoryScope)
	textOnly := keyedNode("work_item", "work_item.v2:0:linear:X-2", noRepositoryScope)
	ownRepo := keyedNode("work_item", "work_item.v2:own:gh:acme/own#3", "acme/own")
	outside := keyedNode("work_item", "work_item.v2:0:jira:X-4", noRepositoryScope)
	// A repository renamed out of the scope whose pull request the projection
	// has not re-stamped yet: the repository decides, as it does for the walk,
	// which starts from the scope's repositories.
	renamed := keyedNode("repository", "repository:renamed", "acme/renamed")
	prStale := keyedNode("pull_request", "pull_request:renamed:1", "acme/svc")
	stale := keyedNode("work_item", "work_item.v2:0:linear:X-5", noRepositoryScope)
	rows := []row{
		// A repository-less item's text link is refused under a scope.
		keyedRow(svc, pr1, repoLess, "native"),
		keyedRow(svc, pr2, repoLess, "explicit_text"),
		keyedRow(svc, pr1, textOnly, "explicit_text"),
		// An item with its own repository is admitted through a link of any
		// tier, so its tiers compete: the stronger row comes first.
		keyedRow(svc, pr1, ownRepo, "native"),
		keyedRow(svc, pr2, ownRepo, "heuristic"),
		// A row the scope clause would drop, as a store could return it: the
		// row is still decided in process, by the scope rule.
		keyedRow(other, prOther, outside, "native"),
		keyedRow(renamed, prStale, stale, "native"),
	}
	ids := []string{"work_item.v2:0:linear:X-1", "work_item.v2:0:linear:X-2", "work_item.v2:own:gh:acme/own#3", "work_item.v2:0:jira:X-4", "work_item.v2:0:linear:X-5"}
	scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/svc"}}
	for _, c := range []struct {
		name      string
		principal storage.Principal
		want      string
	}{
		// A repository-less item needs a native link under a requested scope; the
		// strongest admitted tier wins; an item with its own repository is decided
		// by that repository, through a link of any tier.
		{"organization-wide", storage.Principal{OrgID: "org_1"}, "work_item.v2:0:linear:X-1=native,work_item.v2:own:gh:acme/own#3=native"},
		// The item's own repository is outside the grant: refused, link or not.
		{"restricted to the scope", storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/svc"}}, "work_item.v2:0:linear:X-1=native"},
		{"restricted to the scope and the item's repository", storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/svc", "acme/own"}}, "work_item.v2:0:linear:X-1=native,work_item.v2:own:gh:acme/own#3=native"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fake, _ := keyedFake(rows)
			tiers, complete, err := newFakeAdapter(t, fake).linkScopedWorkItemsOf(context.Background(), "key", c.principal, scope, ids)
			if err != nil || !complete {
				t.Fatalf("complete %t err %v", complete, err)
			}
			got := make([]string, 0, len(tiers))
			for _, id := range ids {
				if tier, ok := tiers[id]; ok {
					got = append(got, id+"="+tier)
				}
			}
			if strings.Join(got, ",") != c.want {
				t.Fatalf("tiers %v, want %s", got, c.want)
			}
		})
	}
}

// The statement is one text for a caller class and scope shape: the ids are a
// parameter, so a subject outside the grant changes no statement.
func TestTheKeyedLinkReadStatementNamesNoSubject(t *testing.T) {
	t.Parallel()
	scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/svc"}}
	for _, principal := range []storage.Principal{{OrgID: "org_1"}, {OrgID: "org_1", RepositoryScopes: []string{"acme/svc"}}} {
		fake, seen := keyedFake(nil)
		adapter := newFakeAdapter(t, fake)
		for _, ids := range [][]string{{"work_item.v2:0:linear:X-1"}, {"work_item.v2:0:linear:SECRET-9", "work_item.v2:r:gh:acme/hidden#1"}} {
			if _, _, err := adapter.linkScopedWorkItemsOf(context.Background(), "key", principal, scope, ids); err != nil {
				t.Fatal(err)
			}
		}
		if len(*seen) != 2 || (*seen)[0] != (*seen)[1] {
			t.Fatalf("grants %v: statements %q, want one text for both id sets", principal.RepositoryScopes, *seen)
		}
		for _, id := range []string{"X-1", "SECRET-9", "hidden"} {
			if strings.Contains((*seen)[0], id) {
				t.Fatalf("the statement names %q", id)
			}
		}
		if restricted := len(principal.RepositoryScopes) > 0; strings.Contains((*seen)[0], "$grantRaw") != restricted {
			t.Fatalf("grants %v: statement %q, want the grant clause only for a restricted caller", principal.RepositoryScopes, (*seen)[0])
		}
	}
	// A restricted read keeps the repository inside the grants too (codex r1 on
	// #1929), so a repository the grants cannot admit spends no row.
	if anchor := (treeNodeVar{name: "a", position: treeRepository}).repositoryMatchClause("grant"); !strings.Contains(keyedLinkScopeCypher(true, false), anchor) || strings.Contains(keyedLinkScopeCypher(false, false), anchor) {
		t.Fatal("the restricted read must hold the repository to the grants, and only it")
	}
	if strings.Contains(keyedLinkScopeCypher(false, true), "$scopeRaw") || !strings.Contains(keyedLinkScopeCypher(false, false), "$scopeRaw") {
		t.Fatal("a scope that names \"*\" must read without the scope clause, and only it")
	}
}

// A read past its row bound is cut, never a smaller answer; no ids is no read.
func TestTheKeyedLinkReadIsCutPastItsBoundAndSilentWithNoIDs(t *testing.T) {
	t.Parallel()
	svc := keyedNode("repository", "repository:svc", "acme/svc")
	pr := keyedNode("pull_request", "pull_request:svc:1", "acme/svc")
	rows := make([]row, 0, keyedLinkRowLimit+1)
	for i := range keyedLinkRowLimit + 1 {
		rows = append(rows, keyedRow(svc, pr, keyedNode("work_item", fmt.Sprintf("work_item.v2:0:linear:X-%d", i), noRepositoryScope), "native"))
	}
	scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/svc"}}
	fake, seen := keyedFake(rows)
	adapter := newFakeAdapter(t, fake)
	tiers, complete, err := adapter.linkScopedWorkItemsOf(context.Background(), "key", storage.Principal{OrgID: "org_1"}, scope, []string{"work_item.v2:0:linear:X-1"})
	if err != nil || complete || tiers != nil {
		t.Fatalf("tiers %d complete %t err %v, want a cut read", len(tiers), complete, err)
	}
	*seen = nil
	tiers, complete, err = adapter.linkScopedWorkItemsOf(context.Background(), "key", storage.Principal{OrgID: "org_1"}, scope, nil)
	if err != nil || !complete || len(tiers) != 0 || len(*seen) != 0 {
		t.Fatalf("no ids: tiers %v complete %t err %v reads %d, want an empty, complete answer with no read", tiers, complete, err, len(*seen))
	}
}

// FalkorDB drops the rows past its RESULTSET_SIZE with no error, so a bound at
// or past it would read a capped result as complete (codex r1 on #1929).
func TestTheKeyedLinkReadBoundStaysUnderTheServerCap(t *testing.T) {
	t.Parallel()
	if keyedLinkRowLimit+1 >= falkorResultSetSize {
		t.Fatalf("keyedLinkRowLimit %d asks for %d rows, at or past FalkorDB's result cap of %d: a capped read would read as complete", keyedLinkRowLimit, keyedLinkRowLimit+1, falkorResultSetSize)
	}
}
