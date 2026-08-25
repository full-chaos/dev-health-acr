package v1

import "fmt"

// CHAOS-4042 (sol-max ruling, 2026-08-20): the anchor MEMBERSHIP-VERIFY
// semantic major.
//
// v1 AnchorOption/ancr_ redemption is, and stays, PERMANENTLY unique-claimant
// (design brief's original anchor rule: a term whose complete
// identity-universe claimant set has exactly one member). A receipt-confirmed
// selection supplies caller INTENT authority, not identity truth, so
// term-exclusivity is unnecessary once the caller has picked a specific
// claimant -- but ACR must still prove that claimant remains a genuine member
// of the term's claimant set at redemption, under the pinned
// ResolvedGraphBinding epoch. That is membership semantics, and per the
// AGENTS.md contract rule ("changed meaning... require a new major
// contract"), it is a new major: context_fabric_investigation_result.v2.
//
// Wire fields are IDENTICAL to v1's AnchorOption/ContextFabricStructureNeeds
// -- only the redemption MEANING differs. ContextFabricAnchorOptionV2 exists
// as a distinct Go type (not a v1 AnchorOption reused) so offer-generation
// and redemption code can enforce at compile time which semantic mode an
// option was minted under; ToV1Wire converts it into the wire slice
// (ContextFabricStructureNeeds.AnchorOptions stays []ContextFabricAnchorOption
// on the wire for both v1 and v2 results, since the JSON shape does not
// differ) -- explicit, not a bare Go type conversion, so a future field
// divergence between the two types fails to compile here rather than
// silently mis-copying.
//
// SHADOW-ONLY as of this changeset (PR1b of CHAOS-4042's three-PR slice):
// nothing yet constructs a ContextFabricAnchorOptionV2 or mints a
// context_fabric_investigation_result.v2 result -- that is the offer
// generation, AnchorMembershipVerifier, and ConfirmedAnchorSelection work in
// the follow-up PRs. This changeset ships the complete, working contract
// family (Go types, JSON Schema, OpenAPI, MCP, golden example, parity
// tests) so those PRs consume an already-proven contract rather than
// building it under runtime-behavior review pressure.
const ContextFabricInvestigationResultSchemaV2 = "context_fabric_investigation_result.v2"

// ContextFabricAnchorOptionV2 offers one anchor candidate under MEMBERSHIP
// semantics: the claimant set is NOT required to be unique. Redemption
// re-verifies that (Kind, CanonicalID) remains a member of the term's
// complete claimant set at the pinned ResolvedGraphBinding epoch, matched by
// (MatchedTermHash, Kind, CanonicalID) together -- never CanonicalID or
// MatchedTermHash alone (that would be exactly the snapshot-equality or
// hash-only comparison the ruling's do-not list forbids). See
// ContextFabricAnchorOption's own doc comment for the v1 (permanently
// unique-claimant) counterpart this type is deliberately never merged with.
type ContextFabricAnchorOptionV2 struct {
	ReceiptID       string                            `json:"receipt_id"`
	OptionID        string                            `json:"option_id"`
	Label           string                            `json:"label"`
	Kind            ContextFabricSubjectKind          `json:"kind"`
	CanonicalID     string                            `json:"canonical_id"`
	MatchedTermHash string                            `json:"matched_term_hash"`
	OfferSource     ContextFabricStructureOfferSource `json:"offer_source"`
	PriorVersionID  string                            `json:"prior_version_id,omitempty"`
	PriorEntryID    string                            `json:"prior_entry_id,omitempty"`
	// Phrasing (CHAOS-4171 PR2) -- see ContextFabricAnchorOption.Phrasing's
	// own doc comment; identical contract. ToV1Wire copies it through like
	// every other field, even though nothing constructs a non-empty value
	// here today (applyOfferPhrasing runs on the wire ContextFabricAnchorOption
	// AFTER ToV1Wire converts, not on this engine-internal type) -- this
	// type's own JSON Schema (AnchorOptionV2, the v2 result's anchor_options
	// $ref) must still declare the property, or a v2 result carrying a
	// phrased anchor option fails its own schema's additionalProperties
	// check.
	Phrasing string `json:"phrasing,omitempty"`
}

// Validate checks ContextFabricAnchorOptionV2 against the SAME wire-shape
// bounds as v1's ContextFabricAnchorOption.Validate() -- the two types carry
// identical fields, so their shape constraints are identical; only the
// semantic meaning of a passing value differs, which Validate() (a pure
// shape check) has no way to express and does not attempt to.
func (o ContextFabricAnchorOptionV2) Validate() error {
	if !stringLengthBetween(o.ReceiptID, 8, 256) || !hasAnchorReceiptPrefix(o.ReceiptID) {
		return fmt.Errorf("anchor option v2 receipt_id must carry the %q namespace prefix and satisfy v1 bounds", ContextFabricAnchorOptionReceiptPrefix)
	}
	if !stringLengthBetween(o.OptionID, 1, 256) || !stringLengthBetween(o.Label, 1, 200) {
		return fmt.Errorf("anchor option v2 option_id or label violates v1 bounds")
	}
	if !validContextFabricSubjectKind(o.Kind) {
		return fmt.Errorf("anchor option v2 kind is invalid")
	}
	if !stringLengthBetween(o.CanonicalID, 1, 256) {
		return fmt.Errorf("anchor option v2 canonical_id violates v1 bounds")
	}
	if !matchedTermHashPattern.MatchString(o.MatchedTermHash) {
		return fmt.Errorf("anchor option v2 matched_term_hash must be a 24-character lowercase hex digest")
	}
	if !ValidContextFabricStructureOfferSource(o.OfferSource) {
		return fmt.Errorf("anchor option v2 offer_source is invalid")
	}
	if !optionalStringBetween(o.PriorVersionID, 1, 256) || !optionalStringBetween(o.PriorEntryID, 1, 256) {
		return fmt.Errorf("anchor option v2 prior_version_id or prior_entry_id violates v1 bounds")
	}
	if !optionalStringBetween(o.Phrasing, 1, ContextFabricStructureOfferPhrasingMaxLength) {
		return fmt.Errorf("anchor option v2 phrasing violates v1 bounds")
	}
	return nil
}

// ToV1Wire converts o into the wire ContextFabricAnchorOption shape
// ContextFabricStructureNeeds.AnchorOptions carries on every result,
// v1 or v2 -- see this file's own package doc comment for why the wire
// slice type never forks even though redemption meaning does.
func (o ContextFabricAnchorOptionV2) ToV1Wire() ContextFabricAnchorOption {
	return ContextFabricAnchorOption{
		ReceiptID:       o.ReceiptID,
		OptionID:        o.OptionID,
		Label:           o.Label,
		Kind:            o.Kind,
		CanonicalID:     o.CanonicalID,
		MatchedTermHash: o.MatchedTermHash,
		OfferSource:     o.OfferSource,
		PriorVersionID:  o.PriorVersionID,
		PriorEntryID:    o.PriorEntryID,
		Phrasing:        o.Phrasing,
	}
}
