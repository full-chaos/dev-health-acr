package panelharness

import "sort"

// BuildMemberRun assembles one PanelMemberRun from a member's raw panelist
// selections, computing Complete, DistinctIdentities, and the consensus
// summary -- the single place this package's REQUIRED INVARIANT (sol-max
// ruling, CHAOS-3860, 2026-08-20) is evaluated, so every caller (the live
// two-turn driver in run.go, and every test) shares one implementation
// rather than each re-deriving the same three checks slightly differently.
//
// expectedPanelists is the configured panel size for this run -- selections
// may be shorter than it (a panelist that errored or timed out contributes
// no PanelistSelection at all, never a zero-value placeholder, so a caller
// cannot mistake "no answer" for "answered the empty string"). Complete is
// true only when every configured panelist is actually present.
func BuildMemberRun(member string, expectedPanelists int, selections []PanelistSelection) PanelMemberRun {
	run := PanelMemberRun{
		Member:             member,
		Panelists:          selections,
		Complete:           expectedPanelists > 0 && len(selections) == expectedPanelists,
		DistinctIdentities: distinctCanonicalIdentities(selections),
		Consensus:          computeConsensus(selections),
	}
	return run
}

// distinctCanonicalIdentities reports whether every panelist's
// CanonicalModelIdentity is unique -- the ruling's "distinct canonical
// identities" invariant. An empty or single-panelist slice is vacuously
// distinct.
func distinctCanonicalIdentities(selections []PanelistSelection) bool {
	seen := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		if _, duplicate := seen[selection.CanonicalModelIdentity]; duplicate {
			return false
		}
		seen[selection.CanonicalModelIdentity] = struct{}{}
	}
	return true
}

// computeConsensus builds the ValueCounts histogram, the deterministic
// MajorityValue (ties broken on the lexicographically smaller value, never
// arrival order), and the AgreementBits slice parallel to selections.
func computeConsensus(selections []PanelistSelection) ConsensusSummary {
	counts := make(map[string]int, len(selections))
	for _, selection := range selections {
		counts[selection.AppliedValue]++
	}
	majority := majorityValue(counts)
	bits := make([]bool, len(selections))
	for i, selection := range selections {
		bits[i] = majority != "" && selection.AppliedValue == majority
	}
	return ConsensusSummary{ValueCounts: counts, MajorityValue: majority, AgreementBits: bits}
}

// majorityValue returns the value with the highest count in counts, ties
// broken on the lexicographically smaller value -- deterministic and
// reproducible from counts alone, so re-running this function on the same
// histogram (e.g. from a stored manifest, or after a retry that reordered
// panelist completion) always yields the identical answer. Empty map
// returns "".
func majorityValue(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	values := make([]string, 0, len(counts))
	for value := range counts {
		values = append(values, value)
	}
	sort.Strings(values)
	best := values[0]
	for _, value := range values[1:] {
		if counts[value] > counts[best] {
			best = value
		}
	}
	return best
}
