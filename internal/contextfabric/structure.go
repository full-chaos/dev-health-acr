package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
}

// structureReceiptMember describes one of the three P1.B receipt
// namespaces uniformly, so canonicalizeStructure resolves all three
// through ONE loop rather than three hand-copies -- the same DRY
// discipline validate_context_fabric_structure.go's shared prefix-checker
// map already applies to validation.
type structureReceiptMember struct {
	member          contractsv1.ContextFabricStructureNeedKind
	receipts        []contractsv1.ContextFabricBoundSubjectReceipt
	appliedValueFor func(stored InvestigationResult, receiptID string) (value string, offerSource contractsv1.ContextFabricStructureOfferSource, priorVersionID, priorEntryID string, ok bool)
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
		},
	}

	var confirmed []confirmedStructureMember
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
		confirmed = append(confirmed, confirmedStructureMember{
			Member: m.member, AppliedValue: value, PriorResultID: resultID, ReceiptID: receiptID,
			OfferSource: offerSource, PriorVersionID: priorVersionID, PriorEntryID: priorEntryID,
		})
	}
	return requestStructureCanonicalization{Confirmed: confirmed}
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
func (e *Engine) structureVetoResult(ctx context.Context, principal storage.Principal, request InvestigationRequest, veto structureVetoReason, binding ResolvedGraphBinding) (InvestigationResult, error) {
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
// content is the caller's own stable serialization of everything that
// makes one offer distinct from a sibling in the SAME member's list
// (e.g. for a KindOption, the kind alone; for an AnchorOption, kind +
// canonical_id + claimant_key) -- see composeStructureOffers' own call
// sites for the exact per-member content strings.
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

// composeStructureNeeds fills in the deterministic receipt_id/option_id
// for every offer StructureOfferMaterial carries -- ResultID was not yet
// known when ResolveSubjects built the material's own CONTENT fields, so
// minting waits until the result composing this disclosure has one (see
// StructureOfferMaterial's own doc comment). nil whenever Missing is
// empty (nothing to disclose -- mirrors composeEffectiveWindow's own
// nil-means-nothing-in-play convention).
func composeStructureNeeds(material StructureOfferMaterial, resultID string) *contractsv1.ContextFabricStructureNeeds {
	if len(material.Missing) == 0 {
		return nil
	}
	needs := &contractsv1.ContextFabricStructureNeeds{Missing: material.Missing}
	if len(material.KindOptions) > 0 {
		needs.KindOptions = make([]contractsv1.ContextFabricKindOption, 0, len(material.KindOptions))
		for _, opt := range material.KindOptions {
			content := string(opt.Kind)
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedExpectedKind, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedExpectedKind, resultID, content)
			needs.KindOptions = append(needs.KindOptions, opt)
		}
	}
	if len(material.AnchorOptions) > 0 {
		needs.AnchorOptions = make([]contractsv1.ContextFabricAnchorOption, 0, len(material.AnchorOptions))
		for _, opt := range material.AnchorOptions {
			content := string(opt.Kind) + "\x00" + opt.CanonicalID + "\x00" + opt.ClaimantKey
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedSubjectAnchor, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedSubjectAnchor, resultID, content)
			needs.AnchorOptions = append(needs.AnchorOptions, opt)
		}
	}
	if len(material.HandleOptions) > 0 {
		needs.HandleOptions = make([]contractsv1.ContextFabricHandleOption, 0, len(material.HandleOptions))
		for _, opt := range material.HandleOptions {
			content := string(opt.Kind) + "\x00" + opt.PatternID + "\x00" + opt.Value + "\x00" + opt.SourceColumn
			opt.ReceiptID = mintStructureReceiptID(contractsv1.ContextFabricStructureNeedSubjectHandle, resultID, content)
			opt.OptionID = mintStructureOptionID(contractsv1.ContextFabricStructureNeedSubjectHandle, resultID, content)
			needs.HandleOptions = append(needs.HandleOptions, opt)
		}
	}
	return needs
}
