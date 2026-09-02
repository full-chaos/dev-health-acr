package devhealthsource

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Quarantine reason tokens. CLOSED vocabulary: these are the only values
// this package ever reports, so an operator can alert on them and a
// dashboard can enumerate them. Never derived from an error's TEXT -- see
// quarantineReason.
const (
	quarantineUnknownRelationshipType = "unknown_relationship_type"
	quarantineUntrimmedLabel          = "untrimmed_label"
	quarantineInvertedWindow          = "inverted_window"
	quarantineOversizeScalar          = "oversize_scalar"
	quarantineUnrepresentableInstant  = "unrepresentable_instant"
	quarantineContractBoundViolation  = "contract_bound_violation"
	// quarantineOrphanedDependent marks an item that was VALID on its own
	// but existed solely to support another item that was quarantined.
	// Distinct from every other token: nothing is wrong with this item, and
	// counting it as a bound violation would misreport a healthy row shape
	// as bad data.
	quarantineOrphanedDependent = "orphaned_dependent"
)

// maxQuarantineDetailRunes bounds the offending value carried alongside a
// reason. relationship_type is a low-cardinality enum column, but it is
// still SOURCE data reaching operational telemetry, so it is capped rather
// than trusted -- an unbounded label is a cardinality hazard as well as a
// content-safety one.
const maxQuarantineDetailRunes = 64

// quarantineObservation is one dropped item, reported to the owning source's
// telemetry hook. Counts and closed tokens only.
type quarantineObservation struct {
	// Reason is one of the closed tokens above.
	Reason string
	// Detail is the offending value when it is a bounded enum this package
	// can safely name (today: the uppercased relationship_type), else "".
	Detail string
	// Kind names which item shape was dropped: entity, relationship,
	// episode or tombstone.
	Kind string
}

// validateCandidateItem runs the contract's OWN validator over whichever
// item a candidate carries. This is the authority on whether an item may be
// projected -- deliberately the same method ContextFabricProjectionBatch.
// Validate would call, not a reimplementation of its rules, so an item this
// package keeps can never be one the batch validator would later reject.
func validateCandidateItem(c candidate) (kind string, err error) {
	switch {
	case c.entity != nil:
		return "entity", c.entity.Validate()
	case c.relationship != nil:
		return "relationship", c.relationship.Validate()
	case c.episode != nil:
		return "episode", c.episode.Validate()
	case c.tombstone != nil:
		return "tombstone", c.tombstone.Validate()
	}
	// A progress marker carries no item; it is not projectable and not
	// invalid -- it exists to move the cursor.
	return "", nil
}

// quarantineReason names WHY an item was dropped, from a closed vocabulary.
//
// Deliberately NOT derived by matching the validator's error text: those
// messages are plain fmt.Errorf strings with no stability contract, and a
// reason token that silently changes meaning when a message is reworded is
// worse than no token. Only ONE bound carries a real sentinel
// (ErrContextFabricUnknownRelationshipType) and that one is read with
// errors.Is; the rest are re-derived from the ITEM'S OWN DATA by the
// predicates below.
//
// Those predicates are a diagnostic aid, not a second validator: the
// authoritative fact is always "the contract validator rejected this item",
// which is why an item matching none of them still reports a token
// (quarantineContractBoundViolation) rather than being kept. A test pins each
// token against a fixture that genuinely breaches that bound, so a predicate
// drifting away from the contract shows up as a failing test rather than as a
// mislabelled counter.
func quarantineReason(c candidate, err error) string {
	if errors.Is(err, contractsv1.ErrContextFabricUnknownRelationshipType) {
		return quarantineUnknownRelationshipType
	}
	switch {
	case c.entity != nil:
		return entityQuarantineReason(*c.entity)
	case c.relationship != nil:
		return relationshipQuarantineReason(*c.relationship)
	}
	return quarantineContractBoundViolation
}

func entityQuarantineReason(e contractsv1.ContextFabricEntityProjection) string {
	if untrimmedOrOversizeLabel(e.Subject) {
		return labelReason(e.Subject)
	}
	if reason := instantReason(&e.ObservedAt, e.ValidFrom, e.ValidTo); reason != "" {
		return reason
	}
	if oversizeScalarMap(e.Properties) {
		return quarantineOversizeScalar
	}
	return quarantineContractBoundViolation
}

func relationshipQuarantineReason(r contractsv1.ContextFabricRelationshipProjection) string {
	if untrimmedOrOversizeLabel(r.From) || untrimmedOrOversizeLabel(r.To) {
		if reason := labelReason(r.From); reason != quarantineContractBoundViolation {
			return reason
		}
		return labelReason(r.To)
	}
	if reason := instantReason(&r.ObservedAt, r.ValidFrom, r.ValidTo); reason != "" {
		return reason
	}
	if oversizeScalarMap(r.Properties) {
		return quarantineOversizeScalar
	}
	return quarantineContractBoundViolation
}

func untrimmedOrOversizeLabel(s contractsv1.ContextFabricSubjectRef) bool {
	return strings.TrimSpace(s.Label) != s.Label || utf8.RuneCountInString(s.Label) > 512 ||
		strings.TrimSpace(s.CanonicalID) != s.CanonicalID
}

func labelReason(s contractsv1.ContextFabricSubjectRef) string {
	if strings.TrimSpace(s.Label) != s.Label || strings.TrimSpace(s.CanonicalID) != s.CanonicalID {
		return quarantineUntrimmedLabel
	}
	if utf8.RuneCountInString(s.Label) > 512 {
		return quarantineOversizeScalar
	}
	return quarantineContractBoundViolation
}

// instantReason distinguishes the two temporal bounds: a value outside the
// epoch-nanosecond range Go can represent, and a window whose end precedes
// its start.
func instantReason(observed, validFrom, validTo *time.Time) string {
	for _, value := range []*time.Time{observed, validFrom, validTo} {
		if value == nil || value.IsZero() {
			continue
		}
		if !contractsv1.RepresentableInstant(*value) {
			return quarantineUnrepresentableInstant
		}
	}
	if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		return quarantineInvertedWindow
	}
	return ""
}

func oversizeScalarMap(properties map[string]contractsv1.ContextFabricScalarValue) bool {
	for _, value := range properties {
		if value.String != nil && utf8.RuneCountInString(*value.String) > 4000 {
			return true
		}
	}
	return false
}

// partitionProjectableCandidates drops every candidate whose item the
// contract validator rejects, reporting each to observe, and returns the rest.
//
// This is the fix for the wedge class as a whole, not for any one bound:
// ContextFabricProjectionBatch.Validate is all-or-nothing, so before this
// existed a SINGLE unprojectable row rejected its entire page forever. An
// item dropped here costs exactly that item; the page still publishes, the
// cursor still advances, and the organization keeps projecting.
//
// The returned slice is a new backing array: `all` stays intact for the
// caller, which still derives the batch's NextCursor from it. That
// separation is load-bearing -- deriving the cursor from the FILTERED list
// would move the watermark backwards whenever the last row on a page was
// quarantined, and those rows would be re-read, re-dropped and re-counted on
// every subsequent tick.
func partitionProjectableCandidates(all []candidate, observe func(quarantineObservation)) []candidate {
	kept := make([]candidate, 0, len(all))
	quarantinedRelationships := make(map[string]struct{})
	for _, c := range all {
		kind, err := validateCandidateItem(c)
		if err == nil {
			kept = append(kept, c)
			continue
		}
		if c.relationship != nil {
			quarantinedRelationships[c.relationship.RelationshipID] = struct{}{}
		}
		if observe != nil {
			observe(quarantineObservation{
				Reason: quarantineReason(c, err),
				Detail: quarantineDetail(c),
				Kind:   kind,
			})
		}
	}
	if len(quarantinedRelationships) == 0 {
		return kept
	}
	// Second pass: an item is not projectable just because it is VALID. A
	// candidate that exists only to support a quarantined item is an
	// unreachable orphan, and emitting it would turn a clean drop into
	// silent graph litter. Runs only when something was actually dropped,
	// so the common path stays a single walk.
	survivors := kept[:0]
	for _, c := range kept {
		if c.supports != "" {
			if _, dropped := quarantinedRelationships[c.supports]; dropped {
				if observe != nil {
					kind, _ := validateCandidateItem(c)
					observe(quarantineObservation{Reason: quarantineOrphanedDependent, Kind: kind})
				}
				continue
			}
		}
		survivors = append(survivors, c)
	}
	return survivors
}

// quarantineDetail returns the bounded offending value when this package can
// name one safely. Today that is the relationship TYPE only: it is a
// low-cardinality enum column, and naming it is what lets an operator tell
// "one unmapped vocabulary value, 105 rows" from "105 different problems"
// without a diagnostic build.
func quarantineDetail(c candidate) string {
	if c.relationship == nil {
		return ""
	}
	return headRunes(strings.ToUpper(strings.TrimSpace(string(c.relationship.Type))), maxQuarantineDetailRunes)
}
