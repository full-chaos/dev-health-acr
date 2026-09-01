package v1

import "sort"

// ContextFabricSetCoverGroupGuard bounds the exact overlap-aware selection
// (CHAOS-4678) to small group counts, where an exact solve over subsets is
// cheap and trivially affordable (the ruling's own words: 3-8 teams is the
// stated small-org reality). Beyond the guard, SelectGroupCoverMembers falls
// back to the pre-CHAOS-4678 largest-group-round-robin order untouched,
// rather than risk an exponential blowup on a pathological org.
const ContextFabricSetCoverGroupGuard = 12

// SelectGroupCoverMembers picks which cohort members a member-budget admits
// out of a grouped cohort, exploiting overlap: group membership is
// many-to-many, and a member shared by several groups can cover all of them
// at once (CHAOS-4678).
//
// Worked example (the ticket's own): groups A={a,b}, B={b,c}, budget=1. The
// OLD largest-group-round-robin order keeps one member per group -- a and b,
// two members, over budget. This selects b alone, which covers both groups
// within the cap.
//
// The selection is EXACT for group counts up to ContextFabricSetCoverGroupGuard:
// it first finds the MINIMUM number of members that covers every group, then
// spends any budget left over on additional members, largest remaining group
// first. The cover step ignores the budget on purpose -- decision D2's floor
// ("every group survives with at least one member for as long as the budget
// admits any") is unconditional, exactly as the pre-CHAOS-4678 round-robin
// enforced it: a budget too small even for the minimum cover comes back
// OVER budget rather than dropping a group, and the caller's own downstream
// measurement is what turns that into a planned refusal. Overlap only ever
// LOWERS how many members the floor costs; it never raises it.
//
// Beyond the guard this falls back to the pre-CHAOS-4678 largest-group-
// round-robin order untouched, and reports which order it actually ran (see
// ContextFabricNarrowingBasis) so a caller's disclosure never claims an
// order that did not execute.
//
// DETERMINISM is a hard bar: two identical requests must select identical
// members. Every tie here resolves by canonical_id_lexical -- groups arrive
// pre-sorted by canonical id (BuildCohortGroups), and both the exact solve
// and the greedy fill iterate members in canonical_id_lexical order, so the
// first minimal selection found is always the same one, run to run.
func SelectGroupCoverMembers(groups []ContextFabricCohortGroup, ungroupedMemberIDs []string, maxMembers int) (map[string]struct{}, ContextFabricNarrowingBasis) {
	selected := make(map[string]struct{}, maxMembers)
	if maxMembers <= 0 || len(groups) == 0 {
		return selected, ContextFabricNarrowingBasisLargestGroupRoundRobin
	}

	// remaining pools start as every group's FULL member list; the cover
	// phase (when it runs) strips the members it chose before the fill
	// phase below ever sees them, so the two phases never double-select.
	remaining := make([][]string, len(groups))
	for index, group := range groups {
		remaining[index] = append([]string(nil), group.MemberCanonicalIDs...)
	}
	ungrouped := append([]string(nil), ungroupedMemberIDs...)

	basis := ContextFabricNarrowingBasisLargestGroupRoundRobin
	if len(groups) <= ContextFabricSetCoverGroupGuard {
		basis = ContextFabricNarrowingBasisOverlapAwareSetCover
		for _, id := range minimumCoverSelection(groups) {
			selected[id] = struct{}{}
			removeFromPools(remaining, id)
		}
	}
	fillLargestRemainingFirst(remaining, ungrouped, selected, maxMembers)
	return selected, basis
}

// minimumCoverSelection returns the SMALLEST set of members whose union of
// owning groups is every group -- the unconditional D2 floor, deliberately
// computed without regard to the caller's budget (see SelectGroupCoverMembers).
// A bitmask DP over the groups (group count bounded by
// ContextFabricSetCoverGroupGuard): dp[mask] is the minimum member count
// whose union of covered groups is exactly mask, built by relaxing masks in
// ascending numeric order, which is always a valid topological order here
// because OR-ing in a member's group bits can only hold a mask's bits steady
// or grow them. dp[full] is always reachable: every group is non-empty
// (BuildCohortGroups never emits an empty one), so picking any one member per
// group trivially covers every bit.
//
// Ties resolve deterministically: members are iterated in canonical_id_lexical
// order and a transition is applied only on a STRICT improvement, so the
// first (and therefore lexically-earliest) member reaching a given mask at
// the lowest cost is the one kept, run to run.
func minimumCoverSelection(groups []ContextFabricCohortGroup) []string {
	k := len(groups)
	full := (1 << k) - 1

	memberMask := make(map[string]int)
	for index, group := range groups {
		for _, id := range group.MemberCanonicalIDs {
			memberMask[id] |= 1 << index
		}
	}
	members := make([]string, 0, len(memberMask))
	for id := range memberMask {
		members = append(members, id)
	}
	sort.Strings(members)

	const unreached = 1<<31 - 1
	dp := make([]int, full+1)
	parentMask := make([]int, full+1)
	parentMember := make([]string, full+1)
	for mask := range dp {
		dp[mask] = unreached
	}
	dp[0] = 0
	for mask := 0; mask <= full; mask++ {
		if dp[mask] == unreached {
			continue
		}
		for _, id := range members {
			next := mask | memberMask[id]
			if dp[mask]+1 < dp[next] {
				dp[next] = dp[mask] + 1
				parentMask[next] = mask
				parentMember[next] = id
			}
		}
	}

	if full == 0 || dp[full] == unreached {
		return nil
	}
	picked := make([]string, 0, dp[full])
	for mask := full; mask != 0; mask = parentMask[mask] {
		picked = append(picked, parentMember[mask])
	}
	return picked
}

// fillLargestRemainingFirst spends any budget left after the cover phase on
// additional members, taking one from the largest remaining pool at a time:
// groups first (in their own canonical-id-lexical order, set by
// BuildCohortGroups), then ungrouped members, each as a singleton pool. A
// SIZE TIE between a group's leftover pool and an ungrouped member's
// singleton favors the ungrouped member: an ungrouped member has no other
// channel through which its absence is disclosed, while a second member of a
// group the cover phase already gave a representative to is a group whose
// Truncated flag already says something was cut. Admitting the ungrouped
// member on a tie preserves strictly more information.
//
// remaining must already exclude every member the cover phase selected --
// SelectGroupCoverMembers strips them before calling this -- so the two
// phases never compete for the same member. Within this phase, selecting a
// member shared by two pools removes it from BOTH immediately, so a later
// pool never re-offers a member this pass already spent budget on.
func fillLargestRemainingFirst(remaining [][]string, ungrouped []string, selected map[string]struct{}, maxMembers int) {
	pools := make([][]string, 0, len(remaining)+len(ungrouped))
	for _, id := range ungrouped {
		pools = append(pools, []string{id})
	}
	pools = append(pools, remaining...)
	for progressed := true; len(selected) < maxMembers && progressed; {
		progressed = false
		order := make([]int, 0, len(pools))
		for index := range pools {
			if len(pools[index]) > 0 {
				order = append(order, index)
			}
		}
		sort.SliceStable(order, func(i, j int) bool { return len(pools[order[i]]) > len(pools[order[j]]) })
		for _, index := range order {
			if len(selected) >= maxMembers {
				break
			}
			if len(pools[index]) == 0 {
				// Emptied earlier in this same pass by a shared-member
				// removal below.
				continue
			}
			candidate := pools[index][0]
			pools[index] = pools[index][1:]
			removeFromPools(pools, candidate)
			if _, already := selected[candidate]; already {
				continue
			}
			selected[candidate] = struct{}{}
			progressed = true
		}
	}
}

// removeFromPools strips one member id out of every pool that carries it. A
// no-op on a pool that does not.
func removeFromPools(pools [][]string, id string) {
	for index, pool := range pools {
		for position, candidate := range pool {
			if candidate == id {
				pools[index] = append(pool[:position], pool[position+1:]...)
				break
			}
		}
	}
}
