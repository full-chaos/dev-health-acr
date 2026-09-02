// Package p1repro is a committed RED fixture found by codex round 1, P1
// (EXECUTED) against this gate itself, not against the original CHAOS-4735
// sweep -- it is a NEW class, not a re-find. Not production code; loaded
// standalone by TestFamilyTextGateCatchesHistoricalConstructions.
//
// The construction: the map key is a STRUCT that WRAPS a family-typed
// field, rather than the family type itself. The map-type structural rule
// (a map literal whose declared KEY TYPE is family-identical) does not
// fire, because the key type is `wrappedKey`, not the family type. Without
// composite-literal taint propagation, the literal `wrappedKey{Family:
// family}` built at the lookup site also evaluated as untainted, so
// indexing the map with it was invisible to the general index rule too.
// Composite literals now propagate taint from any tainted field/element,
// closing this: the wrapped key is tainted-derived, and indexing a
// text-valued map with it is flagged the same way an unwrapped family key
// would be.
package p1repro

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

type wrappedKey struct {
	Family contractsv1.ContextFabricQuestionFamily
}

var table = map[wrappedKey]string{
	wrappedKey{Family: contractsv1.ContextFabricQuestionFamilySubjectInvestigation}: "ask about one subject",
}

// Lookup reproduces the composite-key construction: family is wrapped in a
// struct at the call site, then used to index a text-valued table.
func Lookup(family contractsv1.ContextFabricQuestionFamily) string {
	return table[wrappedKey{Family: family}]
}
