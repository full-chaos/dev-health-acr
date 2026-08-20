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
	Member         contractsv1.ContextFabricStructureNeedKind
	AppliedValue   string
	PriorResultID  string
	ReceiptID      string
	OfferSource    contractsv1.ContextFabricStructureOfferSource
	PriorVersionID string
	PriorEntryID   string
}

// requestStructureCanonicalization is canonicalizeStructure's own result.
type requestStructureCanonicalization struct {
	// Confirmed lists every member this request's receipts resolved.
	// NON-EMPTY means the request BYPASSES tryReuse entirely (design brief
	// §2.1/DP11: "any request whose canonicalized confirmed-structure set
	// is non-empty skips the reuse lookup entirely" -- a bypass, not a
	// ReuseKey fold-in).
	Confirmed []confirmedStructureMember
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
	// round is NOT yet at the moment canonicalizeStructure returns. Engine
	// sends these only once Save has actually won every claim they depend
	// on (engine.go, right after a successful decisive Save) -- never on
	// the veto path (nothing was durably confirmed) and never on the
	// Save-time-race path (the result these describe was never persisted).
	PendingSelections []StructureSelectionEvent
	// StaleMember (CHAOS-3927 P4) is populated ONLY alongside
	// Veto==structureVetoStaleSupersededOffer -- the single member whose
	// stored offer was already superseded, composed eagerly here (this is
	// the one veto class the design brief's §2.5 table explicitly requires
	// an echo entry for: "echo disposition vetoed_stale") rather than
	// deferred the way the pre-existing structureVetoConfirmationConflict/
	// structureVetoConfirmationUnresolved gap is (composeConfirmedStructure's
	// own KNOWN GAP comment) -- canonicalizeStructure already has every
	// field the echo needs in scope at the exact moment it detects
	// staleness, so composing it here costs nothing extra.
	StaleMember *contractsv1.ContextFabricConfirmedStructureEntry
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

// structureReceiptMember describes one of the three P1.B receipt
// namespaces uniformly, so canonicalizeStructure resolves all three
// through ONE loop rather than three hand-copies -- the same DRY
// discipline validate_context_fabric_structure.go's shared prefix-checker
// map already applies to validation.
type structureReceiptMember struct {
	member          contractsv1.ContextFabricStructureNeedKind
	receipts        []contractsv1.ContextFabricBoundSubjectReceipt
	appliedValueFor func(stored InvestigationResult, receiptID string) (value string, offerSource contractsv1.ContextFabricStructureOfferSource, priorVersionID, priorEntryID string, ok bool)
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
func (e *Engine) canonicalizeStructure(ctx context.Context, principal storage.Principal, request InvestigationRequest) requestStructureCanonicalization {
	if len(request.PriorKindReceipts) == 0 && len(request.PriorAnchorReceipts) == 0 && len(request.PriorHandleReceipts) == 0 {
		return requestStructureCanonicalization{}
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
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.KindOptions {
					if opt.ReceiptID == receiptID {
						return string(opt.Kind), opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", false
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
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.AnchorOptions {
					if opt.ReceiptID == receiptID {
						return opt.CanonicalID, opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", false
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
			reverify: func(ctx context.Context, principal storage.Principal, stored InvestigationResult, receiptID string) bool {
				if e.anchorVerifier == nil || stored.StructureNeeds == nil {
					return false
				}
				for _, opt := range stored.StructureNeeds.AnchorOptions {
					if opt.ReceiptID == receiptID {
						// Codex xhigh review (chaos-pivot-p1, first round),
						// finding 2: ok alone is not trustworthy -- a
						// misconfigured AnchorVerifier returning
						// (true, AnchorVerificationClaimLost) (or any
						// non-Valid reason) must not open redemption.
						// Require the reason to be the closed vocabulary's
						// own Valid member too, not just a truthy bool.
						ok, reason := e.anchorVerifier(ctx, principal.OrgID, opt.Kind, opt.CanonicalID, opt.MatchedTermHash)
						return ok && reason == AnchorVerificationValid
					}
				}
				return false
			},
		},
		{
			member:   contractsv1.ContextFabricStructureNeedSubjectHandle,
			receipts: request.PriorHandleReceipts,
			appliedValueFor: func(stored InvestigationResult, receiptID string) (string, contractsv1.ContextFabricStructureOfferSource, string, string, bool) {
				if stored.StructureNeeds == nil {
					return "", "", "", "", false
				}
				for _, opt := range stored.StructureNeeds.HandleOptions {
					if opt.ReceiptID == receiptID {
						return opt.Value, opt.OfferSource, opt.PriorVersionID, opt.PriorEntryID, true
					}
				}
				return "", "", "", "", false
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
	}

	var confirmed []confirmedStructureMember
	var offerSnapshot []contractsv1.ContextFabricStructureOfferSnapshotEntry
	var pendingSelections []StructureSelectionEvent
	for _, m := range members {
		if len(m.receipts) == 0 {
			continue
		}
		if len(m.receipts) > 1 {
			return requestStructureCanonicalization{Veto: structureVetoConfirmationConflict}
		}
		receipt := m.receipts[0]
		resultID := strings.TrimSpace(receipt.ResultID)
		receiptID := strings.TrimSpace(receipt.ReceiptID)
		if resultID == "" || receiptID == "" {
			return requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved}
		}
		stored, err := e.results.Get(ctx, principal, resultID)
		if err != nil {
			return requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved}
		}
		value, offerSource, priorVersionID, priorEntryID, ok := m.appliedValueFor(stored.Result, receiptID)
		if !ok {
			return requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved}
		}
		if m.reverify != nil && !m.reverify(ctx, principal, stored.Result, receiptID) {
			return requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved}
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
				return requestStructureCanonicalization{Veto: structureVetoStaleSupersededOffer, StaleMember: &stale}
			}
		}
		confirmed = append(confirmed, confirmedStructureMember{
			Member: m.member, AppliedValue: value, PriorResultID: resultID, ReceiptID: receiptID,
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
	return requestStructureCanonicalization{Confirmed: confirmed, OfferSnapshot: offerSnapshot, PendingSelections: pendingSelections}
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
func (e *Engine) structureSupersessionVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, structureCanon requestStructureCanonicalization, superseded *ErrStructureOfferSuperseded, binding ResolvedGraphBinding) (InvestigationResult, error) {
	recordStructureReceiptTelemetry(ctx, e.telemetry, principal, request, requestStructureCanonicalization{Veto: structureVetoStaleSupersededOffer})
	return e.structureVetoResult(ctx, principal, request, structureVetoStaleSupersededOffer, staleConfirmedStructureEntry(structureCanon.Confirmed, superseded.Members), binding)
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
// staleMember, when non-nil, is composed onto the resulting InvestigationResult.ConfirmedStructure
// as its sole entry (design brief §2.5's stale-offer row: "echo disposition
// vetoed_stale") -- callers pass requestStructureCanonicalization.StaleMember
// on the veto==structureVetoStaleSupersededOffer path, and nil for every
// other veto reason (the pre-existing conflict/unresolved echo gap,
// composeConfirmedStructure's own KNOWN GAP comment, unchanged by this
// function).
func (e *Engine) structureVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, veto structureVetoReason, staleMember *contractsv1.ContextFabricConfirmedStructureEntry, binding ResolvedGraphBinding) (InvestigationResult, error) {
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
	if staleMember != nil {
		result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{*staleMember}
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
// KNOWN GAP (P1.B, flagged rather than silently absent): a VETOED request
// (structureVetoResult) does not yet compose per-member vetoed_unresolved/
// vetoed_conflict echo entries the way design brief §2.5's closed table
// describes ("ConfirmedStructure... present whenever the request carried
// ANY structure receipt... including the vetoed ones") -- the atomic veto
// in canonicalizeStructure above discards which specific member/receipt
// triggered it once it returns, so structureVetoResult has nothing to
// compose from yet. The request is still rejected wholesale with a clear
// Limitations disclosure in the meantime; per-member vetoed echo entries
// are a follow-up, not a silent omission.
func composeConfirmedStructure(confirmed []confirmedStructureMember) []contractsv1.ContextFabricConfirmedStructureEntry {
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
			Disposition:    contractsv1.ContextFabricStructureDispositionApplied,
		})
	}
	return entries
}

// staleConfirmedStructureEntry (CHAOS-3927 P4) rebuilds the echo entry for
// the Save-time supersession race (engine.go's own ErrStructureOfferSuperseded
// handling): confirmed is the SAME structureCanon.Confirmed slice the
// discarded decisive result was built from, and members is
// ErrStructureOfferSuperseded.Members -- the subset of those confirmed
// members whose atomic claim actually lost the race. Returns the first
// matching entry, Disposition forced to vetoed_stale -- structureVetoResult
// composes it exactly like a pre-flight-detected stale veto's own
// StaleMember, so a caller cannot distinguish which detection path caught
// the staleness from the response alone.
func staleConfirmedStructureEntry(confirmed []confirmedStructureMember, members []contractsv1.ContextFabricStructureNeedKind) *contractsv1.ContextFabricConfirmedStructureEntry {
	lost := make(map[contractsv1.ContextFabricStructureNeedKind]bool, len(members))
	for _, m := range members {
		lost[m] = true
	}
	for _, c := range confirmed {
		if !lost[c.Member] {
			continue
		}
		return &contractsv1.ContextFabricConfirmedStructureEntry{
			Member: c.Member, AppliedValue: c.AppliedValue, Source: contractsv1.ContextFabricStructureSourceReceipt,
			PriorResultID: c.PriorResultID, ReceiptID: c.ReceiptID, OfferSource: c.OfferSource,
			PriorVersionID: c.PriorVersionID, PriorEntryID: c.PriorEntryID,
			Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionVetoedStale,
		}
	}
	return nil
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

// recordStructureNeedsTelemetry (CHAOS-3900 P1.F) reports
// cf_structure_needs_disclosed{member} and cf_structure_offer_count{member,
// source} for one composed StructureNeeds block -- called only where
// StructureNeeds is ever composed (the subjectless-terminal path,
// unresolved.go), immediately after it is built. needs==nil (nothing
// disclosed) records nothing, exactly mirroring composeStructureNeeds' own
// nil-means-nothing-in-play convention.
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
