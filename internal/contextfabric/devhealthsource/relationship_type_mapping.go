package devhealthsource

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The alias table lives in internal/contextfabric/dependencyrelation (see its
// package doc and the CHAOS-4874/4571/7150 history there).

// dependencyRelationshipType translates one raw
// work_item_dependencies.relationship_type value.
//
// It returns the wire type to emit, whether the endpoints must be swapped,
// and the identity spelling to derive the relationship id from. The identity
// spelling matters for replay: for an UNMAPPED value it is the raw column
// value, byte-identical to what this producer has always passed to
// identity.DeriveRelationship, so no already-projected edge changes id. For a
// mapped value it is the mapped type's own spelling, so an inverted row and
// the equivalent forward row derive the SAME id and converge on one edge --
// and no already-projected edge is affected, because a row that would take
// this branch could never be projected at all before this change.
func dependencyRelationshipType(raw string) (typ contractsv1.ContextFabricRelationshipType, swapEndpoints bool, identitySpelling string) {
	return dependencyrelation.Resolve(raw)
}

// orientDependencyEndpoints exchanges a dependency edge's endpoints when the
// source spelling names the relationship from the opposite side. Kept beside
// the mapping table so the swap and the table that decides it cannot drift:
// every caller that consults dependencyRelationshipType for a type must
// orient with the same boolean, for the wire endpoints AND for the id it
// derives, or an inverted row would carry one and not the other.
func orientDependencyEndpoints(from, to contractsv1.ContextFabricSubjectRef, swapEndpoints bool) (contractsv1.ContextFabricSubjectRef, contractsv1.ContextFabricSubjectRef) {
	if swapEndpoints {
		return to, from
	}
	return from, to
}

// Relationship types of work_item_dependencies by disposition:
//   - mapped: translated to a contract type by dependencyrelation.
//   - ignored: ignoredDependencyTypes; the row advances the cursor, projects
//     nothing and is counted under its type at Info, never quarantined.
//   - quarantined: any other value outside the vocabulary (WARN per item).
//
// external_issue_key is ignored because its target is an unresolved external
// key, not a work item, and a key prefix alone is not linked-issue inheritance.
var ignoredDependencyTypes = map[string]struct{}{"external_issue_key": {}}

func ignoredDependencyType(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	_, ok := ignoredDependencyTypes[normalized]
	return normalized, ok
}
