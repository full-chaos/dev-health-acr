package devhealthsource

import (
	"errors"
	"log/slog"
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
	// quarantineDuplicateWithinBatch marks a second item carrying an
	// identity another item in the SAME batch already used. Like
	// orphaned_dependent, nothing is wrong with the item: it is a redundant
	// restatement of a fact already present, and the contract rejects the
	// batch rather than the item, so the duplicate must be resolved here.
	quarantineDuplicateWithinBatch = "duplicate_within_batch"
	// quarantineEndpointEntityQuarantined marks a relationship dropped
	// because the authoritative entity for one of its endpoints was itself
	// quarantined in the same consumed set. The relationship is individually
	// valid; emitting it would leave the backend to mint an implicit stub for
	// that endpoint, with no validity window and therefore admitted at every
	// requested time.
	quarantineEndpointEntityQuarantined = "endpoint_entity_quarantined"
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
	case c.tombstone != nil:
		// Round-1 P3: tombstones used to fall straight through to the
		// generic token, so a dependency row whose last_synced is outside
		// the representable range reported unrepresentable_instant for its
		// relationship and contract_bound_violation for the two healing
		// tombstones derived from that SAME timestamp -- three drops, one
		// cause, two of them unattributable. EffectiveAt carries the same
		// bound (validate_context_fabric_projection.go), so it derives the
		// same way.
		if reason := instantReason(&c.tombstone.EffectiveAt, nil, nil); reason != "" {
			return reason
		}
		return quarantineContractBoundViolation
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
	quarantinedEntities := make(map[string]struct{})
	for _, c := range all {
		kind, err := validateCandidateItem(c)
		if err == nil {
			kept = append(kept, c)
			continue
		}
		if c.relationship != nil {
			quarantinedRelationships[c.relationship.RelationshipID] = struct{}{}
		}
		if c.entity != nil {
			quarantinedEntities[subjectIdentityKey(c.entity.Subject)] = struct{}{}
		}
		if observe != nil {
			observe(quarantineObservation{
				Reason: quarantineReason(c, err),
				Detail: quarantineDetail(c),
				Kind:   kind,
			})
		}
	}
	// An endpoint whose AUTHORITATIVE entity was quarantined must not be left
	// for the backend to invent.
	//
	// A relationship and the entity for one of its endpoints can come from
	// DIFFERENT tables, so they fail independently: a work_items row with an
	// untrimmed title loses its entity, while the work_item_dependencies row
	// naming it stays valid because that edge labels the endpoint with the
	// work item ID rather than the title. The edge then survives with no
	// entity behind it, and the backend merges the endpoint as a referenced
	// stub with NIL validity -- admitted at every historical time. The cursor
	// advances past the rejected entity, so it is durable and silent.
	//
	// This is the cross-TABLE form of the same class as the ref-stub sweep
	// below, which only covers pairs emitted by one row through `supports`.
	// Dropping the edge costs one relationship; keeping it corrupts the
	// subject's temporal admission for as long as the graph lives.
	//
	// Deliberately keyed on entities that were CONSUMED AND QUARANTINED, never
	// on entities merely absent from the batch: most endpoints legitimately
	// have their entity on another page or from another table, and dropping
	// those would delete most of the graph.
	// DROPPED IS NOT THE SAME AS NO SURVIVOR -- the third time this branch has
	// had to learn it, and the reason both this sweep and the carrier
	// correction below are keyed on survivors rather than on drops. Two rows
	// can mint the SAME subject: one quarantined, one valid. Keying on "an
	// entity with this subject was quarantined" then drops edges whose
	// endpoint is in fact supplied by the surviving twin, cascading to its
	// dependents and emptying the page. Caught by the existing duplicate-stub
	// regression the moment this sweep was added.
	if len(quarantinedEntities) > 0 {
		for _, c := range kept {
			if c.entity != nil {
				delete(quarantinedEntities, subjectIdentityKey(c.entity.Subject))
			}
		}
	}
	if len(quarantinedEntities) > 0 {
		survivors := kept[:0]
		for _, c := range kept {
			if c.relationship != nil {
				_, fromDropped := quarantinedEntities[subjectIdentityKey(c.relationship.From)]
				_, toDropped := quarantinedEntities[subjectIdentityKey(c.relationship.To)]
				if fromDropped || toDropped {
					quarantinedRelationships[c.relationship.RelationshipID] = struct{}{}
					if observe != nil {
						observe(quarantineObservation{
							Reason: quarantineEndpointEntityQuarantined,
							Kind:   "relationship",
							Detail: quarantineDetail(c),
						})
					}
					continue
				}
			}
			survivors = append(survivors, c)
		}
		kept = survivors
	}

	// A quarantined relationship id does NOT mean that id has no surviving
	// carrier. Two source rows can derive the SAME relationship id -- that is
	// the whole point of the inverted-spelling mapping, which makes
	// `A BLOCKED_BY B` and `B BLOCKS A` converge -- so one row's edge can be
	// dropped while another row's edge under that same id survives. Treating
	// the id as gone would then orphan the SURVIVING row's stub, deleting a
	// valid endpoint because an unrelated duplicate failed.
	//
	// Keyed on what actually SURVIVED, never on individual validity. An
	// earlier form asked whether any candidate carrying the id was
	// individually valid, which was wrong the moment a pass could drop an
	// individually-valid item: the endpoint sweep above does exactly that, so
	// a relationship it removed was still counted as a surviving carrier and
	// its dependents escaped the orphan sweep. Found by the generated-input
	// property test on the run that introduced the endpoint sweep.
	if len(quarantinedRelationships) > 0 {
		surviving := make(map[string]struct{}, len(kept))
		for _, c := range kept {
			if c.relationship != nil {
				surviving[c.relationship.RelationshipID] = struct{}{}
			}
		}
		for id := range quarantinedRelationships {
			if _, alive := surviving[id]; alive {
				delete(quarantinedRelationships, id)
			}
		}
	}

	// ORDER IS LOAD-BEARING: the dependent sweep runs BEFORE identity
	// deduplication, never after.
	//
	// Both passes can drop a candidate that shares an identity with another,
	// and the wrong order loses the survivor. Two unresolved rows for the
	// same target -- one whose edge is quarantined, one whose edge is valid --
	// mint the SAME `work_item_ref` stub. Deduplicating first keeps whichever
	// sorted earlier; if that is the quarantined row's stub, the sweep then
	// removes it as an orphan and the VALID row's stub is already gone, so the
	// batch carries a relationship pointing at a subject it never projected.
	// The graph then gets only the implicit endpoint stub the edge write
	// creates, which asserts no validity window and is therefore admitted at
	// every requested time -- silently over-admitting the subject historically.
	//
	// Sweeping first removes exactly the orphans, and deduplication then
	// chooses among candidates that are all still supported.
	kept = dropOrphanedDependents(kept, quarantinedRelationships, observe)
	return dropDuplicateIdentities(kept, observe)
}

// dropOrphanedDependents removes candidates whose supported item was
// quarantined. Split out from the main pass so the ordering against
// deduplication is explicit at the call site rather than implied by
// statement order inside one function.
func dropOrphanedDependents(all []candidate, quarantinedRelationships map[string]struct{}, observe func(quarantineObservation)) []candidate {
	if len(quarantinedRelationships) == 0 {
		return all
	}
	kept := all
	// An item is not projectable just because it is VALID: a candidate that
	// exists only to support a quarantined item is an unreachable orphan, and
	// emitting it would turn a clean drop into silent graph litter.
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
// subjectIdentityKey is the batch-level identity of a subject: kind plus
// canonical ID, the same pair the contract's own entity-uniqueness rule uses.
// One definition, used by both the dedup pass and the endpoint sweep, so the
// two cannot disagree about what "the same subject" means.
func subjectIdentityKey(s contractsv1.ContextFabricSubjectRef) string {
	return string(s.Kind) + "\x00" + s.CanonicalID
}

func quarantineDetail(c candidate) string {
	if c.relationship == nil {
		return ""
	}
	return headRunes(strings.ToUpper(strings.TrimSpace(string(c.relationship.Type))), maxQuarantineDetailRunes)
}

// dropDuplicateIdentities enforces the v1 contract's BATCH-level uniqueness
// rules, which per-item validation structurally cannot see: every item here is
// individually valid, and only their COMBINATION is rejected -- entity
// subjects (kind + canonical ID), relationship IDs, episode IDs, and tombstone
// kind + canonical ID.
//
// CHAOS-4874: this became reachable through the relationship mapping. Mapping
// BLOCKED_BY onto BLOCKS with exchanged endpoints is deliberate convergence --
// "A is blocked by B" and "B blocks A" state one fact and must become one
// edge -- but when BOTH spellings for the same pair land on ONE page, that
// convergence produces two items sharing one identity, and the contract
// rejects the whole batch. Without this pass the mapping re-creates the exact
// wedge it was written to remove, for the same organization, just further
// along: the affected org carries 1,310 BLOCKS rows alongside 16 inverted
// ones, so a collision on some page is a matter of when, not if.
//
// The FIRST occurrence wins, and that is deterministic because candidates are
// sorted before this runs. The choice is real and worth naming: the duplicate
// carries its own evidence ref and observed-at, which are dropped with it. A
// second row asserting an identical fact adds no edge the graph does not
// already have, so losing its evidence ref is a smaller cost than losing the
// page -- but it IS a cost, which is why it is counted rather than silent.
func dropDuplicateIdentities(all []candidate, observe func(quarantineObservation)) []candidate {
	seenEntities := make(map[string]struct{}, len(all))
	seenRelationships := make(map[string]struct{}, len(all))
	seenEpisodes := make(map[string]struct{}, len(all))
	seenTombstones := make(map[string]struct{}, len(all))
	kept := all[:0]
	for _, c := range all {
		var key string
		var seen map[string]struct{}
		var kind string
		switch {
		case c.entity != nil:
			// Entities carry a uniqueness rule too, worded differently from
			// the others ("subject must appear at most once per batch",
			// keyed on kind + canonical ID) -- which is exactly how it was
			// missed: a search for the other three rules' phrasing does not
			// match it. Two unresolved dependency rows for the same pair
			// under different spellings that MAP to one type emit the same
			// ref-stub subject twice; deduplicating only the edges leaves
			// both stubs and the batch is rejected anyway.
			key, seen, kind = subjectIdentityKey(c.entity.Subject), seenEntities, "entity"
		case c.relationship != nil:
			key, seen, kind = c.relationship.RelationshipID, seenRelationships, "relationship"
		case c.episode != nil:
			key, seen, kind = c.episode.EpisodeID, seenEpisodes, "episode"
		case c.tombstone != nil:
			key, seen, kind = string(c.tombstone.Kind)+"\x00"+c.tombstone.CanonicalID, seenTombstones, "tombstone"
		default:
			kept = append(kept, c)
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			if observe != nil {
				observe(quarantineObservation{Reason: quarantineDuplicateWithinBatch, Kind: kind, Detail: quarantineDetail(c)})
			}
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, c)
	}
	return kept
}

// quarantineLogger builds the observer both sources hand to the shared
// assembly engine. ONE implementation on purpose: the ClickHouse source got
// this signal when quarantine was added and TeamsProjectsSource did not, so
// every item the engine dropped on that source was dropped silently --
// against the standing rule that a drop is always counted. A second copy of
// the log line is how that gap would come back.
//
// Emitted at WARN, one line per dropped item. Deliberately NOT an error:
// dropping the item is the CORRECT outcome (the alternative, which shipped
// before this, was rejecting the whole page and wedging the organization),
// but a source producing unprojectable rows is still something an operator
// must see and count. A sustained non-zero rate for one reason token means
// either the source data changed or this producer needs a mapping it lacks.
//
// Content-safe on the same terms as logTableReadFailure: a closed reason
// token, a fixed item-kind label, and -- only for a relationship -- the
// offending TYPE, a bounded low-cardinality enum capped at
// maxQuarantineDetailRunes. Never row data, never free text, never the
// validator's own message.
func quarantineLogger(logger *slog.Logger, sourceName string) func(quarantineObservation) {
	if logger == nil {
		return nil
	}
	return func(observation quarantineObservation) {
		attrs := []any{
			"source", sourceName,
			"quarantine_reason", observation.Reason,
			"item_kind", observation.Kind,
		}
		if observation.Detail != "" {
			attrs = append(attrs, "relationship_type", observation.Detail)
		}
		logger.Warn("context_fabric: projection item quarantined; the item is dropped and the batch continues", attrs...)
	}
}
