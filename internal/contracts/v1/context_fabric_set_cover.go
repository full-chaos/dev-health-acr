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
// first. Beyond the guard, the cover step falls back to a CHEAP (not
// necessarily minimum) cover -- one member per group, lexically smallest --
// rather than the exponential exact solve; either way, EVERY group gets a
// floor member before the guard even applies. The floor is unconditional and
// ignores the budget on purpose: decision D2's floor ("every group survives
// with at least one member for as long as the budget admits any") holds
// exactly as the pre-CHAOS-4678 round-robin enforced it, guard or no guard --
// a budget too small even for the floor comes back OVER budget rather than
// dropping a group, and the caller's own downstream measurement is what
// turns that into a planned refusal. Overlap only ever LOWERS how many
// members the floor costs; it never raises it, and never removes it.
//
// Beyond the guard, the basis reported is largest_group_round_robin -- the
// cheap floor plus the same greedy fill the pre-CHAOS-4678 order used --
// never overlap_aware_set_cover, which would claim an order that did not
// run (see ContextFabricNarrowingBasis).
//
// DETERMINISM is a hard bar: two identical requests must select identical
// members. Every tie here resolves by canonical_id_lexical: within the guard,
// minimumCoverSelection reconstructs the LEXICOGRAPHICALLY SMALLEST minimum
// cover (not merely *a* deterministic one -- see its own doc comment for why
// that distinction needed a second pass); beyond it, the cheap floor takes
// each group's lexically smallest member; the leftover-budget fill iterates
// pools in canonical_id_lexical order throughout.
func SelectGroupCoverMembers(groups []ContextFabricCohortGroup, ungroupedMemberIDs []string, maxMembers int) (map[string]struct{}, ContextFabricNarrowingBasis) {
	selected := make(map[string]struct{}, maxMembers)
	if maxMembers <= 0 || len(groups) == 0 {
		return selected, ContextFabricNarrowingBasisLargestGroupRoundRobin
	}

	// remaining pools start as every group's FULL member list; the floor
	// step below strips the members it chose before the fill phase ever
	// sees them, so the two phases never double-select.
	remaining := make([][]string, len(groups))
	for index, group := range groups {
		remaining[index] = append([]string(nil), group.MemberCanonicalIDs...)
	}
	ungrouped := append([]string(nil), ungroupedMemberIDs...)

	basis := ContextFabricNarrowingBasisLargestGroupRoundRobin
	var floor []string
	if len(groups) <= ContextFabricSetCoverGroupGuard {
		basis = ContextFabricNarrowingBasisOverlapAwareSetCover
		floor = minimumCoverSelection(groups)
	} else {
		floor = oneMemberPerGroup(groups)
	}
	for _, id := range floor {
		selected[id] = struct{}{}
		removeFromPools(remaining, id)
	}
	fillLargestRemainingFirst(remaining, ungrouped, selected, maxMembers)
	return selected, basis
}

// oneMemberPerGroup is the CHEAP, non-exact floor used beyond
// ContextFabricSetCoverGroupGuard: one member per group, the lexically
// smallest, in O(members) rather than the exact solve's O(2^k * members).
// It does not exploit overlap -- that is exactly what the guard trades away
// to avoid an exponential blowup -- but it still guarantees the
// unconditional D2 floor no group loses its only representative.
func oneMemberPerGroup(groups []ContextFabricCohortGroup) []string {
	picked := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.MemberCanonicalIDs) == 0 {
			continue
		}
		smallest := group.MemberCanonicalIDs[0]
		for _, id := range group.MemberCanonicalIDs[1:] {
			if id < smallest {
				smallest = id
			}
		}
		picked = append(picked, smallest)
	}
	return picked
}

// minimumCoverSelection returns the LEXICOGRAPHICALLY SMALLEST minimum-size
// set of members whose union of owning groups is every group -- the
// unconditional D2 floor when the group count is within
// ContextFabricSetCoverGroupGuard, deliberately computed without regard to
// the caller's budget (see SelectGroupCoverMembers).
//
// Two passes over a bitmask DP (group count bounded by the guard):
//
//  1. coverCost[mask] is the minimum number of members whose union covers
//     AT LEAST the bits in mask, for every mask from 0 to full. The
//     recurrence picks one member at a time and recurses on whatever of
//     mask that member left uncovered (mask &^ memberMask[id]), which is
//     always a strictly smaller mask when the member covers any bit of
//     mask at all -- so relaxing mask in ascending numeric order is a valid
//     topological order.
//  2. Reconstruction walks from `full` down to 0, and at each step tries
//     candidate members in canonical_id_lexical order, taking the FIRST one
//     whose use does not cost more than optimal (coverCost of what is left
//     after using it is exactly one less than the current target's cost).
//     That is the standard technique for recovering the
//     lexicographically-smallest optimum from a cost table: always prefer
//     the earliest candidate that provably does not sacrifice optimality,
//     never merely the first one a single-path DP transition happened to
//     record.
//
// The distinction matters and was NOT hypothetical: a naive DP that
// reconstructs via "first transition that achieved dp[mask] over the
// ascending-mask relaxation order" is deterministic (same input always
// gives the same output) but not lexicographically smallest -- which
// member's mask reaches a given intermediate state first depends on group
// BIT-INDEX arithmetic, not on the member's own canonical id, and the two
// do not correlate. Groups A={a,b}, B={a,c}, C={c,d} with a 2-member cover
// is the smallest reproducing case: {a,c} and {b,c} are both valid minimum
// covers, {a,c} is lexically smaller, and the naive reconstruction returned
// {b,c} (codex round 1, finding 2, EXECUTED).
func minimumCoverSelection(groups []ContextFabricCohortGroup) []string {
	k := len(groups)
	full := (1 << k) - 1
	if full == 0 {
		return nil
	}

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
	coverCost := make([]int, full+1)
	for mask := 1; mask <= full; mask++ {
		best := unreached
		for _, id := range members {
			m := memberMask[id]
			if m&mask == 0 {
				// This member covers none of what's still needed; using it
				// here can never be part of an optimal cover of `mask`.
				continue
			}
			sub := mask &^ m
			if coverCost[sub]+1 < best {
				best = coverCost[sub] + 1
			}
		}
		coverCost[mask] = best
	}
	if coverCost[full] == unreached {
		// Every group is non-empty (BuildCohortGroups never emits an empty
		// one), so picking any one member per group trivially covers every
		// bit; this is unreachable in practice and defensive only.
		return nil
	}

	picked := make([]string, 0, coverCost[full])
	remaining := full
	for remaining != 0 {
		target := coverCost[remaining]
		for _, id := range members {
			m := memberMask[id]
			if m&remaining == 0 {
				continue
			}
			sub := remaining &^ m
			if coverCost[sub]+1 == target {
				picked = append(picked, id)
				remaining = sub
				break
			}
		}
	}
	return picked
}

// fillLargestRemainingFirst spends any budget left after the floor step on
// additional members, taking one from the largest remaining pool at a time:
// groups first (in their own canonical-id-lexical order, set by
// BuildCohortGroups), then ungrouped members, each as a singleton pool. A
// SIZE TIE between a group's leftover pool and an ungrouped member's
// singleton favors the ungrouped member: an ungrouped member has no other
// channel through which its absence is disclosed, while a second member of a
// group the floor step already gave a representative to is a group whose
// Truncated flag already says something was cut. Admitting the ungrouped
// member on a tie preserves strictly more information.
//
// remaining must already exclude every member the floor step selected --
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
