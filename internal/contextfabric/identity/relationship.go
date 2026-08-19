package identity

import (
	"crypto/sha256"
	"encoding/hex"
)

// KindWorkItemRef is the non-authoritative stub kind for an unresolved
// work_item dependency/hierarchy target (design brief §1.5, P5). Its
// canonical id form is "work_item_ref:<enc(raw)>" -- a single segment,
// deliberately WITHOUT the ".v2:" marker every changed kind's Derive
// output carries: it is not a `<kind>.v2:` natural-key derivation through
// the registry (it names no resolved row at all, so there is no natural
// key to derive from), it is a distinct SubjectKind altogether
// (contractsv1.ContextFabricSubjectWorkItemRef, not KindWorkItem), and it
// never enters Registry -- Lookup/Derive/Segments never recognize it.
const KindWorkItemRef = "work_item_ref"

// DeriveWorkItemRef computes the work_item_ref stub subject's canonical id
// for one unresolved raw target id (design brief §1.5). raw is encoded
// through the SAME EncodeSegment codec every other changed-kind segment
// uses, so the same injectivity/round-trip guarantees hold; the whole id
// is length-guarded at MaxNaturalKeyBytes exactly like Derive, and refuses
// (never truncates) on overflow -- the same "omit the whole row, never a
// collision-prone prefix" discipline, recorded to ledger if non-nil.
func DeriveWorkItemRef(raw string, ledger *Ledger) (id string, omitted bool) {
	id = KindWorkItemRef + ":" + EncodeSegment(raw)
	if len(id) > MaxNaturalKeyBytes {
		if ledger != nil {
			ledger.Record(KindWorkItemRef, []string{raw}, len(id))
		}
		return "", true
	}
	return id, false
}

// RelationshipFamily names the STRUCTURAL shape of a changed-kind
// relationship for the relationship.v2 scheme (design brief §1.5, P5) --
// e.g. "work_item_dependency", "work_item_hierarchy". It is NOT the
// relationship's contractsv1.ContextFabricRelationshipType (BLOCKS,
// RELATED_TO, PART_OF, ...): the family is fixed per PRODUCER, while the
// type can vary per ROW within the same producer (work_item_dependency
// rows carry a source-supplied relationship_type that maps to several
// different ContextFabricRelationshipType values) -- folding type into
// the id would make two DIFFERENT relationships between the SAME ordered
// endpoint pair (e.g. "blocks" and "relates_to") share one id, when they
// must stay distinguishable exactly like the type-varying rows tables.go
// already handles by including type in the pre-v2 relationship_id.
type RelationshipFamily string

const (
	RelationshipFamilyWorkItemDependency RelationshipFamily = "work_item_dependency"
	RelationshipFamilyWorkItemHierarchy  RelationshipFamily = "work_item_hierarchy"
)

// DeriveRelationship computes the `relationship.v2:<family>:
// <hex(sha256(enc(from) + ":" + enc(to) + ":" + enc(type)))[:32]>` canonical
// relationship id (design brief §1.5, P5, chris ruling on the byte-budget
// question): relationship identity versioned by canonical ENDPOINT
// identities, closing the collision/strand defect P5 documents -- an edge
// between different endpoints IS a different relationship, injective in
// endpoints, the same property node identity already has (§1.1).
//
// A DIGEST of the endpoint pair + type, not the pair embedded verbatim --
// deliberately, not for brevity alone. Two endpoint canonical ids
// (identity.Derive's own MaxNaturalKeyBytes bound is 256 bytes EACH) can
// together exceed any fixed relationship_id budget in the worst case,
// and widening that budget to cover the worst case keeps a real failure
// class alive: an oversize pair still forces a choice between refusing
// the row (whole-row omit, same as Derive) or truncating it (never done
// here). A digest makes that failure class UNCONSTRUCTIBLE instead of
// managed -- the id length is FIXED regardless of endpoint length, so
// DeriveRelationship never omits and callers need no overflow branch,
// mirroring the SAME pattern falkorgraph's own graphKey already uses
// (prefix + sha256(org) digest, for the identical reason: variable-length
// identity embedded in a bounded key). Collision risk reduces to SHA-256
// collision, negligible, PROVIDED the digest input is unique per
// (from, to, type) triple -- which it is: from/to/relType are each passed
// through EncodeSegment individually and joined with ':' BEFORE hashing,
// the same closure JoinSegments gives Derive's own id (an unescaped
// ':' inside any of the three inputs can never introduce an extra
// top-level separator the way it would in a raw, un-encoded join).
//
// from and to are the endpoints' OWN full canonical ids (e.g. a
// "work_item.v2:..." id from Derive, or a "work_item_ref:..." id from
// DeriveWorkItemRef) -- NOT raw natural-key segments; this function does
// not re-derive them, it only versions the RELATIONSHIP by them. relType
// is the row's own relationship-type discriminator (e.g. "blocks",
// "related_to" -- the lowercase source value tables.go already reads
// before upper-casing it into a ContextFabricRelationshipType).
//
// The digest is truncated to the first 32 HEX CHARACTERS (16 raw bytes,
// half of SHA-256's 256-bit output) -- ample collision resistance for
// this cardinality (a single organization's edge count is nowhere near
// birthday-bound territory at 2^64), and keeps the whole id comfortably
// inside the pre-existing 8-256 RelationshipID bound with room for the
// "relationship.v2:<family>:" prefix (no contract widening required, on
// chris's explicit ruling).
func DeriveRelationship(family RelationshipFamily, fromCanonicalID, toCanonicalID, relType string) (id string) {
	digestInput := JoinSegments(fromCanonicalID, toCanonicalID, relType)
	sum := sha256.Sum256([]byte(digestInput))
	return "relationship.v2:" + string(family) + ":" + hex.EncodeToString(sum[:])[:32]
}
