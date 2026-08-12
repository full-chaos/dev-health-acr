package graphrank

import (
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// MergeFactRequirements dedups additional fact kinds into an already-built
// requirement list and returns the result sorted by Kind.
//
// Codex P2f: this was originally duplicated per-backend-adapter (zepgraph and
// falkorgraph each carried their own private copy of this exact function),
// and the two copies had already silently diverged -- falkorgraph's copy was
// missing the final sort.Slice zepgraph's had, so the SAME investigation
// against the two backends could report FactRequirements in a different,
// externally visible order purely because of which graph backend happened to
// be configured. Hoisting the one true implementation into graphrank (shared,
// backend-neutral decision logic, same as AdmitEdges/DiscoveredCohort) makes
// that divergence structurally impossible going forward, which is a stronger
// guarantee than a parity test alone: every adapter that calls this function
// is provably ordering-identical by construction, not merely by having been
// checked once. See merge_test.go's TestMergeFactRequirementsOrderingParity
// for the assertion Codex's ruling asked for.
func MergeFactRequirements(existing []contextfabric.FactRequirement, kinds ...contextfabric.FactKind) []contextfabric.FactRequirement {
	seen := make(map[contextfabric.FactKind]bool, len(existing)+len(kinds))
	result := append([]contextfabric.FactRequirement(nil), existing...)
	for _, requirement := range existing {
		seen[requirement.Kind] = true
	}
	for _, kind := range kinds {
		if !seen[kind] {
			seen[kind] = true
			result = append(result, contextfabric.FactRequirement{Kind: kind})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind < result[j].Kind })
	return result
}
