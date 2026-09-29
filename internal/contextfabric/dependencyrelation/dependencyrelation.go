// Package dependencyrelation is the ONE alias table for
// work_item_dependencies.relationship_type (CHAOS-7177). It is a leaf package
// so the projector (devhealthsource), the fact providers (devhealthfacts) and
// the evidence catalog (contextpacket, which devhealthsource imports and so
// cannot import back) all resolve a raw spelling through the same table.
package dependencyrelation

import (
	"fmt"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// mapping is the source-side translation for
// work_item_dependencies.relationship_type values that name a relationship
// the v1 vocabulary already has, under another spelling or from the opposite
// direction (the table holds inversions and, since CHAOS-7150, one alias).
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
// This table is deliberately TINY and holds ONLY inversions and aliases: a value that
// means an existing vocabulary member (with the endpoints reversed, or under
// another spelling). It is not
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
var mapping = map[string]struct {
	mapped contractsv1.ContextFabricRelationshipType
	// swapEndpoints is true when the source spelling names the relationship
	// from the opposite side, so From/To must be exchanged for the mapped
	// type to state the same fact.
	swapEndpoints bool
	// identity, when set, is the spelling the relationship id derives from
	// instead of the mapped type's own. It exists for an alias whose target
	// is stored in the column under a DIFFERENT spelling than the vocabulary
	// member's (see RELATES): the id must equal the one the live rows already
	// carry, or one fact projects as two edges.
	identity string
	// inverse is the ONE spelling the wire carries for a swapped row
	// (CHAOS-7177): BLOCKED_BY and IS_BLOCKED_BY both emit it, so the emitted
	// value never depends on which raw row survives.
	inverse string
}{
	"BLOCKED_BY":    {mapped: contractsv1.ContextFabricRelationshipBlocks, swapEndpoints: true, inverse: "blocked_by"},
	"IS_BLOCKED_BY": {mapped: contractsv1.ContextFabricRelationshipBlocks, swapEndpoints: true, inverse: "blocked_by"},
	// RELATES (CHAOS-7150): the ops work-graph edge vocabulary spells the
	// symmetric "relates" edge RELATES (ops work_graph.models.EdgeType.RELATES,
	// the GraphQL EdgeType enum, and the neighbors service that maps
	// relates_to <-> relates), and ops' own team-inheritance treats "relates"
	// and "relates_to" as the same inheritable relation
	// (compute_work_items._INHERITABLE_RELATIONSHIP_TYPES). It is the SAME
	// fact as RELATES_TO, not a new relation: admitting RELATES as a second
	// vocabulary member would let one fact carry two edges. It maps onto
	// RELATES_TO with the endpoints unchanged (the relation is symmetric and
	// the row keeps its own direction), so it converges on the RELATES_TO
	// edge. Authorization is unchanged: the edge is a work-item dependency
	// edge like RELATES_TO, so it is visible only when both ends are admitted.
	//
	// Measured on the bigboy venue ClickHouse (org 70d529e0, read-only): all 7
	// 'relates' rows are Jira (raw 'relates to', last_synced 2026-08-31), and
	// each of those 7 pairs ALSO has a lowercase 'relates_to' row written on
	// 2026-09-28 -- the current normalizer's spelling, and the spelling every
	// projected RELATES_TO edge derives its id from (unmapped values derive the
	// id from the RAW column value). So the alias derives its id from
	// "relates_to": the stale 'relates' row lands on the SAME edge id as the
	// live one, never as a second edge.
	"RELATES": {mapped: contractsv1.ContextFabricRelationshipRelatesTo, swapEndpoints: false, identity: "relates_to"},
}

// Resolve translates one raw relationship_type value: the wire type, whether
// the endpoints are swapped, and the spelling a relationship id derives from.
// An unmapped value passes through upper-cased with its raw spelling as the
// identity, exactly as the projector has always done.
func Resolve(raw string) (typ contractsv1.ContextFabricRelationshipType, swapEndpoints bool, identitySpelling string) {
	upper := strings.ToUpper(strings.TrimSpace(raw))
	if m, ok := mapping[upper]; ok {
		if m.identity != "" {
			return m.mapped, m.swapEndpoints, m.identity
		}
		return m.mapped, m.swapEndpoints, string(m.mapped)
	}
	return contractsv1.ContextFabricRelationshipType(upper), false, raw
}

// Key is the canonical relation key of a raw spelling: canonical type plus
// direction. Two rows for one pair with the same Key state one relation.
func Key(raw string) string {
	typ, swap, _ := Resolve(raw)
	return keyOf(strings.ToLower(string(typ)), swap)
}

func keyOf(lowerType string, swap bool) string {
	if swap {
		return lowerType + ":inv"
	}
	return lowerType + ":fwd"
}

// EmittedSpelling is the relationship_type value a fact carries: the
// canonical type lower-cased; for a swapped row the fixed inverse spelling of
// that alias family, never the raw survivor.
func EmittedSpelling(raw string) string {
	typ, swap, _ := Resolve(raw)
	if swap {
		upper := strings.ToUpper(strings.TrimSpace(raw))
		if m, ok := mapping[upper]; ok && m.inverse != "" {
			return m.inverse
		}
	}
	return strings.ToLower(string(typ))
}

// KeySQL renders Key as a ClickHouse expression over column (a String or
// Nullable(String) expression), generated from the same table so SQL and Go
// cannot drift. Whitespace trimming is ASCII-only in SQL.
func KeySQL(column string) string {
	trimmed := fmt.Sprintf("upper(replaceRegexpAll(ifNull(%s, ''), '^[[:space:]]+|[[:space:]]+$', ''))", column)
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("multiIf(")
	for _, k := range keys {
		typ, swap, _ := Resolve(k)
		fmt.Fprintf(&b, "%s = '%s', '%s', ", trimmed, k, keyOf(strings.ToLower(string(typ)), swap))
	}
	fmt.Fprintf(&b, "concat(lower(replaceRegexpAll(ifNull(%s, ''), '^[[:space:]]+|[[:space:]]+$', '')), ':fwd'))", column)
	return b.String()
}
