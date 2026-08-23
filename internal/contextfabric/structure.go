package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3900 P1 (pivot-intent design brief, DESIGN-FINAL, §2.1). This file
// is the FIRST slice of the structure contract's engine mechanism:
// pre-reuse receipt resolution and the reuse-bypass decision. It
// deliberately transplants CHAOS-3900 W1's canonicalizeEvidenceWindow/
// resolveWindowReceipts structure (window.go) wholesale -- same ordering
// discipline (§2.1: "canonicalizeStructure runs BEFORE tryReuse... producing
// the resolved confirmed-structure set before any reuse lookup or
// interpretation"), same atomic-batch-veto shape, same fail-closed rules.
//
// SCOPE NOTE (P1.B): this slice resolves RECEIPTS only (kindr_/ancr_/
// handr_ against a prior result's own StructureNeeds offer sets). Explicit
// structure fields (design brief §2.3's subject_handles/expected_kinds) are
// an MCP-surface wire concept that does not exist on the canonical request
// contract yet -- that lands with P3's own request extension, at which
// point canonicalizeStructure gains the explicit-vs-receipt conflict class
// window's own canonicalizeEvidenceWindow already has (windowsAgree's
// analogue). Offer building (KindOptions/AnchorOptions/HandleOptions
// construction from pool/resolution state), kind-insensitivity gating, and
// anchor/handle redemption-time re-verification are separate, later slices
// (P1.C/D/E) -- this file's own resolution logic assumes a stored offer's
// shape is trustworthy as persisted and does not yet re-verify a claimant's
// live uniqueness or a handle's live source-row existence at redemption
// time (that re-verification is P1.E's own scope).

// structureVetoReason mirrors windowVetoReason's own two-member shape
// exactly (design brief §2.5 row 2/3): a structure receipt batch either
// resolves atomically or vetoes the whole request, with no partial
// application and no inference substituted for a vetoed member.
type structureVetoReason string

const (
	structureVetoNone structureVetoReason = ""
	// structureVetoConfirmationUnresolved: a PriorKindReceipts/
	// PriorAnchorReceipts/PriorHandleReceipts entry could not be resolved
	// -- the named prior result could not be loaded, carried no
	// StructureNeeds, or named no matching option in the relevant offer
	// list. Recovery is retry the same receipt, or omit it.
	structureVetoConfirmationUnresolved structureVetoReason = "structure_confirmation_unresolved"
	// structureVetoConfirmationConflict: plural receipts (>=2) for the
	// SAME member -- ambiguous by construction, never first-match-wins
	// (design brief §2.1, mirroring window's own plural-receipt rule).
	structureVetoConfirmationConflict structureVetoReason = "structure_confirmation_conflict"
	// structureVetoStaleSupersededOffer (CHAOS-3927 P4, design brief §2.1's
	// offer-supersession rule / §2.5's "redeeming a superseded offer" row):
	// the (org, PriorResultID, member) tuple this receipt would redeem was
	// already claimed by a LATER result that redeemed a receipt of the
	// SAME member from the SAME prior result -- either detected up front
	// by a StructureSupersessionChecker consult (the common case) or
	// discovered only at Save time when the atomic claim insert itself
	// lost its race (ErrStructureOfferSuperseded, the rare concurrent
	// case). Recovery is retry against the NEWER result's own fresh
	// offers, never the same receipt again.
	structureVetoStaleSupersededOffer structureVetoReason = "stale_superseded_offer"
)

// confirmedStructureMember is one member's resolved confirmation --
// canonicalizeStructure's own internal shape, composed into
// ContextFabricConfirmedStructureEntry (with Disposition=applied,
// Provenance=clarification_confirmed) once a result exists to attach it to.
type confirmedStructureMember struct {
	Member       contractsv1.ContextFabricStructureNeedKind
	AppliedValue string
	// AppliedKind (CHAOS-3972 P3, codex xhigh review round 1 finding 3) is
	// the confirmed offer's own Kind -- populated for subject_handle (the
	// one member with an explicit-field conflict class to check against,
	// §2.1: "anchors are RECEIPT-ONLY on the MCP surface... there is no
	// explicit anchor field"). Empty for expected_kind (AppliedValue IS
	// already the kind there) and subject_anchor (no explicit-anchor
	// conflict class exists). See resolveExplicitStructure's own handle
	// conflict check, the sole reader.
	AppliedKind    contractsv1.ContextFabricSubjectKind
	PriorResultID  string
	ReceiptID      string
	OfferSource    contractsv1.ContextFabricStructureOfferSource
	PriorVersionID string
	PriorEntryID   string
}

// explicitStructureMember is one member's EXPLICIT (non-receipt) value
// (CHAOS-3972 P3, design brief §2.5's "explicit structure via MCP surface"
// row) -- canonicalizeStructure's own internal shape for the request's
// ExpectedKinds/SubjectHandles fields, composed into a
// ContextFabricConfirmedStructureEntry (Disposition=applied) exactly like
// confirmedStructureMember is for receipts, but carrying NO receipt
// identity: there is nothing to redeem, only a caller-supplied value that
// entered at Provenance (never question_stated on the MCP surface, per
// DP12(b)).
type explicitStructureMember struct {
	Member       contractsv1.ContextFabricStructureNeedKind
	AppliedValue string
	Source       contractsv1.ContextFabricStructureSource
	Provenance   contractsv1.ContextFabricStructureProvenance
}

// requestStructureCanonicalization is canonicalizeStructure's own result.
type requestStructureCanonicalization struct {
	// Confirmed lists every member this request's receipts resolved.
	// NON-EMPTY means the request BYPASSES tryReuse entirely (design brief
	// §2.1/DP11: "any request whose canonicalized confirmed-structure set
	// is non-empty skips the reuse lookup entirely" -- a bypass, not a
	// ReuseKey fold-in).
	Confirmed []confirmedStructureMember
	// Explicit lists every member CHAOS-3972 P3's own request.ExpectedKinds/
	// SubjectHandles fields resolved WITHOUT a receipt (design brief §2.5:
	// "explicit structure via MCP surface... enters inferred_default,
	// drives narrowing + offer shaping; request proceeds"). Deliberately
	// separate from Confirmed: an explicit-only member does NOT bypass
	// tryReuse (only a receipt-confirmed member does, per DP11 -- an
	// inferred_default value is not caller authority), and does not narrow
	// the ordinary resolution pool the way a receipt-confirmed kind does
	// (see ConfirmedExpectedKind's own doc comment, ports.go).
	Explicit []explicitStructureMember
	// Veto is non-empty when this request must short-circuit to a
	// no_match terminal WITHOUT reuse, WITHOUT interpretation, and
	// WITHOUT any inference substituted (design brief §2.5 rows 2/3).
	Veto structureVetoReason
	// OfferSnapshot (CHAOS-3900 P1.G, design brief §2.1 B5) is the minimal
	// echoed copy of EVERY offer in the SOURCE offer list for each
	// confirmed member -- not just the redeemed one -- so a decisive
	// result reached via confirmation still carries the (offered,
	// selected) pair the Bridge needs. Populated only alongside Confirmed
	// (never on a veto: nothing was confirmed, so there is nothing to
	// snapshot -- matching ConfirmedStructure's own composition, which
	// composeStructureOfferSnapshot's caller pairs this with 1:1).
	OfferSnapshot []contractsv1.ContextFabricStructureOfferSnapshotEntry
	// PendingSelections (CHAOS-3927 P4, codex adversarial review fix) holds
	// one BUILT (never yet sent) StructureSelectionEvent per member this
	// canonicalization confirmed -- non-nil only alongside Confirmed, same
	// discipline as OfferSnapshot. Deliberately deferred: firing capture
	// synchronously inside canonicalizeStructure's own loop (P4's first
	// pass) could durably record a selection for a round that later loses
	// the Save-time supersession claim race and is discarded entirely --
	// design brief §3.5's own invariant is that the authoritative
	// confirmation record is "the persisted canonical result," which this
	// round is NOT yet at the moment canonicalizeStructure returns.
	// Engine.recordStructureConfirmationOutcome sends these only once Save
	// has actually won every claim they depend on -- called from every
	// Save call site that can carry a non-empty Confirmed (Investigate's
	// own decisive path in engine.go, AND terminalResult's own
	// subjectless-terminal path in unresolved.go, added by codex round-2's
	// own finding that round 1 only wired the former) -- never on the veto
	// path (nothing was durably confirmed) and never on the
	// Save-time-race path (the result these describe was never persisted).
	PendingSelections []StructureSelectionEvent
	// StaleMembers (CHAOS-3927 P4) is populated ONLY alongside
	// Veto==structureVetoStaleSupersededOffer -- every member whose stored
	// offer was already superseded, composed eagerly here (this is the one
	// veto class the design brief's §2.5 table explicitly requires an echo
	// entry for: "echo disposition vetoed_stale") rather than deferred the
	// way the pre-existing structureVetoConfirmationConflict/
	// structureVetoConfirmationUnresolved gap is (composeConfirmedStructure's
	// own KNOWN GAP comment) -- canonicalizeStructure already has every
	// field the echo needs in scope at the exact moment it detects
	// staleness, so composing it here costs nothing extra.
	//
	// A SLICE, not a single entry (codex round-3 adversarial review,
	// MEDIUM finding): the PRE-FLIGHT consult (canonicalizeStructure's own
	// loop) can only ever detect ONE stale member per veto -- it returns
	// immediately on the first one found, the same short-circuit discipline
	// structureVetoConfirmationConflict/Unresolved already use, so this
	// slice holds exactly one entry on that path -- but the SAVE-TIME race
	// (ErrStructureOfferSuperseded.Members, engine.go/unresolved.go) can
	// legitimately report MULTIPLE members losing their claim in the SAME
	// Save (e.g. a request redeeming both expected_kind and subject_anchor
	// against a prior result a concurrent Save already claimed both
	// members of) -- a single-entry echo there silently dropped every
	// member past the first, violating the "one entry PER carried member"
	// wire contract this exists to uphold.
	StaleMembers []contractsv1.ContextFabricConfirmedStructureEntry
	// VetoedEntries (CHAOS-3963) closes composeConfirmedStructure's own
	// former KNOWN GAP comment: on a
	// structureVetoConfirmationUnresolved/structureVetoConfirmationConflict
	// veto, this carries one re-dispositioned entry (Disposition swapped
	// from applied to the veto's own vetoed_unresolved/vetoed_conflict)
	// for every member the loop had ALREADY resolved before the veto
	// fired -- the atomic all-or-nothing rule (§2.1) means none of them
	// were actually applied, so echoing them as "applied" would misreport
	// the outcome, but dropping them entirely was the silent-drop-reborn
	// gap CHAOS-3963 exists to close: a caller could not tell "member A
	// confirmed fine, member B is why the whole batch was rejected" from
	// the response. When the triggering member itself had a resolved
	// value at the point of failure (only true of a reverify failure --
	// every earlier failure mode in the loop has no value to echo, and
	// ContextFabricConfirmedStructureEntry.Validate requires a non-empty
	// applied_value, so those genuinely-unresolvable members carry no
	// entry at all, matching resolveExplicitStructure's own established
	// "flag rather than silently omit" convention for the analogous
	// multi-valued-explicit-field gap), that member's own entry is
	// appended too. Mutually exclusive with StaleMembers: populated only
	// on the two veto reasons StaleMembers is never populated for.
	VetoedEntries []contractsv1.ContextFabricConfirmedStructureEntry
}

// HandleVerificationReason is the closed vocabulary Engine's handle
// re-verification dependency reports (CHAOS-3900 P1.E). Mirrors
// graphrank.HandleVerificationReason's own four members exactly --
// contextfabric cannot import graphrank (the package-graph constraint this
// whole epic works around throughout P1), so this is the package-local
// dependency-injection copy, the same pattern ConfirmedExpectedKind (ports.go)
// already establishes for P1.D.
type HandleVerificationReason string

const (
	HandleVerificationValid             HandleVerificationReason = "valid"
	HandleVerificationGrammarMismatch   HandleVerificationReason = "grammar_mismatch"
	HandleVerificationNotFound          HandleVerificationReason = "not_found"
	HandleVerificationCensusUnavailable HandleVerificationReason = "census_unavailable"
)

// HandleVerifier is canonicalizeStructure's own redemption-time
// re-verification dependency for the subject_handle member (design brief
// §2.1: "redemption re-validates the value against the registry grammar
// AND re-runs the keyed source-row existence check"). (org, kind,
// pattern_id, value) in -> valid/invalid + reason out, question-free --
// the same contract shape team-lead's E guidance established for every
// re-verification primitive in this slice.
//
// The production implementation (internal/runtime/hosted/open.go) adapts
// graphrank.VerifyHandle over the SAME CensusFunc the shadow evidence
// round already uses (devhealthsource.NewCensusFunc) -- see that
// function's own doc comment for why it deliberately carries no epoch
// parameter. A nil HandleVerifier is NOT "trust the stored offer": see
// canonicalizeStructure's own reverify wiring below for why an unwired
// verifier fails closed instead.
type HandleVerifier func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason)

// AnchorVerificationReason is the closed vocabulary Engine's anchor
// re-verification dependency reports (CHAOS-3900 P1.E, team-lead ruling).
// Package-local copy of graphrank.AnchorVerificationReason, same reason
// HandleVerificationReason above is one -- contextfabric cannot import
// graphrank.
type AnchorVerificationReason string

const (
	AnchorVerificationValid                 AnchorVerificationReason = "valid"
	AnchorVerificationClaimContested        AnchorVerificationReason = "anchor_claim_contested"
	AnchorVerificationClaimLost             AnchorVerificationReason = "anchor_claim_lost"
	AnchorVerificationIncompleteEnumeration AnchorVerificationReason = "incomplete_enumeration"
	// AnchorVerificationUnauthorized (CHAOS-4042, sol-max ruling) is
	// AnchorMembershipVerifier's own reason for "the selected claimant is
	// no longer authorized under binding/scope B" -- INTERNAL/telemetry
	// only. The ruling: "Generic unresolved veto; do not expose whether it
	// still exists" -- the caller-visible effect is IDENTICAL to
	// AnchorVerificationClaimLost (structure_confirmation_unresolved), so
	// this value must never itself be surfaced on the wire; it exists only
	// so an operator reading telemetry can distinguish "the claim vanished"
	// from "the principal lost visibility" without that distinction ever
	// reaching a caller.
	AnchorVerificationUnauthorized AnchorVerificationReason = "anchor_claim_unauthorized"
	// AnchorVerificationGraphUnverifiable (CHAOS-4042 PR3, sol-max ruling)
	// mirrors graphrank.AnchorVerificationGraphUnverifiable's own doc
	// comment: the pinned binding's own graph key could not be read at all
	// (a retired epoch) or the graph-side read errored -- CANNOT-VERIFY,
	// deliberately distinct from AnchorVerificationClaimLost (a stale
	// binding proves nothing about whether the claimant still exists in a
	// LIVE epoch). Same generic vetoed_unresolved wire disposition as
	// AnchorVerificationIncompleteEnumeration; internal telemetry only.
	AnchorVerificationGraphUnverifiable AnchorVerificationReason = "graph_binding_unverifiable"
)

// AnchorVerifier is canonicalizeStructure's own redemption-time
// re-verification dependency for the subject_anchor member (design brief
// §2.1, team-lead ruling on the matched_term_hash contract change):
// (org, kind, canonical_id, matched_term_hash) in -> valid/invalid + reason
// out, question-free -- re-reads the identity universe hash-side and
// re-proves EXACTLY ONE row still carries the matched term AND that row's
// (kind, canonical_id) still equals the stored pair.
//
// The production implementation (internal/runtime/hosted/open.go) adapts
// graphrank.VerifyAnchorClaimantUnique over the SAME identity-universe read
// devhealthsource.IdentityUniverse already provides. A nil AnchorVerifier
// is NOT "trust the stored offer": see canonicalizeStructure's own
// reverify wiring below -- same fail-closed default as HandleVerifier,
// same reasoning (applying an un-reverified anchor claim would be a false
// sense of safety).
type AnchorVerifier func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason)

// AnchorMembershipVerifier is CHAOS-4042's (sol-max ruling) own
// redemption-time re-verification dependency for a v2 (membership-verify)
// subject_anchor confirmation: (principal, requestedScope, binding, kind,
// canonicalID, matchedTermHash) -> valid/invalid + reason.
//
// Unlike AnchorVerifier (v1, permanently unique-claimant: EXACTLY ONE row
// may carry the term), this proves MEMBERSHIP: (kind, canonicalID) remains
// ANY member of the term's complete claimant set under the PINNED binding
// epoch B -- rivals gained or lost do not invalidate the claim; only the
// SELECTED claimant's own loss, re-keying, or an incomplete/unprovable
// read does (`valid = complete(C_B(h)) && e ∈ C_B(h) && authorized_B(...)`,
// the ruling's own membership rule). Matched by (matchedTermHash, kind,
// canonicalID) together, never canonicalID or matchedTermHash alone.
//
// Takes the FULL storage.Principal and the caller's own RequestedScope
// (AnchorVerifier takes only an org id) because membership verification
// must RE-AUTHORIZE the selected node at redemption under B -- the
// ruling's explicit "never compute truth from the authorized subset;
// authorization must not manufacture uniqueness by hiding rivals" applies
// in reverse here too: redemption must re-check visibility, never assume
// offer-time authorization still holds.
//
// A nil AnchorMembershipVerifier is NOT "trust the stored offer" -- same
// fail-CLOSED default as AnchorVerifier/HandleVerifier. Production
// implementation: internal/runtime/hosted/open.go.
type AnchorMembershipVerifier func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason)

// CandidateVerificationReason is the closed vocabulary Engine's candidate
// re-verification dependency reports (CHAOS-4012). Deliberately a SMALLER
// vocabulary than AnchorVerificationReason: a candidate offer claims no
// per-term uniqueness (see ContextFabricCandidateOption's own CHANGE LOG-
// style doc comment), so there is no "contested"/"incomplete enumeration"
// case to distinguish -- only "still exists and is authorized" or not, and
// "the graph binding itself could not be read at all."
type CandidateVerificationReason string

const (
	CandidateVerificationValid CandidateVerificationReason = "valid"
	// CandidateVerificationClaimLost is CandidateVerifier's own "the node
	// no longer exists, or is no longer authorized for this principal, at
	// the pinned binding epoch" -- the SAME generic vetoed_unresolved wire
	// disposition every other lost/unresolved reason in this file maps to.
	CandidateVerificationClaimLost CandidateVerificationReason = "candidate_claim_lost"
	// CandidateVerificationGraphUnverifiable mirrors
	// AnchorVerificationGraphUnverifiable's own reasoning exactly: the
	// pinned binding's own graph key could not be read at all (a retired
	// epoch) or the graph-side read errored -- CANNOT-VERIFY, distinct from
	// ClaimLost (a stale binding proves nothing about whether the
	// candidate still exists in a LIVE epoch).
	CandidateVerificationGraphUnverifiable CandidateVerificationReason = "graph_binding_unverifiable"
)

// CandidateVerifier is canonicalizeStructure's own redemption-time
// re-verification dependency for the subject_candidate member (CHAOS-4012,
// team-lead ratified design): re-proves that (kind, canonicalID) still
// exists as a real, authorized node at the PINNED ResolvedGraphBinding
// epoch -- a keyed graph existence + re-authorization read, the SAME
// primitive AnchorMembershipVerifier's own graph-side half already uses
// (graphrank.GraphAnchorMemberFunc / falkorgraph.Adapter.AnchorMember),
// reused directly rather than duplicated: a candidate offer claims nothing
// beyond "this subject ranked in the resolution's own top N," so there is
// no ClickHouse identity-universe/term-uniqueness layer to check first the
// way anchor redemption needs -- the graph-side existence+authorization
// check IS the entire proof.
//
// The production implementation (internal/runtime/hosted/open.go) adapts
// the SAME graphReader.AnchorMember construction anchorMembershipVerifier
// already wires, over the SAME binding this request already resolved --
// no new graph backend method, no new composition-root dependency. A nil
// CandidateVerifier is NOT "trust the stored offer": same fail-CLOSED
// default as every other reverify dependency in this file.
type CandidateVerifier func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID string) (bool, CandidateVerificationReason)

// HandleGrammarChecker is the OFFER-TIME (never redemption-time) grammar
// check for an explicit request.SubjectHandles entry (CHAOS-3972 P3,
// design brief §2.1: "offered when a grammar-valid value arrived
// explicit_unattributed"). PURE, no I/O, no org parameter -- unlike
// HandleVerifier above, this never proves the value keys an existing
// source row (that existence proof is redemption's own job, via
// HandleVerifier, once the caller redeems the receipt this check made
// possible); it only proves the (kind, pattern_id, value) triple is a
// syntactically valid member of the closed handle-grammar registry, so an
// invalid explicit value never becomes a receipt-bound offer.
//
// Threaded through graphrank.ResolveDeps (not an Engine-level dependency
// the way HandleVerifier/AnchorVerifier are): the consumer is
// graphrank.handleOfferMaterial, called from ResolveSubjects at OFFER-BUILD
// time -- before a result exists for canonicalizeStructure's own
// redemption path to run against. See ResolveDeps.HandleGrammarChecker's
// own doc comment (resolve.go). The production implementation
// (internal/runtime/hosted/open.go) adapts graphrank.ValidateHandleGrammar
// directly -- a pure function already, no wrapping needed beyond the type
// conversion. sourceColumn is looked up alongside
// (graphrank.HandleSourceColumn) so a valid explicit handle can mint a
// full ContextFabricHandleOption in one call. nil (never wired) means an
// explicit subject_handle can never become an offer -- the safe
// degradation, never a veto.
type HandleGrammarChecker func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (sourceColumn string, ok bool)

// structureReceiptMember describes one of the three P1.B receipt
// namespaces uniformly, so canonicalizeStructure resolves all three
// through ONE loop rather than three hand-copies -- the same DRY
// discipline validate_context_fabric_structure.go's shared prefix-checker
// map already applies to validation.
type structureReceiptMember struct {
	member          contractsv1.ContextFabricStructureNeedKind
	receipts        []contractsv1.ContextFabricBoundSubjectReceipt
	appliedValueFor func(stored InvestigationResult, receiptID string) (value string, kind contractsv1.ContextFabricSubjectKind, offerSource contractsv1.ContextFabricStructureOfferSource, priorVersionID, priorEntryID string, ok bool)
	// reverify (P1.E) is canonicalizeStructure's own redemption-time
	// re-verification hook, additional to appliedValueFor's stored-offer
	// lookup above. nil means the stored offer's content is trusted as
	// persisted, exactly as every member behaved before P1.E (this file's
	// own SCOPE NOTE) -- still true for expected_kind only (no live
	// tampering vector: the confirmed kind only narrows a pool, it never
	// stands in for a fact). subject_anchor and subject_handle both set
	// this to a non-nil closure over Engine.anchorVerifier/handleVerifier
	// respectively.
	reverify func(ctx context.Context, principal storage.Principal, stored InvestigationResult, receiptID string) bool
	// offerSnapshot (P1.G) echoes EVERY offer in stored's own offer list
	// for this member (not just the redeemed one) as
	// ContextFabricStructureOfferSnapshotEntry rows, Rank = the offer's
	// own position in that list -- see requestStructureCanonicalization.
	// OfferSnapshot's own doc comment for why the full list, not just the
	// winner.
	offerSnapshot func(stored InvestigationResult) []contractsv1.ContextFabricStructureOfferSnapshotEntry
	// offeredOptions (CHAOS-3927 P4) builds the SAME full offer list
	// offerSnapshot does, but as StructureOfferedOption -- carrying each
	// offer's own typed AppliedValue (a SubjectKind string, a canonical
	// anchor id, or a handle's literal value), which offerSnapshot's
	// canonical-storage-only wire echo deliberately omits (ids/ranks/enums
	// only, never a re-derivable value). captureStructureSelection is the
	// only caller -- StructureSelectionEvent needs the actual value to be
	// a useful training signal at all.
	offeredOptions func(stored InvestigationResult) []StructureOfferedOption
}

// canonicalizeStructure is design brief §2.1's own entry point, called from
// Investigate at the SAME point as canonicalizeEvidenceWindow -- BEFORE
// tryReuse, BEFORE Interpret. It never returns an error: every failure mode
// it recognizes is a VETO, not a Go error, mirroring
// canonicalizeEvidenceWindow's own reasoning (a canonicalization failure is
// an ordinary, expected, clarification-shaped outcome).
func (e *Engine) canonicalizeStructure(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding) requestStructureCanonicalization {
	hasReceipts := len(request.PriorKindReceipts) > 0 || len(request.PriorAnchorReceipts) > 0 || len(request.PriorHandleReceipts) > 0 || len(request.PriorCandidateReceipts) > 0
	hasExplicit := len(request.ExpectedKinds) > 0 || len(request.SubjectHandles) > 0
	if !hasReceipts && !hasExplicit {
		return requestStructureCanonicalization{}
	}
	if !hasReceipts {
		// CHAOS-3972 P3: no receipts at all -- skip straight to explicit
		// resolution, which needs no result store (there is nothing to
		// load).
		explicit, veto := e.resolveExplicitStructure(request, nil)
		if veto != structureVetoNone {
			return requestStructureCanonicalization{Veto: veto}
		}
		return requestStructureCanonicalization{Explicit: explicit}
	}
	if e.results == nil {
		// No store to resolve against -- cannot confirm, so this cannot
		// proceed as if nothing had been asked (mirrors
		// resolveWindowReceipts' own fail-closed rule).
		return requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved}
	}

	members := []structureReceiptMember{
		{
			member:   contractsv1.ContextFabricStructureNeedExpectedKind,
			receipts: request.PriorKindReceipts,
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricSubjectKind, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.KindOptions {
					if opt.ReceiptID == receiptID {
						return string(opt.Kind), opt.Kind, opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", "", false
			},
			offerSnapshot: func(stored InvestigationResult) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
				if stored.StructureNeeds == nil {
					return nil
				}
				return kindOptionsSnapshot(stored.StructureNeeds.KindOptions)
			},
			offeredOptions: func(stored InvestigationResult) []StructureOfferedOption {
				if stored.StructureNeeds == nil {
					return nil
				}
				return kindOptionsOffered(stored.StructureNeeds.KindOptions)
			},
		},
		{
			member:   contractsv1.ContextFabricStructureNeedSubjectAnchor,
			receipts: request.PriorAnchorReceipts,
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricSubjectKind, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.AnchorOptions {
					if opt.ReceiptID == receiptID {
						return opt.CanonicalID, opt.Kind, opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", "", false
			},
			offerSnapshot: func(stored InvestigationResult) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
				if stored.StructureNeeds == nil {
					return nil
				}
				return anchorOptionsSnapshot(stored.StructureNeeds.AnchorOptions)
			},
			offeredOptions: func(stored InvestigationResult) []StructureOfferedOption {
				if stored.StructureNeeds == nil {
					return nil
				}
				return anchorOptionsOffered(stored.StructureNeeds.AnchorOptions)
			},
			// reverify (P1.E, team-lead ruling on the matched_term_hash
			// contract change): an ancr_ redemption re-proves the offer's
			// per-TERM claimant uniqueness still holds -- (kind,
			// canonical_id) alone cannot detect a rival gaining the SAME
			// alias after this anchor was offered (the CHAOS-3917 class),
			// so the stored offer's own matched_term_hash is re-checked
			// hash-side against a fresh identity-universe read.
			// e.anchorVerifier == nil fails CLOSED, not open -- identical
			// reasoning to the handle member's own reverify above.
			// CHAOS-4042 (sol-max ruling): dispatch on the ISSUING STORED
			// result's OWN schema_version, exactly as persisted -- never on
			// this request's own structure material, never on any other
			// signal. A v1-stamped stored result always redeems through
			// the legacy unique-claimant verifier; a v2-stamped one always
			// redeems through the membership verifier. Any OTHER
			// schema_version fails closed (neither branch runs) -- an
			// explicit reject, never a fallthrough that would let an
			// unrecognized or future major redeem under today's rules.
			reverify: func(ctx context.Context, principal storage.Principal, stored InvestigationResult, receiptID string) bool {
				if stored.StructureNeeds == nil {
					return false
				}
				for _, opt := range stored.StructureNeeds.AnchorOptions {
					if opt.ReceiptID != receiptID {
						continue
					}
					switch stored.SchemaVersion {
					case InvestigationResultSchemaV1:
						if e.anchorVerifier == nil {
							return false
						}
						// Codex xhigh review (chaos-pivot-p1, first round),
						// finding 2: ok alone is not trustworthy -- a
						// misconfigured AnchorVerifier returning
						// (true, AnchorVerificationClaimLost) (or any
						// non-Valid reason) must not open redemption.
						// Require the reason to be the closed vocabulary's
						// own Valid member too, not just a truthy bool.
						ok, reason := e.anchorVerifier(ctx, principal.OrgID, opt.Kind, opt.CanonicalID, opt.MatchedTermHash)
						return ok && reason == AnchorVerificationValid
					case InvestigationResultSchemaV2:
						if e.anchorMembershipVerifier == nil {
							return false
						}
						ok, reason := e.anchorMembershipVerifier(ctx, principal, request.RequestedScope, binding, opt.Kind, opt.CanonicalID, opt.MatchedTermHash)
						return ok && reason == AnchorVerificationValid
					default:
						return false
					}
				}
				return false
			},
		},
		{
			member:   contractsv1.ContextFabricStructureNeedSubjectHandle,
			receipts: request.PriorHandleReceipts,
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricSubjectKind, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.HandleOptions {
					if opt.ReceiptID == receiptID {
						return opt.Value, opt.Kind, opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", "", false
			},
			offerSnapshot: func(stored InvestigationResult) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
				if stored.StructureNeeds == nil {
					return nil
				}
				return handleOptionsSnapshot(stored.StructureNeeds.HandleOptions)
			},
			offeredOptions: func(stored InvestigationResult) []StructureOfferedOption {
				if stored.StructureNeeds == nil {
					return nil
				}
				return handleOptionsOffered(stored.StructureNeeds.HandleOptions)
			},
			// reverify (P1.E): a handr_ redemption re-validates the stored
			// value's grammar AND re-runs the keyed source-row existence
			// check (design brief §2.1) -- a value that was offerable when
			// it was disclosed is NOT assumed still valid at redemption
			// time. e.handleVerifier == nil fails CLOSED, not open: an
			// unwired verifier means this deployment cannot uphold the
			// re-verification the design brief requires, and applying the
			// stored value anyway would be a false sense of safety, not a
			// weaker-but-honest check (the same reasoning that blocked
			// anchor re-verification from silently degrading to a
			// canonical-id-only check).
			reverify: func(ctx context.Context, principal storage.Principal, stored InvestigationResult, receiptID string) bool {
				if e.handleVerifier == nil || stored.StructureNeeds == nil {
					return false
				}
				for _, opt := range stored.StructureNeeds.HandleOptions {
					if opt.ReceiptID == receiptID {
						// Codex xhigh review (chaos-pivot-p1, first round),
						// finding 2: same reasoning as the anchor reverify
						// above -- require reason == HandleVerificationValid,
						// not just a truthy ok, so a misconfigured
						// HandleVerifier cannot open redemption on an
						// inconsistent (true, non-valid-reason) result.
						ok, reason := e.handleVerifier(ctx, principal.OrgID, opt.Kind, opt.PatternID, opt.Value)
						return ok && reason == HandleVerificationValid
					}
				}
				return false
			},
		},
		{
			member:   contractsv1.ContextFabricStructureNeedSubjectCandidate,
			receipts: request.PriorCandidateReceipts,
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricSubjectKind, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.CandidateOptions {
					if opt.ReceiptID == receiptID {
						return opt.CanonicalID, opt.Kind, opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", "", false
			},
			offerSnapshot: func(stored InvestigationResult) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
				if stored.StructureNeeds == nil {
					return nil
				}
				return candidateOptionsSnapshot(stored.StructureNeeds.CandidateOptions)
			},
			offeredOptions: func(stored InvestigationResult) []StructureOfferedOption {
				if stored.StructureNeeds == nil {
					return nil
				}
				return candidateOptionsOffered(stored.StructureNeeds.CandidateOptions)
			},
			// reverify (CHAOS-4012): a candr_ redemption re-proves the
			// stored (kind, canonical_id) still exists as a real, authorized
			// node at the PINNED binding epoch -- CandidateVerifier's own
			// doc comment for why this is the graph-side-only half of
			// anchor membership verification, never a term-uniqueness
			// proof. e.candidateVerifier == nil fails CLOSED, not open --
			// same reasoning as every other reverify above: an unwired
			// verifier means this deployment cannot uphold the
			// re-verification this member requires, and applying the
			// stored value anyway would be a false sense of safety.
			reverify: func(ctx context.Context, principal storage.Principal, stored InvestigationResult, receiptID string) bool {
				if e.candidateVerifier == nil || stored.StructureNeeds == nil {
					return false
				}
				for _, opt := range stored.StructureNeeds.CandidateOptions {
					if opt.ReceiptID == receiptID {
						ok, reason := e.candidateVerifier(ctx, principal, request.RequestedScope, binding, opt.Kind, opt.CanonicalID)
						return ok && reason == CandidateVerificationValid
					}
				}
				return false
			},
		},
	}

	var confirmed []confirmedStructureMember
	var offerSnapshot []contractsv1.ContextFabricStructureOfferSnapshotEntry
	var pendingSelections []StructureSelectionEvent
	for _, m := range members {
		if len(m.receipts) == 0 {
			continue
		}
		if len(m.receipts) > 1 {
			return requestStructureCanonicalization{
				Veto:          structureVetoConfirmationConflict,
				VetoedEntries: vetoedConfirmedEntries(confirmed, contractsv1.ContextFabricStructureDispositionVetoedConflict),
			}
		}
		receipt := m.receipts[0]
		resultID := strings.TrimSpace(receipt.ResultID)
		receiptID := strings.TrimSpace(receipt.ReceiptID)
		if resultID == "" || receiptID == "" {
			return requestStructureCanonicalization{
				Veto:          structureVetoConfirmationUnresolved,
				VetoedEntries: vetoedConfirmedEntries(confirmed, contractsv1.ContextFabricStructureDispositionVetoedUnresolved),
			}
		}
		stored, err := e.results.Get(ctx, principal, resultID)
		if err != nil {
			return requestStructureCanonicalization{
				Veto:          structureVetoConfirmationUnresolved,
				VetoedEntries: vetoedConfirmedEntries(confirmed, contractsv1.ContextFabricStructureDispositionVetoedUnresolved),
			}
		}
		value, kind, offerSource, priorVersionID, priorEntryID, ok := m.appliedValueFor(stored.Result, receiptID)
		if !ok {
			return requestStructureCanonicalization{
				Veto:          structureVetoConfirmationUnresolved,
				VetoedEntries: vetoedConfirmedEntries(confirmed, contractsv1.ContextFabricStructureDispositionVetoedUnresolved),
			}
		}
		if m.reverify != nil && !m.reverify(ctx, principal, stored.Result, receiptID) {
			// Unlike the failure modes above, appliedValueFor already
			// resolved a real value here -- reverify rejected it, not the
			// lookup -- so this member's own attempted state IS echoable
			// (Validate's non-empty applied_value requirement is
			// satisfiable), and is appended after the batch's own
			// already-confirmed members.
			entries := vetoedConfirmedEntries(confirmed, contractsv1.ContextFabricStructureDispositionVetoedUnresolved)
			entries = append(entries, triggeringMemberEntry(m.member, contractsv1.ContextFabricStructureDispositionVetoedUnresolved, resultID, receiptID, value, offerSource, priorVersionID, priorEntryID))
			return requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved, VetoedEntries: entries}
		}
		// CHAOS-3927 P4 (design brief §2.1 offer-supersession rule): a
		// receipt that resolves cleanly against its stored offer can still
		// name an offer a LATER result has already redeemed for this same
		// (org, prior result, member) tuple -- reverify above proves the
		// offer's own content is still valid, not that it is still the
		// CURRENT one. Consult the claim table, when the wired store
		// supports it, BEFORE trusting this redemption. checker is a type
		// assertion, not a required dependency (StructureSupersessionChecker's
		// own doc comment) -- absent, this consult is simply skipped and
		// Save's own atomic claim remains the sole (still sufficient)
		// enforcement point.
		if checker, ok := e.results.(StructureSupersessionChecker); ok {
			superseded, err := checker.IsStructureSuperseded(ctx, principal.OrgID, resultID, m.member)
			if err != nil || superseded {
				// Fail-closed on an authority-relevant read (design brief
				// §2.0): an unreadable claim table is treated identically
				// to a confirmed-stale claim, never as "assume fresh."
				stale := contractsv1.ContextFabricConfirmedStructureEntry{
					Member: m.member, AppliedValue: value, Source: contractsv1.ContextFabricStructureSourceReceipt,
					PriorResultID: resultID, ReceiptID: receiptID, OfferSource: offerSource,
					PriorVersionID: priorVersionID, PriorEntryID: priorEntryID,
					Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
				}
				return requestStructureCanonicalization{Veto: structureVetoStaleSupersededOffer, StaleMembers: []contractsv1.ContextFabricConfirmedStructureEntry{stale}}
			}
		}
		confirmed = append(confirmed, confirmedStructureMember{
			Member: m.member, AppliedValue: value, AppliedKind: kind, PriorResultID: resultID, ReceiptID: receiptID,
			OfferSource: offerSource, PriorVersionID: priorVersionID, PriorEntryID: priorEntryID,
		})
		if m.offerSnapshot != nil {
			offerSnapshot = append(offerSnapshot, m.offerSnapshot(stored.Result)...)
		}
		// CHAOS-3927 P4 (codex adversarial review fix): BUILD, never yet
		// send, a StructureSelectionEvent for this confirmed member -- this
		// is the only point canonicalizeStructure confirms a member, so it
		// is the only point the event's content can honestly be built
		// from, but sending it here (P4's first pass) durably recorded a
		// selection for a round that could still lose the Save-time
		// supersession claim race and be discarded -- see
		// requestStructureCanonicalization.PendingSelections' own doc
		// comment for the full reasoning and where these actually get
		// sent.
		if e.structureSelectionSink != nil && m.offeredOptions != nil {
			pendingSelections = append(pendingSelections, e.buildStructureSelectionEvent(principal, request.Consumer, m.member, resultID, stored.Result, m.offeredOptions(stored.Result), receiptID))
		}
	}
	// CHAOS-3972 P3 (design brief §2.5's "explicit structure via MCP
	// surface" row): explicit ExpectedKinds/SubjectHandles resolve AFTER
	// every receipt-bearing member has cleared reverification AND the
	// CHAOS-3927 P4 supersession pre-flight above (a mid-loop supersession
	// veto returns before this point is ever reached, so confirmed here
	// only ever contains members that passed both checks) -- see
	// resolveExplicitStructure's own doc comment for the agreement/
	// conflict rule against confirmed.
	explicit, veto := e.resolveExplicitStructure(request, confirmed)
	if veto != structureVetoNone {
		// CHAOS-3963: the only veto resolveExplicitStructure ever returns
		// is an explicit-vs-receipt conflict. The conflicting member
		// itself is by definition already in confirmed (that is the
		// precondition confirmedMemberValue/confirmedMemberKindValue
		// requires before detecting disagreement) -- vetoedConfirmedEntries
		// echoes it. codex xhigh round-1 finding (MEDIUM): `explicit` can
		// ALSO carry an earlier member resolveExplicitStructure already
		// built cleanly before hitting the conflicting one (e.g. an
		// explicit expected_kind resolved fine, then explicit
		// subject_handles conflicted with a receipt) -- vetoedExplicitEntries
		// re-dispositions those too, closing the same silent-drop gap on
		// the explicit side.
		return requestStructureCanonicalization{
			Veto: veto,
			VetoedEntries: append(
				vetoedConfirmedEntries(confirmed, contractsv1.ContextFabricStructureDispositionVetoedConflict),
				vetoedExplicitEntries(explicit, contractsv1.ContextFabricStructureDispositionVetoedConflict)...,
			),
		}
	}
	return requestStructureCanonicalization{Confirmed: confirmed, OfferSnapshot: offerSnapshot, PendingSelections: pendingSelections, Explicit: explicit}
}

// buildStructureSelectionEvent builds (but never sends) a
// StructureSelectionEvent (CHAOS-3927 P4) for one receipt
// canonicalizeStructure just confirmed against a real offer in a real
// prior result. Sending is Engine's own responsibility, deferred until the
// caller can prove this round's Save actually won every supersession claim
// it needed -- see requestStructureCanonicalization.PendingSelections' own
// doc comment. This function itself never touches e.structureSelectionSink
// at all -- callers already gate on it being non-nil before calling this
// (canonicalizeStructure's own loop, above), matching the "don't do
// pointless work when capture is off" discipline every other optional
// dependency in this package follows.
func (e *Engine) buildStructureSelectionEvent(principal storage.Principal, consumer ConsumerInfo, member contractsv1.ContextFabricStructureNeedKind, priorResultID string, prior InvestigationResult, offered []StructureOfferedOption, receiptID string) StructureSelectionEvent {
	var selected StructureOfferedOption
	for _, opt := range offered {
		if opt.ReceiptID == receiptID {
			selected = opt
			break
		}
	}
	return StructureSelectionEvent{
		OrgID: principal.OrgID, CapturedAt: e.now().UTC(),
		QuestionHash: QuestionHash(prior.Question), PriorResultID: priorResultID,
		Member: string(member), Offered: offered, Selected: selected, Accepted: selected.Rank == 0,
		SelectionMode:       structureSelectionMode(principal),
		SelectionProvenance: clarificationSelectionProvenance(principal, consumer),
		ProjectionVersion:   e.reuseProjectionVersion, ModelIdentities: e.reuseModelIdentities,
		RetrievalIdentity: e.reuseRetrievalIdentity, PromptVersions: e.reusePromptVersions,
		VersionAuthorities: e.reuseVersionAuthorities,
	}
}

// recordStructureConfirmationOutcome (CHAOS-3927 P4, codex round-2
// adversarial review fix) records the FINAL "applied" telemetry outcome
// and flushes structureCanon.PendingSelections to the wired sink, for a
// structure confirmation whose owning Save has just genuinely succeeded.
//
// SHARED between every Save call site that can carry a non-empty
// structureCanon.Confirmed -- today that is Investigate's own decisive
// path (engine.go) AND terminalResult's own subjectless-terminal path
// (unresolved.go): a confirmed kind/anchor/handle can just as easily
// narrow a census down to zero committed subjects (a no_match/
// clarification_required terminal) as it can reach a synthesized answer,
// and round 1's fix deferring capture/telemetry to "right after Save
// succeeds" only touched Investigate's own call site, silently dropping
// BOTH signals for every subjectless-terminal confirmation (round-2
// finding). A future THIRD Save call site that can carry confirmed
// structure must call this too, not hand-copy its body.
func (e *Engine) recordStructureConfirmationOutcome(ctx context.Context, principal storage.Principal, request InvestigationRequest, structureCanon requestStructureCanonicalization) {
	// CHAOS-3972 P3: cf_structure_explicit{member,outcome} -- called
	// UNCONDITIONALLY, ahead of the Confirmed-gated return below, since an
	// explicit-only request (ExpectedKinds/SubjectHandles with NO receipts
	// at all) reaches this SAME deferred success-path call with an empty
	// Confirmed -- the veto path (engine.go, right after
	// canonicalizeStructure returns a non-nil Veto) already records this
	// unconditionally for the veto case; this is its success-path twin,
	// deferred to the SAME point recordStructureReceiptTelemetry's own
	// success-path call is, for symmetry, even though (unlike receipts)
	// nothing about an explicit field's own outcome can still change
	// between canonicalizeStructure returning and Save succeeding.
	recordStructureExplicitTelemetry(ctx, e.telemetry, principal, request, structureCanon)
	if len(structureCanon.Confirmed) == 0 {
		return
	}
	recordStructureReceiptTelemetry(ctx, e.telemetry, principal, request, structureCanon)
	if e.structureSelectionSink != nil {
		for _, event := range structureCanon.PendingSelections {
			e.structureSelectionSink.RecordSelection(ctx, event)
		}
	}
}

// structureSupersessionVetoResult (CHAOS-3927 P4, codex round-2
// adversarial review fix) converts a Save-time ErrStructureOfferSuperseded
// into the SAME stale_superseded_offer veto terminal a pre-flight
// detection would have produced, recording "stale" telemetry (never
// "applied", and never sending structureCanon.PendingSelections -- the
// result they describe was never persisted).
//
// SHARED for the identical reason recordStructureConfirmationOutcome
// above is: every Save call site that can carry confirmed structure can
// also lose the atomic claim race to a concurrent one, and round 1's fix
// only wired this conversion into Investigate's own call site, leaving
// terminalResult's own Save call to surface the race as a raw persistence
// error (500) instead of the handled veto terminal (round-2 finding).
//
// confirmed (CHAOS-4003) is the caller's MERGED confirmed-member list --
// structureCanon.Confirmed plus window's own ConfirmedMember, when present
// (mergeConfirmedMembers, window.go) -- not structureCanon itself, since a
// window-only claim loss at Save time must echo exactly like a
// kind/anchor/handle one does, via the SAME staleConfirmedStructureEntries
// call below.
func (e *Engine) structureSupersessionVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, confirmed []confirmedStructureMember, superseded *ErrStructureOfferSuperseded, binding ResolvedGraphBinding) (InvestigationResult, error) {
	// CHAOS-3972 P3: cf_structure_explicit{member,outcome} -- the SAME
	// synthetic Veto:structureVetoStaleSupersededOffer canonicalization
	// recordStructureReceiptTelemetry's own call above uses, so an
	// explicit-bearing member on THIS race path is recorded as
	// non-applied too, never silently left unrecorded or misreported as
	// "applied" against a round that was actually discarded.
	recordStructureExplicitTelemetry(ctx, e.telemetry, principal, request, requestStructureCanonicalization{Veto: structureVetoStaleSupersededOffer})
	return e.structureVetoResult(ctx, principal, request, structureVetoStaleSupersededOffer, staleConfirmedStructureEntries(confirmed, superseded.Members), binding)
}

// resolveExplicitStructure implements design brief §2.5's "explicit
// structure via MCP surface" row for CHAOS-3972 P3's own request.ExpectedKinds/
// SubjectHandles fields: each enters at Provenance
// (structureExplicitAuthority -- inferred_default/explicit_unattributed on
// MCP, question_stated on every other surface, mirroring
// windowExplicitProvenance exactly) UNLESS a receipt already confirmed
// that SAME member, in which case receipt provenance wins and the two
// values must AGREE (design brief §2.1's explicit-vs-receipt conflict
// rule) -- disagreement is an atomic batch veto, exactly like a
// plural-receipt conflict.
//
// A multi-valued explicit field (len>1, no single value to state as ONE
// applied value) drives census-narrowing/offer-shaping only -- see
// ResolveSubjects/kindOfferMaterial -- and deliberately produces NO
// ConfirmedStructureEntry echo (there is nothing singular to echo); this
// is a scoped, flagged gap (this file's own "flag rather than silently
// omit" convention), not a silent drop: cf_structure_explicit telemetry
// still fires per member (recordStructureExplicitTelemetry, called from
// the same site canonicalizeStructure's other telemetry is called from).
func (e *Engine) resolveExplicitStructure(request InvestigationRequest, confirmed []confirmedStructureMember) ([]explicitStructureMember, structureVetoReason) {
	source, provenance := structureExplicitAuthority(request.Consumer)
	var explicit []explicitStructureMember

	if len(request.ExpectedKinds) > 0 {
		if confirmedValue, ok := confirmedMemberValue(confirmed, contractsv1.ContextFabricStructureNeedExpectedKind); ok {
			if !containsSubjectKind(request.ExpectedKinds, contractsv1.ContextFabricSubjectKind(confirmedValue)) {
				// codex xhigh review (CHAOS-3963 round 1, MEDIUM finding):
				// `explicit` is provably empty at this exact point (this is
				// the FIRST block that can append to it), but return it
				// rather than nil anyway -- matching the SubjectHandles
				// block's own fix below and removing any dependence on
				// block ordering never changing.
				return explicit, structureVetoConfirmationConflict
			}
		} else if len(request.ExpectedKinds) == 1 {
			explicit = append(explicit, explicitStructureMember{
				Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(request.ExpectedKinds[0]),
				Source: source, Provenance: provenance,
			})
		}
	}

	if len(request.SubjectHandles) > 0 {
		if confirmedValue, confirmedKind, ok := confirmedMemberKindValue(confirmed, contractsv1.ContextFabricStructureNeedSubjectHandle); ok {
			// codex xhigh review, CHAOS-3972 round 1, finding 3: agreement
			// must match on (kind, value) TOGETHER -- a stored
			// (pull_request, "532") receipt and an explicit
			// (work_item, "532") request name DIFFERENT subjects that
			// merely share a numeric string, and must never be read as
			// agreement.
			if !containsHandle(request.SubjectHandles, confirmedKind, confirmedValue) {
				// codex xhigh review (CHAOS-3963 round 1, MEDIUM finding):
				// this used to `return nil`, silently discarding an
				// ExpectedKind entry the block above may have already
				// appended to `explicit` -- the exact "member A confirmed
				// fine, member B is why the batch was rejected" gap
				// CHAOS-3963 exists to close, just on the EXPLICIT side
				// instead of the receipt side. Return what was already
				// built; the caller re-dispositions it, mirroring
				// vetoedConfirmedEntries' own treatment of `confirmed`.
				return explicit, structureVetoConfirmationConflict
			}
		} else if len(request.SubjectHandles) == 1 {
			explicit = append(explicit, explicitStructureMember{
				Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedValue: request.SubjectHandles[0].Value,
				Source: source, Provenance: provenance,
			})
		}
	}
	return explicit, structureVetoNone
}

// structureExplicitAuthority implements the DP12(b) uniform surface split
// (pivot-intent design brief, ratified 07:28 08-19) for the kind/handle
// members, mirroring windowExplicitProvenance's own identical rule
// exactly: tier is a function of SURFACE alone. MCP's own bare explicit
// field can never itself grant question_stated -- only receipt redemption
// can. Every other surface keeps 3900 v5.2's ratified stated-echo
// semantics, untouched by this ruling.
func structureExplicitAuthority(consumer ConsumerInfo) (contractsv1.ContextFabricStructureSource, contractsv1.ContextFabricStructureProvenance) {
	if strings.TrimSpace(consumer.Surface) == "mcp" {
		return contractsv1.ContextFabricStructureSourceExplicitUnattributed, contractsv1.ContextFabricStructureInferredDefault
	}
	return contractsv1.ContextFabricStructureSourceExplicit, contractsv1.ContextFabricStructureQuestionStated
}

// confirmedMemberValue looks up member's AppliedValue in confirmed, if a
// receipt confirmed it.
func confirmedMemberValue(confirmed []confirmedStructureMember, member contractsv1.ContextFabricStructureNeedKind) (string, bool) {
	for _, c := range confirmed {
		if c.Member == member {
			return c.AppliedValue, true
		}
	}
	return "", false
}

// confirmedMemberKindValue is confirmedMemberValue's own (value, kind)
// pair variant -- subject_handle's own explicit-vs-receipt agreement check
// needs BOTH (codex xhigh review, CHAOS-3972 round 1, finding 3): value
// alone cannot distinguish a genuinely agreeing explicit handle from one
// that merely shares the same numeric string with a DIFFERENT kind.
func confirmedMemberKindValue(confirmed []confirmedStructureMember, member contractsv1.ContextFabricStructureNeedKind) (value string, kind contractsv1.ContextFabricSubjectKind, ok bool) {
	for _, c := range confirmed {
		if c.Member == member {
			return c.AppliedValue, c.AppliedKind, true
		}
	}
	return "", "", false
}

func containsSubjectKind(kinds []contractsv1.ContextFabricSubjectKind, kind contractsv1.ContextFabricSubjectKind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// containsHandle reports whether handles carries an entry naming BOTH the
// same kind and the same value -- see confirmedMemberKindValue's own doc
// comment for why value alone is not a safe agreement test.
func containsHandle(handles []contractsv1.ContextFabricRequestedHandle, kind contractsv1.ContextFabricSubjectKind, value string) bool {
	for _, h := range handles {
		if h.Kind == kind && h.Value == value {
			return true
		}
	}
	return false
}

// kindOptionsSnapshot/anchorOptionsSnapshot/handleOptionsSnapshot (P1.G)
// each build one member's ContextFabricStructureOfferSnapshotEntry list
// from its own offer type -- three short, near-identical functions rather
// than one generic one, matching structureReceiptMember's own established
// per-member-explicit discipline (never a reflection-based shortcut over
// the three distinct offer structs). Rank is the offer's own position in
// its source list -- the SAME order it was disclosed in, never re-sorted.
func kindOptionsSnapshot(opts []contractsv1.ContextFabricKindOption) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
	if len(opts) == 0 {
		return nil
	}
	out := make([]contractsv1.ContextFabricStructureOfferSnapshotEntry, 0, len(opts))
	for i, opt := range opts {
		out = append(out, contractsv1.ContextFabricStructureOfferSnapshotEntry{
			Member: contractsv1.ContextFabricStructureNeedExpectedKind, OfferID: opt.OptionID, Rank: i,
			OfferSource: opt.OfferSource, PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

func anchorOptionsSnapshot(opts []contractsv1.ContextFabricAnchorOption) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
	if len(opts) == 0 {
		return nil
	}
	out := make([]contractsv1.ContextFabricStructureOfferSnapshotEntry, 0, len(opts))
	for i, opt := range opts {
		out = append(out, contractsv1.ContextFabricStructureOfferSnapshotEntry{
			Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, OfferID: opt.OptionID, Rank: i,
			OfferSource: opt.OfferSource, PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

func handleOptionsSnapshot(opts []contractsv1.ContextFabricHandleOption) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
	if len(opts) == 0 {
		return nil
	}
	out := make([]contractsv1.ContextFabricStructureOfferSnapshotEntry, 0, len(opts))
	for i, opt := range opts {
		out = append(out, contractsv1.ContextFabricStructureOfferSnapshotEntry{
			Member: contractsv1.ContextFabricStructureNeedSubjectHandle, OfferID: opt.OptionID, Rank: i,
			OfferSource: opt.OfferSource, PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

// kindOptionsOffered/anchorOptionsOffered/handleOptionsOffered (CHAOS-3927
// P4) each build one member's StructureOfferedOption list from its own
// offer type -- the offeredOptions twin of
// kindOptionsSnapshot/anchorOptionsSnapshot/handleOptionsSnapshot above,
// carrying AppliedValue (the SubjectKind, canonical anchor id, or handle
// value the wire-facing snapshot deliberately omits) for
// captureStructureSelection's own use. Same "three short, near-identical
// functions rather than one generic one" discipline as their snapshot
// twins.
func kindOptionsOffered(opts []contractsv1.ContextFabricKindOption) []StructureOfferedOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]StructureOfferedOption, 0, len(opts))
	for i, opt := range opts {
		out = append(out, StructureOfferedOption{
			ReceiptID: opt.ReceiptID, AppliedValue: string(opt.Kind), Rank: i,
			OfferSource: string(opt.OfferSource), PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

func anchorOptionsOffered(opts []contractsv1.ContextFabricAnchorOption) []StructureOfferedOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]StructureOfferedOption, 0, len(opts))
	for i, opt := range opts {
		out = append(out, StructureOfferedOption{
			ReceiptID: opt.ReceiptID, AppliedValue: opt.CanonicalID, Rank: i,
			OfferSource: string(opt.OfferSource), PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

func handleOptionsOffered(opts []contractsv1.ContextFabricHandleOption) []StructureOfferedOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]StructureOfferedOption, 0, len(opts))
	for i, opt := range opts {
		out = append(out, StructureOfferedOption{
			ReceiptID: opt.ReceiptID, AppliedValue: opt.Value, Rank: i,
			OfferSource: string(opt.OfferSource), PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

// candidateOptionsSnapshot/candidateOptionsOffered (CHAOS-4012) are
// kindOptionsSnapshot/kindOptionsOffered's own twins for the
// subject_candidate member.
func candidateOptionsSnapshot(opts []contractsv1.ContextFabricCandidateOption) []contractsv1.ContextFabricStructureOfferSnapshotEntry {
	if len(opts) == 0 {
		return nil
	}
	out := make([]contractsv1.ContextFabricStructureOfferSnapshotEntry, 0, len(opts))
	for i, opt := range opts {
		out = append(out, contractsv1.ContextFabricStructureOfferSnapshotEntry{
			Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, OfferID: opt.OptionID, Rank: i,
			OfferSource: opt.OfferSource, PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

func candidateOptionsOffered(opts []contractsv1.ContextFabricCandidateOption) []StructureOfferedOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]StructureOfferedOption, 0, len(opts))
	for i, opt := range opts {
		out = append(out, StructureOfferedOption{
			ReceiptID: opt.ReceiptID, AppliedValue: opt.CanonicalID, Rank: i,
			OfferSource: string(opt.OfferSource), PriorVersionID: opt.PriorVersionID, PriorEntryID: opt.PriorEntryID,
		})
	}
	return out
}

// structureVetoLimitation names the single fixed disclosure a structure
// veto attaches, mirroring windowVetoLimitations' own closed map -- both
// veto reasons currently share one sentence (unlike window's three), so a
// map is not yet warranted; revisit once a second distinct disclosure
// exists.
func structureVetoLimitation(veto structureVetoReason) string {
	switch veto {
	case structureVetoConfirmationConflict:
		return "a structure confirmation receipt conflicted with another and could not be applied"
	case structureVetoStaleSupersededOffer:
		return "a structure confirmation receipt named an offer a newer result has already superseded"
	default:
		return "a structure confirmation receipt could not be resolved"
	}
}

// structureVetoResult composes the model-free, no_match result for a
// structure canonicalization veto -- mirrors windowVetoResult's own
// discipline exactly (every answer-bearing field empty by construction).
// Unlike windowVetoResult, structure vetoes are ALWAYS pre-Interpret in
// this slice (P1.B resolves receipts only, and receipts are checked before
// Interpret ever runs) -- no post-Interpret axis-conflict analogue exists
// for structure the way windowVetoAxisConflict does, so this signature
// carries no *InterpretedQuestion parameter.
// echoEntries, when non-empty, is composed onto the resulting
// InvestigationResult.ConfirmedStructure VERBATIM (design brief §2.5's
// per-veto echo rows). Callers pass requestStructureCanonicalization.StaleMembers
// on the veto==structureVetoStaleSupersededOffer path (one entry from the
// pre-flight consult, or every member ErrStructureOfferSuperseded.Members
// named from the Save-time race -- codex round-3 adversarial review,
// MEDIUM finding: a single-entry parameter here silently dropped every
// member past the first when a Save-time race conflicted on more than
// one), and requestStructureCanonicalization.VetoedEntries (CHAOS-3963) on
// the structureVetoConfirmationUnresolved/structureVetoConfirmationConflict
// paths -- StaleMembers and VetoedEntries are mutually exclusive by
// construction (each is populated by different veto reasons), so callers
// pass whichever one the veto reason actually populated.
func (e *Engine) structureVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, veto structureVetoReason, echoEntries []contractsv1.ContextFabricConfirmedStructureEntry, binding ResolvedGraphBinding) (InvestigationResult, error) {
	limitation := structureVetoLimitation(veto)
	resolvedInterpretation := InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: windowVetoPlaceholderJudgment,
		TimeContext:       request.TimeContext,
		FactRequirements:  []FactRequirement{},
	}
	emptyCoverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	result := InvestigationResult{
		SchemaVersion:       InvestigationResultSchemaV1,
		ResultID:            e.newResultID(),
		RequestID:           request.RequestID,
		GeneratedAt:         e.now().UTC(),
		Status:              InvestigationNoMatch,
		Question:            request.Question,
		Reused:              false,
		Interpretation:      resolvedInterpretation,
		SubjectResolution:   SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		DirectJudgment:      "",
		CurrentState:        "",
		StrongestPressures:  []string{},
		Drivers:             []DriverJudgment{},
		RemainingWork:       []Finding{},
		ReadinessGaps:       []Finding{},
		Paths:               []RelationshipPath{},
		Conflicts:           []Finding{},
		Limitations:         []string{limitation},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []ClaimedFact{},
		Coverage:            emptyCoverage,
		Temporal:            composeTemporalLabel(resolvedInterpretation, emptyCoverage, ""),
		Versions:            e.terminalVersions(),
		DeterministicAnswer: limitation,
		Warnings:            []string{},
	}
	if len(echoEntries) > 0 {
		result.ConfirmedStructure = echoEntries
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		if err := e.results.Save(ctx, principal, result, nil, nil, TimeAxisKeyFor(request.TimeContext), e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, binding.Epoch); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}

// composeConfirmedStructure maps canonicalizeStructure's own internal
// confirmations into the wire-visible echo (design brief §2.1's
// silent-drop closure): every member canonicalizeStructure resolved was
// resolved via a receipt, so Source/Provenance/Disposition are fixed for
// every entry this function produces.
//
// CLOSED (P1.B's own former KNOWN GAP, closed by CHAOS-3963): a VETOED
// request now composes per-member vetoed_unresolved/vetoed_conflict echo
// entries the way design brief §2.5's closed table describes
// ("ConfirmedStructure... present whenever the request carried ANY
// structure receipt... including the vetoed ones") -- canonicalizeStructure's
// loop now threads `confirmed` (everything already resolved before the
// veto fired) and, where a value exists to echo, the triggering member's
// own entry through requestStructureCanonicalization.VetoedEntries (see
// vetoedConfirmedEntries/triggeringMemberEntry below) instead of
// discarding them the instant it returns. The request is still rejected
// wholesale -- the all-or-nothing rule is unchanged, nothing from a
// vetoed batch is ever applied -- only the DISCLOSURE gap is closed.
// composeConfirmedStructure merges canonicalizeStructure's own receipt-
// confirmed members (confirmed) with its explicit (non-receipt) members
// (explicit -- CHAOS-3972 P3) into the wire-visible echo. The two lists
// are disjoint by construction (resolveExplicitStructure never adds a
// member confirmedMemberValue already found in confirmed), so simple
// concatenation is correct order: receipt-confirmed members first
// (matching this function's own pre-P3 order), explicit members after.
func composeConfirmedStructure(confirmed []confirmedStructureMember, explicit []explicitStructureMember) []contractsv1.ContextFabricConfirmedStructureEntry {
	if len(confirmed) == 0 && len(explicit) == 0 {
		return nil
	}
	entries := make([]contractsv1.ContextFabricConfirmedStructureEntry, 0, len(confirmed)+len(explicit))
	for _, c := range confirmed {
		entries = append(entries, contractsv1.ContextFabricConfirmedStructureEntry{
			Member:         c.Member,
			AppliedValue:   c.AppliedValue,
			Source:         contractsv1.ContextFabricStructureSourceReceipt,
			PriorResultID:  c.PriorResultID,
			ReceiptID:      c.ReceiptID,
			OfferSource:    c.OfferSource,
			PriorVersionID: c.PriorVersionID,
			PriorEntryID:   c.PriorEntryID,
			Provenance:     contractsv1.ContextFabricStructureClarificationConfirmed,
			Disposition:    contractsv1.ContextFabricStructureDispositionApplied,
		})
	}
	for _, x := range explicit {
		entries = append(entries, contractsv1.ContextFabricConfirmedStructureEntry{
			Member:       x.Member,
			AppliedValue: x.AppliedValue,
			Source:       x.Source,
			Provenance:   x.Provenance,
			Disposition:  contractsv1.ContextFabricStructureDispositionApplied,
		})
	}
	return entries
}

// vetoedConfirmedEntries (CHAOS-3963) re-dispositions every member
// canonicalizeStructure's receipt loop had already resolved into
// `confirmed` before an atomic veto fired -- Disposition dropped from
// applied to the veto's own vetoed_unresolved/vetoed_conflict, mirroring
// staleConfirmedStructureEntries' own re-dispositioning for the
// supersession case just below. The all-or-nothing rule means none of
// these members were actually applied, so echoing "applied" would
// misreport the outcome; nil confirmed (the common case: the very first
// member evaluated is the one that vetoes) returns nil, same as every
// other empty-echo path in this file.
func vetoedConfirmedEntries(confirmed []confirmedStructureMember, disposition contractsv1.ContextFabricStructureDisposition) []contractsv1.ContextFabricConfirmedStructureEntry {
	if len(confirmed) == 0 {
		return nil
	}
	entries := make([]contractsv1.ContextFabricConfirmedStructureEntry, 0, len(confirmed))
	for _, c := range confirmed {
		entries = append(entries, contractsv1.ContextFabricConfirmedStructureEntry{
			Member:         c.Member,
			AppliedValue:   c.AppliedValue,
			Source:         contractsv1.ContextFabricStructureSourceReceipt,
			PriorResultID:  c.PriorResultID,
			ReceiptID:      c.ReceiptID,
			OfferSource:    c.OfferSource,
			PriorVersionID: c.PriorVersionID,
			PriorEntryID:   c.PriorEntryID,
			Provenance:     contractsv1.ContextFabricStructureClarificationConfirmed,
			Disposition:    disposition,
		})
	}
	return entries
}

// vetoedExplicitEntries (CHAOS-3963, codex xhigh round-1 finding) is
// vetoedConfirmedEntries' own twin for the EXPLICIT (non-receipt) side:
// resolveExplicitStructure can build one explicit member (e.g.
// expected_kind) cleanly, then veto on a LATER member's explicit-vs-receipt
// conflict (e.g. subject_handle) -- without this, that already-built
// explicit member was silently dropped from the echo, the exact "member A
// was fine, member B is why the batch was rejected" gap CHAOS-3963 exists
// to close, just on the explicit side instead of the receipt side.
// Re-dispositions from applied to the veto's own vetoed_conflict (the only
// veto reason resolveExplicitStructure ever returns); AppliedValue/Source/
// Provenance come from the explicit member itself, mirroring
// composeConfirmedStructure's own explicit-entry composition exactly,
// disposition swapped.
func vetoedExplicitEntries(explicit []explicitStructureMember, disposition contractsv1.ContextFabricStructureDisposition) []contractsv1.ContextFabricConfirmedStructureEntry {
	if len(explicit) == 0 {
		return nil
	}
	entries := make([]contractsv1.ContextFabricConfirmedStructureEntry, 0, len(explicit))
	for _, x := range explicit {
		entries = append(entries, contractsv1.ContextFabricConfirmedStructureEntry{
			Member:       x.Member,
			AppliedValue: x.AppliedValue,
			Source:       x.Source,
			Provenance:   x.Provenance,
			Disposition:  disposition,
		})
	}
	return entries
}

// triggeringMemberEntry (CHAOS-3963) builds the ConfirmedStructure echo
// entry for the ONE member whose own resolution attempt triggered an
// atomic veto, for the one loop failure mode that has a real value to
// echo (a reverify rejection -- appliedValueFor already resolved value
// before reverify ran). Every earlier failure mode (plural receipts, a
// malformed receipt, an unloadable prior result, an offer the stored
// result never actually made) never resolves a value at all, and
// ContextFabricConfirmedStructureEntry.Validate requires applied_value to
// be non-empty -- there is no valid entry those modes could construct, so
// callers do not call this for them (matching resolveExplicitStructure's
// own "flag rather than silently omit" convention for its analogous
// multi-valued-explicit-field gap: some source states genuinely have no
// singular value to echo).
func triggeringMemberEntry(member contractsv1.ContextFabricStructureNeedKind, disposition contractsv1.ContextFabricStructureDisposition, resultID, receiptID, appliedValue string, offerSource contractsv1.ContextFabricStructureOfferSource, priorVersionID, priorEntryID string) contractsv1.ContextFabricConfirmedStructureEntry {
	return contractsv1.ContextFabricConfirmedStructureEntry{
		Member:         member,
		AppliedValue:   appliedValue,
		Source:         contractsv1.ContextFabricStructureSourceReceipt,
		PriorResultID:  resultID,
		ReceiptID:      receiptID,
		OfferSource:    offerSource,
		PriorVersionID: priorVersionID,
		PriorEntryID:   priorEntryID,
		Provenance:     contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:    disposition,
	}
}

// staleConfirmedStructureEntries (CHAOS-3927 P4) rebuilds the echo entries
// for the Save-time supersession race (engine.go's/unresolved.go's own
// ErrStructureOfferSuperseded handling): confirmed is the SAME
// structureCanon.Confirmed slice the discarded decisive result was built
// from, and members is ErrStructureOfferSuperseded.Members -- EVERY
// confirmed member whose atomic claim actually lost the race, which can be
// more than one (codex round-3 adversarial review, MEDIUM finding: an
// earlier version of this function returned only the FIRST matching
// entry, silently dropping every member past it from the wire-visible
// echo when a single Save lost the claim on more than one member at
// once). Returns one entry per matching member, in confirmed's own order,
// Disposition forced to vetoed_stale -- structureVetoResult composes them
// exactly like a pre-flight-detected stale veto's own StaleMembers, so a
// caller cannot distinguish which detection path caught the staleness
// from the response alone.
func staleConfirmedStructureEntries(confirmed []confirmedStructureMember, members []contractsv1.ContextFabricStructureNeedKind) []contractsv1.ContextFabricConfirmedStructureEntry {
	lost := make(map[contractsv1.ContextFabricStructureNeedKind]bool, len(members))
	for _, m := range members {
		lost[m] = true
	}
	var entries []contractsv1.ContextFabricConfirmedStructureEntry
	for _, c := range confirmed {
		if !lost[c.Member] {
			continue
		}
		entries = append(entries, contractsv1.ContextFabricConfirmedStructureEntry{
			Member: c.Member, AppliedValue: c.AppliedValue, Source: contractsv1.ContextFabricStructureSourceReceipt,
			PriorResultID: c.PriorResultID, ReceiptID: c.ReceiptID, OfferSource: c.OfferSource,
			PriorVersionID: c.PriorVersionID, PriorEntryID: c.PriorEntryID,
			Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
		})
	}
	return entries
}

// structureReceiptPrefixForMember maps each closed StructureNeedKind
// member to its own receipt namespace prefix (design brief §2.1's
// receipt-namespace table, extended to window per the team-lead ruling
// that minting be member-generic so W2's window offers ride this SAME
// primitive rather than a second bespoke path).
func structureReceiptPrefixForMember(member contractsv1.ContextFabricStructureNeedKind) string {
	switch member {
	case contractsv1.ContextFabricStructureNeedExpectedKind:
		return contractsv1.ContextFabricKindOptionReceiptPrefix
	case contractsv1.ContextFabricStructureNeedSubjectAnchor:
		return contractsv1.ContextFabricAnchorOptionReceiptPrefix
	case contractsv1.ContextFabricStructureNeedSubjectHandle:
		return contractsv1.ContextFabricHandleOptionReceiptPrefix
	case contractsv1.ContextFabricStructureNeedWindow:
		return contractsv1.ContextFabricWindowOptionReceiptPrefix
	case contractsv1.ContextFabricStructureNeedSubjectCandidate:
		return contractsv1.ContextFabricCandidateOptionReceiptPrefix
	default:
		return ""
	}
}

// mintStructureReceiptID and mintStructureOptionID derive DETERMINISTIC,
// content-addressed ids for one structure offer (design brief §2.1's
// typed, receipt-bound offers). Team-lead ruling (P1.C): member-generic
// and deterministic from (result identity, member, option content) --
// not random -- so a retry of the SAME investigation mints the SAME
// offer ids (idempotent re-mint), a replay is stable, and two offers are
// unique-within-result BY CONSTRUCTION: identical content for the same
// member in the same result is, definitionally, the same offer, so
// collapsing them onto one id is correct, never a collision to guard
// against.
//
// content is structureOfferContent's own canonical serialization of the
// WHOLE offer struct (see that function's doc comment for why: round-1
// finding 7 and round-2 finding 3 were the SAME defect class twice --
// content built from a hand-picked field list silently under-covering a
// wire field nobody remembered to add). See composeStructureNeeds' own
// call sites for exactly what each member passes.
//
// member MUST be a member structureReceiptPrefixForMember recognizes;
// callers within this package only ever pass a closed-vocabulary
// constant, never a caller-supplied value, so an unrecognized member
// here is a programming error, not a runtime input to guard defensively
// -- the empty-prefix result is deliberately left un-namespaced rather
// than panicking, so a future member added to the wire vocabulary without
// a matching case here fails Validate() (missing/wrong namespace prefix)
// instead of crashing the engine.
func mintStructureReceiptID(member contractsv1.ContextFabricStructureNeedKind, resultID, content string) string {
	prefix := structureReceiptPrefixForMember(member)
	sum := sha256.Sum256([]byte(resultID + "\x00" + string(member) + "\x00" + content))
	return prefix + hex.EncodeToString(sum[:])[:24]
}

func mintStructureOptionID(member contractsv1.ContextFabricStructureNeedKind, resultID, content string) string {
	sum := sha256.Sum256([]byte("opt\x00" + resultID + "\x00" + string(member) + "\x00" + content))
	return "opt_" + hex.EncodeToString(sum[:])[:16]
}

// structureOfferContent derives mintStructureReceiptID/mintStructureOptionID's
// own content input from v's FULL wire struct (a KindOption/AnchorOption/
// HandleOption value with ReceiptID/OptionID already cleared by the
// caller -- those are the fields being computed FROM this content, so
// they must never feed back into it) via json.Marshal, not a hand-picked
// field list.
//
// Team-lead ruling (chaos-pivot-p1, post-round-2): round-1 finding 7
// (offer_source/prior_version_id/prior_entry_id omitted) and round-2
// finding 3 (label omitted) were the SAME defect class landing TWICE --
// each fix-forward closed the specific fields that round caught, leaving
// the underlying pattern (content = whatever fields someone remembered to
// list) able to repeat for the next field this repo's own future work
// adds to any of these three types. Serializing the whole struct instead
// makes that omission structurally impossible: a new wire field joins
// every option's identity automatically, the moment it exists on the
// struct, with no second edit required here.
//
// json.Marshal on these types is exactly as deterministic as the old
// hand-built strings: none of the three carries a map, slice, or any
// other field whose Go encoding varies between equal values, so struct
// field order (fixed at compile time) is ALL that decides byte order,
// every call, forever. Marshal can only fail on types encoding/json
// cannot represent (channels, funcs, cyclic values) -- none of which a
// plain string/enum struct like these three can ever contain -- so the
// error path below is unreachable in practice; it still returns a
// deterministic (Go-syntax) fallback rather than panicking, because a
// minted id must never be able to crash the engine.
func structureOfferContent(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

// StructureNeedsWouldDisclose reports whether composeStructureNeeds would
// compose a non-nil StructureNeeds block for material -- the single source
// of truth for that decision. composeStructureNeeds itself calls this
// (never a second, parallel len(material.Missing) != 0 check), so the two
// can never diverge.
//
// Exported (CHAOS-3927 P1 post-merge invariance measurement, codex xhigh
// review finding, chaos-replay-structure-needs-coverage round 1): the
// replay harness (internal/runtime/hosted) cannot call composeStructureNeeds
// directly -- it has no result identity to mint receipt/option ids with --
// but needs to ask this SAME question about a StructureOfferMaterial it
// already has. The harness calls this function directly rather than
// re-deriving the condition, closing the "two hand-written copies of the
// same gate can silently drift" defect a test-only contract lock could not
// actually prevent (a test pins two SEPARATE expressions to agree today;
// it cannot stop them from being edited independently tomorrow -- sharing
// the one function does).
func StructureNeedsWouldDisclose(material StructureOfferMaterial) bool {
	return len(material.Missing) != 0
}

// composeStructureNeeds fills in the deterministic receipt_id/option_id
// for every offer StructureOfferMaterial carries -- ResultID was not yet
// known when ResolveSubjects built the material's own CONTENT fields, so
// minting waits until the result composing this disclosure has one (see
// StructureOfferMaterial's own doc comment). nil whenever
// StructureNeedsWouldDisclose is false (nothing to disclose -- mirrors
// composeEffectiveWindow's own nil-means-nothing-in-play convention).
func composeStructureNeeds(material StructureOfferMaterial, resultID string) *contractsv1.ContextFabricStructureNeeds {
	if !StructureNeedsWouldDisclose(material) {
		return nil
	}
	needs := &contractsv1.ContextFabricStructureNeeds{Missing: material.Missing}
	if len(material.KindOptions) > 0 {
		needs.KindOptions = make([]contractsv1.ContextFabricKindOption, 0, len(material.KindOptions))
		for _, opt := range material.KindOptions {
			contentSrc := opt
			contentSrc.ReceiptID, contentSrc.OptionID = "", ""
			content := structureOfferContent(contentSrc)
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedExpectedKind, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedExpectedKind, resultID, content)
			needs.KindOptions = append(needs.KindOptions, opt)
		}
	}
	if len(material.AnchorOptions) > 0 {
		needs.AnchorOptions = make([]contractsv1.ContextFabricAnchorOption, 0, len(material.AnchorOptions))
		for _, opt := range material.AnchorOptions {
			contentSrc := opt
			contentSrc.ReceiptID, contentSrc.OptionID = "", ""
			content := structureOfferContent(contentSrc)
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedSubjectAnchor, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedSubjectAnchor, resultID, content)
			needs.AnchorOptions = append(needs.AnchorOptions, opt)
		}
	}
	if len(material.HandleOptions) > 0 {
		needs.HandleOptions = make([]contractsv1.ContextFabricHandleOption, 0, len(material.HandleOptions))
		for _, opt := range material.HandleOptions {
			contentSrc := opt
			contentSrc.ReceiptID, contentSrc.OptionID = "", ""
			content := structureOfferContent(contentSrc)
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedSubjectHandle, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedSubjectHandle, resultID, content)
			needs.HandleOptions = append(needs.HandleOptions, opt)
		}
	}
	if len(material.CandidateOptions) > 0 {
		needs.CandidateOptions = make([]contractsv1.ContextFabricCandidateOption, 0, len(material.CandidateOptions))
		for _, opt := range material.CandidateOptions {
			contentSrc := opt
			contentSrc.ReceiptID, contentSrc.OptionID = "", ""
			content := structureOfferContent(contentSrc)
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedSubjectCandidate, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedSubjectCandidate, resultID, content)
			needs.CandidateOptions = append(needs.CandidateOptions, opt)
		}
	}
	return needs
}

// confirmedExpectedKind extracts the expected_kind member from a
// resolved confirmation set, if the request's receipts confirmed one
// (CHAOS-3900 P1.D). Returns nil when no expected_kind was confirmed --
// the common case, and the ONLY way a caller of ResolveSubjects can
// obtain a nil *ConfirmedExpectedKind, which is what keeps an ordinary
// no-receipt request's pool composition byte-identical to pre-P1
// (filterCandidatesByConfirmedKind is a no-op on nil).
func confirmedExpectedKind(confirmed []confirmedStructureMember) *ConfirmedExpectedKind {
	for _, c := range confirmed {
		if c.Member == contractsv1.ContextFabricStructureNeedExpectedKind {
			return &ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectKind(c.AppliedValue)}
		}
	}
	return nil
}

// confirmedAnchorSelection (CHAOS-4042, sol-max ruling) is
// confirmedExpectedKind's own sibling for the subject_anchor member: nil
// when no ancr_ receipt was confirmed this round (the common case, keeping
// an ordinary request byte-identical to before this ticket). The receipt-
// confirmation loop (canonicalizeStructure, above) already populates
// AppliedKind/AppliedValue with the redeemed AnchorOption's own Kind/
// CanonicalID for the subject_anchor member -- this reads that back, it
// does not re-derive anything.
func confirmedAnchorSelection(confirmed []confirmedStructureMember) *ConfirmedAnchorSelection {
	for _, c := range confirmed {
		if c.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			return &ConfirmedAnchorSelection{Kind: c.AppliedKind, CanonicalID: c.AppliedValue}
		}
	}
	return nil
}

// StructureReceiptOutcome is the closed vocabulary RecordStructureReceipt
// reports (CHAOS-3900 P1.F, design brief §2.1's cf_structure_receipt{member,
// outcome=applied|unresolved|conflict}).
//
// Atomicity (canonicalizeStructure's own all-or-nothing batch veto) means
// every receipt-bearing member in ONE Investigate call shares the SAME
// outcome: either the whole batch resolved (every bearing member
// "applied") or the whole batch vetoed (every bearing member gets the
// veto's own reason) -- there is no partial-application case to represent,
// matching §2.5's own "partial batch... impossible by construction" row.
type StructureReceiptOutcome string

const (
	StructureReceiptApplied    StructureReceiptOutcome = "applied"
	StructureReceiptUnresolved StructureReceiptOutcome = "unresolved"
	StructureReceiptConflict   StructureReceiptOutcome = "conflict"
	// StructureReceiptStale (CHAOS-3927 P4) reports the
	// stale_superseded_offer veto specifically -- kept distinct from
	// StructureReceiptUnresolved so an operator can tell "the receipt
	// itself was malformed/unmatched" apart from "the receipt was fine but
	// a newer result already redeemed it," which have different recovery
	// stories (retry the same receipt vs. fetch fresh offers).
	StructureReceiptStale StructureReceiptOutcome = "stale"
)

// structureReceiptOutcomeForVeto maps a structureVetoReason to its own
// telemetry outcome -- structureVetoLimitation's own switch, extended with
// the SAME third reason (kept in sync by construction: both read
// structureVetoReason's own named constants, never an untracked one).
func structureReceiptOutcomeForVeto(veto structureVetoReason) StructureReceiptOutcome {
	switch veto {
	case structureVetoConfirmationConflict:
		return StructureReceiptConflict
	case structureVetoStaleSupersededOffer:
		return StructureReceiptStale
	default:
		return StructureReceiptUnresolved
	}
}

// recordStructureReceiptTelemetry (CHAOS-3900 P1.F) is canonicalizeStructure's
// own telemetry companion, called unconditionally right after it returns
// (both the veto and success path) -- one RecordStructureReceipt call per
// member that carried at least one receipt, per structureReceiptMember's
// own three-field mapping (PriorKindReceipts/PriorAnchorReceipts/
// PriorHandleReceipts). Never touches canonicalizeStructure's own tested
// control flow: the outcome is fully recoverable after the fact from
// requestStructureCanonicalization's own Veto/Confirmed fields, per
// StructureReceiptOutcome's own atomicity doc comment.
func recordStructureReceiptTelemetry(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, request InvestigationRequest, canon requestStructureCanonicalization) {
	if telemetry == nil {
		return
	}
	bearing := []struct {
		member   contractsv1.ContextFabricStructureNeedKind
		receipts []contractsv1.ContextFabricBoundSubjectReceipt
	}{
		{contractsv1.ContextFabricStructureNeedExpectedKind, request.PriorKindReceipts},
		{contractsv1.ContextFabricStructureNeedSubjectAnchor, request.PriorAnchorReceipts},
		{contractsv1.ContextFabricStructureNeedSubjectHandle, request.PriorHandleReceipts},
	}
	outcome := StructureReceiptApplied
	if canon.Veto != structureVetoNone {
		outcome = structureReceiptOutcomeForVeto(canon.Veto)
	}
	for _, b := range bearing {
		if len(b.receipts) == 0 {
			continue
		}
		telemetry.RecordStructureReceipt(ctx, principal, b.member, outcome)
	}
}

// StructureExplicitOutcome is the closed vocabulary RecordStructureExplicit
// reports (CHAOS-3972 P3, design brief §2.1/§2.5's cf_structure_explicit{member,
// outcome}). Mirrors StructureReceiptOutcome's own atomicity: an explicit
// field's outcome is the SAME batch-wide veto/no-veto structureCanon
// already carries (resolveExplicitStructure never partially applies), so
// there is no "unresolved" member here the way a receipt can be
// unresolved -- an explicit value is either applied or the whole batch
// conflicted.
type StructureExplicitOutcome string

const (
	StructureExplicitApplied  StructureExplicitOutcome = "applied"
	StructureExplicitConflict StructureExplicitOutcome = "conflict"
)

// recordStructureExplicitTelemetry (CHAOS-3972 P3) is
// canonicalizeStructure's own explicit-field telemetry companion, called
// unconditionally right after it returns (both the veto and success
// path) -- mirrors recordStructureReceiptTelemetry's own placement and
// atomicity reasoning exactly, one call per member that carried at least
// one explicit value (request.ExpectedKinds/SubjectHandles).
func recordStructureExplicitTelemetry(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, request InvestigationRequest, canon requestStructureCanonicalization) {
	if telemetry == nil {
		return
	}
	outcome := StructureExplicitApplied
	if canon.Veto != structureVetoNone {
		outcome = StructureExplicitConflict
	}
	if len(request.ExpectedKinds) > 0 {
		telemetry.RecordStructureExplicit(ctx, principal, contractsv1.ContextFabricStructureNeedExpectedKind, outcome)
	}
	if len(request.SubjectHandles) > 0 {
		telemetry.RecordStructureExplicit(ctx, principal, contractsv1.ContextFabricStructureNeedSubjectHandle, outcome)
	}
}

// recordStructureNeedsTelemetry (CHAOS-3900 P1.F) reports
// cf_structure_needs_disclosed{member} and cf_structure_offer_count{member,
// source} for one composed StructureNeeds block -- called immediately after
// StructureNeeds is built, at every call site that ever composes one: the
// subjectless-terminal path (unresolved.go) for the kind/anchor/handle
// members, and windowConfirmationRequiredResult (window.go, CHAOS-4118) for
// the window member. needs==nil (nothing disclosed) records nothing,
// exactly mirroring composeStructureNeeds' own nil-means-nothing-in-play
// convention.
func recordStructureNeedsTelemetry(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, needs *contractsv1.ContextFabricStructureNeeds) {
	if telemetry == nil || needs == nil {
		return
	}
	for _, member := range needs.Missing {
		telemetry.RecordStructureNeedsDisclosed(ctx, principal, member)
	}
	counts := map[contractsv1.ContextFabricStructureNeedKind]map[contractsv1.ContextFabricStructureOfferSource]int{}
	addCount := func(member contractsv1.ContextFabricStructureNeedKind, source contractsv1.ContextFabricStructureOfferSource) {
		if counts[member] == nil {
			counts[member] = map[contractsv1.ContextFabricStructureOfferSource]int{}
		}
		counts[member][source]++
	}
	for _, opt := range needs.KindOptions {
		addCount(contractsv1.ContextFabricStructureNeedExpectedKind, opt.OfferSource)
	}
	for _, opt := range needs.AnchorOptions {
		addCount(contractsv1.ContextFabricStructureNeedSubjectAnchor, opt.OfferSource)
	}
	for _, opt := range needs.HandleOptions {
		addCount(contractsv1.ContextFabricStructureNeedSubjectHandle, opt.OfferSource)
	}
	// CHAOS-4118: ContextFabricWindowOption carries no OfferSource field --
	// a window offer is always derived from the closed relative-window
	// registry (composeWindowClarification), never prior-sourced -- so
	// every entry counts as engine-sourced, unconditionally.
	for range needs.WindowOptions {
		addCount(contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureOfferEngine)
	}
	// Deterministic iteration: members in Missing's own disclosure-priority
	// order, sources in a fixed order -- so a caller comparing telemetry
	// call sequences across two identical runs sees identical ordering.
	for _, member := range needs.Missing {
		for _, source := range []contractsv1.ContextFabricStructureOfferSource{contractsv1.ContextFabricStructureOfferEngine, contractsv1.ContextFabricStructureOfferPrior} {
			count := counts[member][source]
			if count == 0 {
				continue
			}
			telemetry.RecordStructureOfferCount(ctx, principal, member, source, count)
		}
	}
}
