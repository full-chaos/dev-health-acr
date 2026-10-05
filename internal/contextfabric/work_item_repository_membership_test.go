package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func beginTreeMembership(t *testing.T, graph *treeGraphFake, filter TreeWorkItemFilter, request WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
	t.Helper()
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	request.Anchor = WorkItemMembershipAnchor{Subject: repositoryWorkItemAnchor}
	return NewTreeWorkItemMembership(graph, filter, gate).Begin(context.Background(), storage.Principal{OrgID: "org-1"}, ResolvedGraphBinding{}, RequestedScope{}, request)
}

func TestTheTreeMembershipHandsOnTheServingCapAndCountsEveryAuthorizedMember(t *testing.T) {
	var members []TreeWorkItemMember
	for i := 0; i < 250; i++ {
		members = append(members, treeMember(t, fmt.Sprintf("ENG-%04d", i), TreeLinkTierNative))
	}
	lease, result, err := beginTreeMembership(t, &treeGraphFake{walk: TreeWorkItemWalk{Members: members, PullRequests: 5, LinkedIssues: 250, Denied: 3}}, nil, WorkItemMembershipRequest{})
	if err != nil || lease == nil {
		t.Fatalf("lease=%v err=%v", lease, err)
	}
	defer lease.Release()
	if len(result.Members) != WorkItemMembershipServeLimit || result.Census.AuthorizedPopulation != 250 || result.Census.State != WorkItemMembershipCensusExact || !result.Census.PopulationComplete {
		t.Fatalf("members=%d census=%+v, want the serve limit handed on over an exact count of 250", len(result.Members), result.Census)
	}
	if result.Census.DeniedPopulation != 3 || result.Census.RepositoryPullRequests != 5 || result.Census.RepositoryLinkedIssues != 250 {
		t.Fatalf("census = %+v", result.Census)
	}
	first := result.Members[0]
	if first.LinkTier != TreeLinkTierNative || first.RepoID != treeZeroRepositoryID || first.WorkItemID != "ENG-0000" || first.RepoSlug != "" {
		t.Fatalf("member = %+v", first)
	}
}

func TestTheTreeMembershipFailsLoudlyWhenItCannotMeasure(t *testing.T) {
	member := []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}
	for name, tc := range map[string]struct {
		graph   *treeGraphFake
		filter  TreeWorkItemFilter
		request WorkItemMembershipRequest
	}{
		"walk error":                      {&treeGraphFake{err: errors.New("down")}, nil, WorkItemMembershipRequest{}},
		"filter missing":                  {&treeGraphFake{walk: TreeWorkItemWalk{Members: member}}, nil, WorkItemMembershipRequest{Status: "todo"}},
		"filter error":                    {&treeGraphFake{walk: TreeWorkItemWalk{Members: member}}, &treeFilterFake{err: errors.New("down")}, WorkItemMembershipRequest{Status: "todo"}},
		"created time has no fact filter": {&treeGraphFake{walk: TreeWorkItemWalk{Members: member}}, &treeFilterFake{}, WorkItemMembershipRequest{TimeColumn: "created_at"}},
	} {
		lease, result, err := beginTreeMembership(t, tc.graph, tc.filter, tc.request)
		if lease != nil {
			lease.Release()
		}
		if err == nil || result.Census.State != WorkItemMembershipCensusUnmeasured || result.Census.PopulationMeasured || len(result.Members) != 0 {
			t.Errorf("%s: err=%v result=%+v, want an unmeasured census with no member", name, err, result)
		}
	}
	if _, result, err := NewTreeWorkItemMembership(nil, nil, nil).Begin(context.Background(), storage.Principal{OrgID: "org-1"}, ResolvedGraphBinding{}, RequestedScope{}, WorkItemMembershipRequest{Anchor: WorkItemMembershipAnchor{Subject: repositoryWorkItemAnchor}}); err == nil || result.Census.State != WorkItemMembershipCensusUnmeasured {
		t.Errorf("an unwired read measured: err=%v result=%+v", err, result)
	}
	// A member whose identity cannot be served is an unread member, not a silent drop.
	bad := TreeWorkItemMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item:not-a-v2-id"}, Tier: TreeLinkTierNative}
	lease, result, err := beginTreeMembership(t, &treeGraphFake{walk: TreeWorkItemWalk{Members: []TreeWorkItemMember{member[0], bad}}}, nil, WorkItemMembershipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if len(result.Members) != 1 || result.Census.PopulationComplete || !result.Census.PopulationIncomplete {
		t.Fatalf("an unservable identity was dropped silently: %+v", result)
	}
}

// TestTheServingCapAndTheCensusCutKeepTheStrongestLinks: with more members
// than the serving cap, the members served are the strongest links, never the
// ones whose canonical ids sort first; they are still handed on in canonical
// id order. The census bound cuts the same way.
func TestTheServingCapAndTheCensusCutKeepTheStrongestLinks(t *testing.T) {
	var members []TreeWorkItemMember
	for i := 0; i < WorkItemMembershipServeLimit; i++ {
		members = append(members, treeMember(t, fmt.Sprintf("A-%04d", i), TreeLinkTierHeuristic))
	}
	for i := 0; i < 50; i++ {
		members = append(members, treeMember(t, fmt.Sprintf("Z-%04d", i), TreeLinkTierNative))
	}
	lease, result, err := beginTreeMembership(t, &treeGraphFake{walk: TreeWorkItemWalk{Members: members}}, nil, WorkItemMembershipRequest{})
	if err != nil || lease == nil {
		t.Fatalf("lease=%v err=%v", lease, err)
	}
	defer lease.Release()
	native := 0
	for i, m := range result.Members {
		if m.LinkTier == TreeLinkTierNative {
			native++
		}
		if i > 0 && result.Members[i-1].CanonicalID >= m.CanonicalID {
			t.Fatalf("served members are not in canonical id order at %d", i)
		}
	}
	if native != 50 || len(result.Members) != WorkItemMembershipServeLimit {
		t.Fatalf("served %d members with %d native, want the cap with all 50 native members", len(result.Members), native)
	}
	// The census bound: one past it, the member cut is a heuristic one.
	var over []TreeWorkItemMember
	for i := 0; i < WorkItemMembershipCensusLimit; i++ {
		over = append(over, treeMember(t, fmt.Sprintf("Z-%05d", i), TreeLinkTierNative))
	}
	over = append(over, treeMember(t, "A-00000", TreeLinkTierHeuristic))
	walked := strongestLinkFirst(over)[:WorkItemMembershipCensusLimit]
	for _, m := range walked {
		if m.Tier != TreeLinkTierNative {
			t.Fatalf("the census cut kept the heuristic member %s over a native one", m.Subject.CanonicalID)
		}
	}
}
