package devhealthsource

import (
	"sort"
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
// spelling is the trimmed ASCII lower-case type, so case and padding variants
// of one type for one pair derive one id (version v18; an edge projected from
// any other spelling carries the old id until the rebuild). For a mapped value
// it is the mapped type's own lower-case spelling, so an inverted row and the
// equivalent forward row derive the SAME id and converge on one edge.
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

// ignoredDependencyTypeNames is ignoredDependencyTypes in order.
func ignoredDependencyTypeNames() []string {
	names := make([]string, 0, len(ignoredDependencyTypes))
	for name := range ignoredDependencyTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ignoredDependencyType(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	_, ok := ignoredDependencyTypes[normalized]
	return normalized, ok
}
