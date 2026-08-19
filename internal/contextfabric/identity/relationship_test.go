package identity_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestDeriveWorkItemRefRoundTrips pins the "work_item_ref:<enc(raw)>" shape
// (design brief §1.5) -- single segment, no ".v2:" marker, same codec.
func TestDeriveWorkItemRefRoundTrips(t *testing.T) {
	id, omitted := identity.DeriveWorkItemRef("WIDGET-101", nil)
	if omitted {
		t.Fatalf("unexpected omission for a short raw id")
	}
	if id != "work_item_ref:WIDGET-101" {
		t.Fatalf("id = %q, want %q", id, "work_item_ref:WIDGET-101")
	}
}

// TestDeriveWorkItemRefEncodesColonAndPercent proves the stub id uses the
// SAME §1.1 codec every other changed-kind segment does, closing the same
// encoding-ambiguity class for raw ids that happen to contain ':' or '%'.
func TestDeriveWorkItemRefEncodesColonAndPercent(t *testing.T) {
	id, omitted := identity.DeriveWorkItemRef("weird:id%with%stuff", nil)
	if omitted {
		t.Fatalf("unexpected omission")
	}
	if id != "work_item_ref:weird%3Aid%25with%25stuff" {
		t.Fatalf("id = %q, want the encoded form", id)
	}
}

// TestDeriveWorkItemRefOmitsOverLongRaw proves the stub id shares Derive's
// whole-row-omit discipline (never truncate) against MaxNaturalKeyBytes,
// and records to the ledger like every other omission.
func TestDeriveWorkItemRefOmitsOverLongRaw(t *testing.T) {
	over := strings.Repeat("a", identity.MaxNaturalKeyBytes)
	ledger := &identity.Ledger{}
	id, omitted := identity.DeriveWorkItemRef(over, ledger)
	if !omitted || id != "" {
		t.Fatalf("DeriveWorkItemRef(over-long) = (%q, %v), want (\"\", true)", id, omitted)
	}
	entries := ledger.Entries()
	if len(entries) != 1 || entries[0].Kind != identity.KindWorkItemRef {
		t.Fatalf("ledger entries = %#v, want one entry kind %q", entries, identity.KindWorkItemRef)
	}
}

// TestDeriveRelationshipIsDeterministic proves the digest scheme (chris's
// ruling on the byte-budget question) derives the SAME id every time for
// the same (family, from, to, type) -- required for tombstone healing to
// recompute the ref-form id byte-for-byte on a later sync.
func TestDeriveRelationshipIsDeterministic(t *testing.T) {
	a := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-1:WIDGET-200", "blocks")
	b := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-1:WIDGET-200", "blocks")
	if a != b {
		t.Fatalf("DeriveRelationship is not deterministic: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "relationship.v2:work_item_dependency:") {
		t.Fatalf("id = %q, want the relationship.v2:<family>: prefix", a)
	}
}

// TestDeriveRelationshipIsInjectiveInEndpoints is P5's own closure claim,
// checked directly: an edge between different endpoints IS a different
// relationship, even holding family and type fixed.
func TestDeriveRelationshipIsInjectiveInEndpoints(t *testing.T) {
	a := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-1:WIDGET-200", "blocks")
	b := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-1:WIDGET-201", "blocks")
	if a == b {
		t.Fatalf("different target endpoints derived the same relationship id %q", a)
	}
}

// TestDeriveRelationshipDistinguishesRefFromResolvedEndpoint is the
// EXACT scenario §1.5's tombstone healing depends on: the ref-form and the
// resolved-form of the SAME logical edge must derive to DIFFERENT
// relationship ids, or the healed edge and the stub edge would collide on
// relationship_id's unique constraint instead of coexisting until the
// tombstone retires the stub.
func TestDeriveRelationshipDistinguishesRefFromResolvedEndpoint(t *testing.T) {
	refForm := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item_ref:WIDGET-999", "blocks")
	resolvedForm := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-2:WIDGET-999", "blocks")
	if refForm == resolvedForm {
		t.Fatalf("ref-form and resolved-form relationship ids collided: %q", refForm)
	}
}

// TestDeriveRelationshipDistinguishesType proves the type segment
// participates in the digest input: two rows sharing the same endpoint
// pair but a different relationship type (a real, reachable case --
// work_item_dependency rows carry a source-supplied type) must not
// collide.
func TestDeriveRelationshipDistinguishesType(t *testing.T) {
	blocks := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-1:WIDGET-200", "blocks")
	relatesTo := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:WIDGET-101", "work_item.v2:repo-1:WIDGET-200", "related_to")
	if blocks == relatesTo {
		t.Fatalf("different relationship types derived the same id %q", blocks)
	}
}

// TestDeriveRelationshipDistinguishesFamily proves the family segment is
// not folded into the digest -- two DIFFERENT producers (dependency vs.
// hierarchy) deriving from coincidentally identical endpoint/type inputs
// must still land on different ids, since the family is a plain string
// prefix, not part of the hashed input.
func TestDeriveRelationshipDistinguishesFamily(t *testing.T) {
	dependency := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency,
		"work_item.v2:repo-1:A", "work_item.v2:repo-1:B", "x")
	hierarchy := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemHierarchy,
		"work_item.v2:repo-1:A", "work_item.v2:repo-1:B", "x")
	if dependency == hierarchy {
		t.Fatalf("different families derived the same id %q", dependency)
	}
}

// TestDeriveRelationshipRespectsUnescapedColonBoundary proves the digest
// input is joined through the SAME per-segment EncodeSegment closure every
// other id in this package uses (JoinSegments), so an unescaped ':' inside
// one endpoint id can never manufacture an extra top-level separator that
// makes two structurally DIFFERENT (from, to, type) triples hash
// identically. Concretely: ("A:B", "C", "x") and ("A", "B:C", "x") must
// not collide, even though a naive `strings.Join([]string{a,b,c}, ":")`
// would produce the identical "A:B:C:x" digest input for both.
func TestDeriveRelationshipRespectsUnescapedColonBoundary(t *testing.T) {
	a := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, "A:B", "C", "x")
	b := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, "A", "B:C", "x")
	if a == b {
		t.Fatalf("unescaped ':' boundary collision: %q", a)
	}
}

// TestDeriveRelationshipNeverOmits is the whole point of the digest
// scheme (chris's ruling): unlike Derive/DeriveWorkItemRef, this function
// takes no ledger and returns no omitted flag -- an arbitrarily long
// endpoint pair still produces a FIXED-length id, so the whole-row-omit
// failure class is structurally unconstructible here, not merely rare.
func TestDeriveRelationshipNeverOmits(t *testing.T) {
	longEndpoint := "work_item.v2:repo-1:" + strings.Repeat("a", 2000)
	id := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, longEndpoint, longEndpoint, "blocks")
	if id == "" {
		t.Fatal("DeriveRelationship returned an empty id for a very long endpoint pair, want a fixed-length digest id")
	}
	// "relationship.v2:" + family + ":" + 32 hex chars, well inside the
	// pre-existing 8-256 RelationshipID contract bound regardless of
	// endpoint length.
	if len(id) > 256 {
		t.Fatalf("id length = %d, want comfortably under the 256-byte RelationshipID bound regardless of endpoint length", len(id))
	}
}
