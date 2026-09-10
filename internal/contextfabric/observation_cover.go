package contextfabric

import "sort"

// THE OBSERVATION COVER: how many DISTINCT OBSERVATIONS a set of served fact
// kinds actually represents, at one subject kind.
//
// THE RULING IT IMPLEMENTS. "Two fact kinds backed by one observation are one
// source; the registry declares the observation key so the evaluator can
// tell." The registry half is FactCapability.ObservationKey. This file is the
// evaluator half: every completion threshold is compared against this count,
// never against a raw fact-KIND count, because two kinds that are one
// observation must not corroborate each other.
//
// WHY A COVER AND NOT A DISTINCT-KEY COUNT. The declaration is LIST-valued per
// (kind, subject kind) cell -- a kind may be one-hop built from several
// independent partners at once. `operational_deficiencies` at subject kind
// team is the measured case: recommendations_daily is built from
// compounding_risk_daily (health), from work_item_metrics_daily (flow) AND
// from team_metrics_daily (metrics), three unrelated tables. A SCALAR key
// cannot express that without collapsing health, flow and metrics onto each
// other by transitivity, which is false -- none is built from another.
//
// That is the partial-overlap limit the plan of record recorded as a property
// of a scalar declaration ("observations A={x}, B={y}, C={x,y}: a declaration
// that collapses A/C and B/C necessarily collapses A/B, which share nothing.
// Any key or group defines a partition"). A LIST-valued label is neither a key
// nor a group and defines no partition, so it carries partial overlap. The
// limit is SUPERSEDED BY SHAPE, not worked around; ruled 2026-09-10.
//
// Once labels are set-valued, "count the distinct keys" is no longer a
// well-defined question -- {risk}, {throughput} and {risk,throughput,
// sustainability} have three distinct SETS and two distinct KEYS and neither
// number is the answer. The ruled predicate is the MINIMUM NUMBER OF DISTINCT
// OBSERVATIONS THAT COVERS EVERY SERVED KIND: the smallest set of observations
// such that every served kind carries at least one of them. Worked, at team:
//
//	od + health          -> 1  (risk alone covers both)
//	od + health + flow   -> 2  (health forces risk, flow forces throughput;
//	                            od is then covered by either)
//
// MONOTONICITY, which is D15's own acceptance and the reason the minimum is
// the right statistic rather than any cheaper one. The cover is monotone
// NON-DECREASING in the served set: adding a served kind can never lower it,
// removing one can never raise it. So removing a producer cannot improve
// completeness, and a duplicate adapter -- a kind whose labels are already
// covered -- cannot create corroboration. Both directions are pinned.
//
// AN UNKEYED KIND IS ITS OWN OBSERVATION. A served kind with no declared label
// at this subject kind shares an observation with nothing, so it contributes
// exactly one and is never folded into the cover. Treating "no declaration" as
// "no observation" would silently drop it from every count -- the missing-is-
// not-zero distinction -- and treating it as a wildcard would let it be
// covered by an unrelated observation. It is neither: it is a singleton.

// observationCoverKindGuard bounds the exact solve. The DP is O(2^k * o) over
// k KEYED served kinds, so the bound is on k, not on the registry's size.
//
// IT IS NOT A RUNTIME FALLBACK, and that is deliberate. The number of keyed
// kinds is a property of a STATIC REGISTRY DECLARATION, known when the
// registry is built, so a registry that outgrows this guard is a design event
// that must fail loudly at construction -- see the totality assertion beside
// the declaration. A cheap runtime fallback would have to over-count (every
// approximation to minimum set cover is an upper bound), and an over-count
// reports MORE distinct sources than exist, which is the exact defect this
// whole mechanism was built to remove. There is no safe silent degradation
// here, so there is none.
const observationCoverKindGuard = 20

// observationKeyAssignment is a SNAPSHOT of the registry's observation-key
// declarations, captured at the same moment as the evaluation that reads it.
//
// It is a snapshot rather than a live registry handle for the reason D19's
// comparisonStandards is: an operand evaluated against one registry state and
// compared against another would produce a row no single registry state ever
// justified. A caller holds the assignment for the whole evaluation or it
// holds nothing.
type observationKeyAssignment map[FactKind]map[SubjectKind][]ObservationKey

// observationCover returns the minimum number of distinct observations that
// covers every kind in served, at subject kind subject.
//
// Duplicates in served are ignored: the same kind twice is the same kind. The
// result is 0 exactly when served is empty of distinct kinds.
func observationCover(served []FactKind, subject SubjectKind, assignment observationKeyAssignment) int {
	seen := make(map[FactKind]bool, len(served))
	keyed := make([][]ObservationKey, 0, len(served))
	singletons := 0
	for _, kind := range served {
		if seen[kind] {
			continue
		}
		seen[kind] = true
		labels := dedupeObservationKeys(assignment[kind][subject])
		if len(labels) == 0 {
			// Unkeyed: its own observation, never folded into the cover.
			singletons++
			continue
		}
		keyed = append(keyed, labels)
	}
	if len(keyed) == 0 {
		return singletons
	}
	return singletons + minimumObservationCover(keyed)
}

func dedupeObservationKeys(labels []ObservationKey) []ObservationKey {
	if len(labels) == 0 {
		return nil
	}
	seen := make(map[ObservationKey]bool, len(labels))
	out := make([]ObservationKey, 0, len(labels))
	for _, label := range labels {
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

// minimumObservationCover is the exact minimum set cover over keyed kinds.
//
// Elements are the KINDS (bit i = keyed[i] is covered); sets are the
// OBSERVATIONS (each observation's mask is the kinds that declare it). The DP
// relaxes masks in ascending numeric order, which is a valid topological order
// because using an observation that covers any bit of the current mask always
// leaves a strictly smaller mask.
//
// Every keyed kind carries at least one label by construction (observationCover
// routes the label-less ones to singletons before this is called), so every
// bit of `full` is reachable and dp[full] is always finite. That is the
// same trap the shipped cohort cover hit from the other side -- an element no
// set could cover made its dp[full] unreachable and voided the whole result --
// and it is closed here by construction rather than by a guard clause, because
// an unkeyed kind is a different THING (a singleton), not an uncoverable one.
func minimumObservationCover(keyed [][]ObservationKey) int {
	// Observation -> mask of the keyed kinds that declare it.
	masks := map[ObservationKey]uint32{}
	for index, labels := range keyed {
		for _, label := range labels {
			masks[label] |= 1 << uint(index)
		}
	}
	if len(keyed) > observationCoverKindGuard {
		// Unreachable in a validated registry -- the declaration-side
		// totality assertion refuses a registry this large. Counting one
		// observation per keyed kind here is the value the cover can never
		// exceed, so a build that somehow reached this line reports the
		// LEAST corroboration consistent with the declaration rather than
		// the most.
		return len(keyed)
	}
	// Deterministic iteration order: the DP's result is order-independent,
	// but a map walk is not, and a count that depended on map order would be
	// a flake nobody could reproduce.
	ordered := make([]ObservationKey, 0, len(masks))
	for label := range masks {
		ordered = append(ordered, label)
	}
	sort.Slice(ordered, func(a, b int) bool { return ordered[a] < ordered[b] })

	full := uint32(1)<<uint(len(keyed)) - 1
	const unreachable = int(^uint(0) >> 1)
	cost := make([]int, full+1)
	for mask := uint32(1); mask <= full; mask++ {
		cost[mask] = unreachable
	}
	for mask := uint32(1); mask <= full; mask++ {
		for _, label := range ordered {
			covered := masks[label] & mask
			if covered == 0 {
				continue
			}
			remainder := cost[mask&^covered]
			if remainder != unreachable && remainder+1 < cost[mask] {
				cost[mask] = remainder + 1
			}
		}
	}
	return cost[full]
}
