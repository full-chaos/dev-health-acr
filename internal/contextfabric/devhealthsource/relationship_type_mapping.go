package devhealthsource

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// dependencyRelationshipMapping is the source-side translation for
// work_item_dependencies.relationship_type values that name a relationship
// the v1 vocabulary already has, but from the opposite direction.
//
// CHAOS-4874/CHAOS-4571: the producer used to cast
// strings.ToUpper(relationship_type) straight into
// ContextFabricRelationshipType and let ContextFabricProjectionBatch.Validate
// decide. Because Validate is all-or-nothing, ONE row naming a type outside
// the 12-member vocabulary rejected the whole page, the coordinator held its
// checkpoint, and the same page failed identically every tick -- the
// organization's projection was wedged permanently. A production
// organization sat in exactly that state with 1,296 EXTERNAL_ISSUE_KEY,
// 15 BLOCKED_BY and 1 IS_BLOCKED_BY rows behind its cursor.
//
// This table is deliberately TINY and holds ONLY inversions: a value that
// means an existing vocabulary member with the endpoints reversed. It is not
// an allowlist. A value that is already a valid member passes through
// untouched, and anything else is quarantined per-item (see
// item_quarantine.go) rather than poisoning its batch -- so this table never
// has to be exhaustive to be safe, which is the property that makes the wedge
// class impossible rather than merely less likely.
//
// BLOCKED_BY and IS_BLOCKED_BY are the exact inverse of BLOCKS: "A is blocked
// by B" and "B blocks A" state the same fact. Emitting them as BLOCKS with
// From/To swapped means both spellings converge on ONE edge with ONE
// relationship id, rather than producing a second, contradictory edge in the
// opposite direction. chris ruled this mapping on 2026-09-02; EXTERNAL_ISSUE_KEY
// is deliberately NOT here -- it is quarantined and counted now, and is being
// considered as a vocabulary member under its own ticket.
var dependencyRelationshipMapping = map[string]struct {
	mapped contractsv1.ContextFabricRelationshipType
	// swapEndpoints is true when the source spelling names the relationship
	// from the opposite side, so From/To must be exchanged for the mapped
	// type to state the same fact.
	swapEndpoints bool
}{
	"BLOCKED_BY":    {mapped: contractsv1.ContextFabricRelationshipBlocks, swapEndpoints: true},
	"IS_BLOCKED_BY": {mapped: contractsv1.ContextFabricRelationshipBlocks, swapEndpoints: true},
}

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
	upper := strings.ToUpper(strings.TrimSpace(raw))
	if mapping, ok := dependencyRelationshipMapping[upper]; ok {
		return mapping.mapped, mapping.swapEndpoints, string(mapping.mapped)
	}
	return contractsv1.ContextFabricRelationshipType(upper), false, raw
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
