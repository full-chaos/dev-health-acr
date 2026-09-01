package v1

import "testing"

// TestSelectGroupCoverMembersWorkedExample pins CHAOS-4678's own worked
// example: groups A={a,b}, B={b,c}, budget=1. The pre-CHAOS-4678
// largest-group-round-robin order kept one member per group -- a and b, two
// members, over budget. The overlap-aware selection instead picks the SHARED
// member, b, which covers both groups within the cap.
//
// This is the ticket's red-first proof: on origin/main (pre-CHAOS-4678),
// NarrowGroupedCohort(cohort, 1) against this exact fixture returns TWO
// members ({a, b}), which is the defect this ticket exists to fix -- verified
// directly against origin/main in a detached worktree, recorded in the PR
// body and the codex review context file rather than duplicated here, since
// the old 3-return signature cannot compile against this branch's 4-return
// one. Mutation coverage: reverting SelectGroupCoverMembers to call
// fillLargestRemainingFirst directly, without minimumCoverSelection first
// (i.e. no cover phase at all), reproduces the OLD round-robin's every-group
// floor and this test fails, because it never exploits the shared member.
func TestSelectGroupCoverMembersWorkedExample(t *testing.T) {
	t.Parallel()
	groups := []ContextFabricCohortGroup{
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "A", Label: "A"}, MemberCanonicalIDs: []string{"a", "b"}, Complete: true, Total: 2},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "B", Label: "B"}, MemberCanonicalIDs: []string{"b", "c"}, Complete: true, Total: 2},
	}
	selected, basis := SelectGroupCoverMembers(groups, nil, 1)
	if len(selected) != 1 {
		t.Fatalf("selected %d members within a 1-member budget, want 1: %v", len(selected), selected)
	}
	if _, ok := selected["b"]; !ok {
		t.Fatalf("selected = %v, want the shared member {b} alone, covering both groups", selected)
	}
	if basis != ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("basis = %q, want overlap_aware_set_cover", basis)
	}
}

// TestSelectGroupCoverMembersDeterminism proves the hard bar: two identical
// requests select identical members. Run repeatedly because Go's map
// iteration is randomized per-process -- a selection that happened to read a
// map in one order would still pass a single run and only show its
// nondeterminism across many (CHAOS-4630's own lesson, generalized here).
func TestSelectGroupCoverMembersDeterminism(t *testing.T) {
	t.Parallel()
	groups := []ContextFabricCohortGroup{
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "A", Label: "A"}, MemberCanonicalIDs: []string{"a1", "a2", "shared1"}, Complete: true, Total: 3},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "B", Label: "B"}, MemberCanonicalIDs: []string{"b1", "shared1", "shared2"}, Complete: true, Total: 3},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "C", Label: "C"}, MemberCanonicalIDs: []string{"c1", "shared2"}, Complete: true, Total: 2},
	}
	ungrouped := []string{"orphan1", "orphan2"}

	first, firstBasis := SelectGroupCoverMembers(groups, ungrouped, 3)
	for i := 0; i < 200; i++ {
		got, gotBasis := SelectGroupCoverMembers(groups, ungrouped, 3)
		if gotBasis != firstBasis {
			t.Fatalf("run %d: basis = %q, want %q", i, gotBasis, firstBasis)
		}
		if !sameSet(got, first) {
			t.Fatalf("run %d: selected = %v, want %v (identical requests must select identical members)", i, got, first)
		}
	}
}

// TestSelectGroupCoverMembersTiesBreakByCanonicalIDLexical: when two members
// are otherwise interchangeable for the minimum cover (both belong to
// exactly the same set of groups), the lexically smaller canonical id wins,
// every time -- the declared, stable order the ruling names.
func TestSelectGroupCoverMembersTiesBreakByCanonicalIDLexical(t *testing.T) {
	t.Parallel()
	groups := []ContextFabricCohortGroup{
		// "zzz" and "aaa" are both sole members of A, interchangeable for
		// cover purposes; canonical_id_lexical must always pick "aaa".
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "A", Label: "A"}, MemberCanonicalIDs: []string{"zzz", "aaa"}, Complete: true, Total: 2},
	}
	for i := 0; i < 50; i++ {
		selected, _ := SelectGroupCoverMembers(groups, nil, 1)
		if _, ok := selected["aaa"]; !ok {
			t.Fatalf("run %d: selected = %v, want the lexically smaller tie-break winner {aaa}", i, selected)
		}
	}
}

// TestSelectGroupCoverMembersFloorSurvivesEvenOverBudget generalizes decision
// D2's floor to the overlap-aware case: a budget too small to cover every
// group EVEN WITH MAXIMUM SHARING comes back OVER budget rather than
// dropping a group -- exactly as the pre-CHAOS-4678 round-robin behaved, and
// exactly what a stage-3 measurement (not this function) turns into a
// planned refusal.
//
// The shared member is deliberately placed LAST in each of A and B's member
// lists (codex round 2, finding 1, EXECUTED): with it first, the OLD
// per-group-independent peel algorithm discards each group's LAST element
// first and happens to converge on the shared member by coincidence of list
// order, not because it is overlap-aware -- making an earlier version of
// this test pass identically against origin/main's pre-CHAOS-4678 algorithm
// (own re-run: `NarrowGroupedCohort` on that exact fixture also returns
// `{shared, c1}`), which proved nothing about THIS change. With the shared
// member last, the old algorithm peels it away from BOTH groups
// independently and ends up with 3 members ({a1, b1, c1}, no sharing
// exploited); the new algorithm still finds the true 2-member minimum cover
// ({shared, c1}) regardless of list order, which is the genuinely
// differentiating assertion added below.
func TestSelectGroupCoverMembersFloorSurvivesEvenOverBudget(t *testing.T) {
	t.Parallel()
	groups := []ContextFabricCohortGroup{
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "A", Label: "A"}, MemberCanonicalIDs: []string{"a1", "shared"}, Complete: true, Total: 2},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "B", Label: "B"}, MemberCanonicalIDs: []string{"b1", "shared"}, Complete: true, Total: 2},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "C", Label: "C"}, MemberCanonicalIDs: []string{"c1"}, Complete: true, Total: 1},
	}
	selected, basis := SelectGroupCoverMembers(groups, nil, 1)
	if basis != ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("basis = %q, want overlap_aware_set_cover", basis)
	}
	// Every group must have AT LEAST ONE surviving member, even though that
	// costs more than the stated budget.
	for _, group := range groups {
		var covered bool
		for _, id := range group.MemberCanonicalIDs {
			if _, ok := selected[id]; ok {
				covered = true
			}
		}
		if !covered {
			t.Fatalf("group %q lost every member; selected = %v -- decision D2's floor forbids dropping a group", group.Subject.CanonicalID, selected)
		}
	}
	// THE DIFFERENTIATING ASSERTION: the true minimum cover is 2 members
	// (the shared member plus C's), not 3 -- proving the selection actually
	// exploited the overlap rather than merely floor-protecting each group
	// independently, which is all the old algorithm did.
	want := map[string]struct{}{"shared": {}, "c1": {}}
	if !sameSet(selected, want) {
		t.Fatalf("selected = %v, want the overlap-exploiting 2-member floor {shared,c1}, not a 3-member independent-peel floor", selected)
	}
}

// TestSelectGroupCoverMembersGuardFallback: beyond
// ContextFabricSetCoverGroupGuard, the selection falls back to the
// pre-CHAOS-4678 largest-group-round-robin order untouched, and reports THAT
// basis -- never overlap_aware_set_cover, which would claim an order that
// did not run.
func TestSelectGroupCoverMembersGuardFallback(t *testing.T) {
	t.Parallel()
	groupCount := ContextFabricSetCoverGroupGuard + 1
	groups := make([]ContextFabricCohortGroup, groupCount)
	for i := 0; i < groupCount; i++ {
		id := string(rune('A' + i))
		groups[i] = ContextFabricCohortGroup{
			Subject:            ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: id, Label: id},
			MemberCanonicalIDs: []string{id + "1"},
			Complete:           true,
			Total:              1,
		}
	}
	selected, basis := SelectGroupCoverMembers(groups, nil, groupCount)
	if basis != ContextFabricNarrowingBasisLargestGroupRoundRobin {
		t.Fatalf("basis = %q beyond the guard, want largest_group_round_robin (the exact solve must not run)", basis)
	}
	// Every group's sole member must still be admitted -- the fallback is
	// the OLD algorithm, not a degraded one.
	if len(selected) != groupCount {
		t.Fatalf("selected %d members, want all %d single-member groups covered by the fallback", len(selected), groupCount)
	}
}

// TestSelectGroupCoverMembersGuardFallbackFloorSurvivesEvenOverBudget pins
// codex round 1, finding 1 (P1, EXECUTED): beyond the guard, a budget
// TIGHTER than the group count must still never drop a group -- exactly the
// same unconditional floor the exact path guarantees. The first guard-
// fallback test above used a budget exactly equal to the group count, which
// never actually exercised the floor under pressure and passed either way;
// this is the tightened version that would have caught the regression.
func TestSelectGroupCoverMembersGuardFallbackFloorSurvivesEvenOverBudget(t *testing.T) {
	t.Parallel()
	groupCount := ContextFabricSetCoverGroupGuard + 1
	groups := make([]ContextFabricCohortGroup, groupCount)
	for i := 0; i < groupCount; i++ {
		id := string(rune('A' + i))
		groups[i] = ContextFabricCohortGroup{
			Subject:            ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: id, Label: id},
			MemberCanonicalIDs: []string{id + "1"},
			Complete:           true,
			Total:              1,
		}
	}
	// Budget is one LESS than the group count -- too tight to cover every
	// group even at 1 member each.
	selected, basis := SelectGroupCoverMembers(groups, nil, groupCount-1)
	if basis != ContextFabricNarrowingBasisLargestGroupRoundRobin {
		t.Fatalf("basis = %q beyond the guard, want largest_group_round_robin", basis)
	}
	for _, group := range groups {
		if _, ok := selected[group.MemberCanonicalIDs[0]]; !ok {
			t.Fatalf("group %q lost its only member under a tight budget beyond the guard; selected = %v -- the floor must hold even OVER budget, exactly as the exact path does", group.Subject.CanonicalID, selected)
		}
	}
	if len(selected) != groupCount {
		t.Fatalf("selected %d members, want all %d (over the %d-member budget, preserving the floor)", len(selected), groupCount, groupCount-1)
	}
}

// TestSelectGroupCoverMembersMinimumCoverIsLexicographicallySmallest pins
// codex round 1, finding 2 (P2, EXECUTED): among several equally-small
// minimum covers, the selection must be the LEXICOGRAPHICALLY SMALLEST set,
// not merely *a* deterministic one. Groups A={a,b}, B={a,c}, C={c,d} with a
// 2-member budget admit two distinct minimum covers, {a,c} and {b,c}; {a,c}
// is lexically smaller and must be the one chosen. A naive bitmask-DP
// reconstruction that just follows "the first transition that reached this
// state" is deterministic but ends up member-mask-order dependent rather
// than canonical_id_lexical, and returned {b,c} here.
func TestSelectGroupCoverMembersMinimumCoverIsLexicographicallySmallest(t *testing.T) {
	t.Parallel()
	groups := []ContextFabricCohortGroup{
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "A", Label: "A"}, MemberCanonicalIDs: []string{"a", "b"}, Complete: true, Total: 2},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "B", Label: "B"}, MemberCanonicalIDs: []string{"a", "c"}, Complete: true, Total: 2},
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "C", Label: "C"}, MemberCanonicalIDs: []string{"c", "d"}, Complete: true, Total: 2},
	}
	selected, basis := SelectGroupCoverMembers(groups, nil, 2)
	if basis != ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("basis = %q, want overlap_aware_set_cover", basis)
	}
	want := map[string]struct{}{"a": {}, "c": {}}
	if !sameSet(selected, want) {
		t.Fatalf("selected = %v, want the lexicographically smallest minimum cover {a,c} (not {b,c}, which is equally minimal but lexically larger)", selected)
	}
}

func sameSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
