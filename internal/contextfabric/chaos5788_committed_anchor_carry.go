package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// CHAOS-5788: a committed anchor with an authoritative identity is carried
// state; a later turn that only refines the member kind must never re-derive
// the subject. The per-need confirmation ledger (chaos5639_confirmed_need.go)
// only ever captured a RECEIPT-REDEEMED confirmation -- a caller picking from
// an offer this engine raised. A subject a resolution commits with no offer
// ever raised (identity_fast_path/caller_canonical_id, no ambiguity, nothing
// for a caller to pick from) had no way to reach that ledger at all, so the
// very next turn's resolution ran with an empty anchor pool scope and lost it.
//
// ConfirmedNeedBasis is the closed vocabulary distinguishing the two ways a
// ledger's subject_anchor member came to be, so a later turn's disclosure can
// tell them apart. Deliberately its OWN type rather than a reuse of
// CommitBasis (chaos4085_commit_basis.go): CommitBasis is declared "NEVER
// WIRE" -- an engine-internal explanation of a proof class that must never
// reach a persisted document -- while this type's whole purpose is to reach
// the wire, through composeCarriedNeedEntry's Provenance. Sharing the type
// would either wire the never-wire vocabulary or force it to grow entries
// (statistical, unknown) this axis has no use for.
type ConfirmedNeedBasis string

const (
	// ConfirmedNeedBasisConfirmed is the zero value: the member was
	// redeemed from a receipt the caller picked, or supplied as an explicit
	// request field -- every ledger entry before this axis existed, and
	// every ordinary confirmation today. Named explicitly, like
	// CommitBasisUnknown, so a log or disclosure line never reads an unset
	// axis as a key nobody wrote.
	ConfirmedNeedBasisConfirmed ConfirmedNeedBasis = ""
	// ConfirmedNeedBasisEngineCommitted is a subject_anchor member the
	// engine bound to the frame's own anchor by resolving it, with no offer
	// ever raised for a caller to redeem: anchorBound's own predicate
	// (count_population_scope.go), gated to the SAME identity-proven bases
	// that predicate already requires.
	ConfirmedNeedBasisEngineCommitted ConfirmedNeedBasis = "engine_committed"
)

// ValidConfirmedNeedBasis reports membership.
func ValidConfirmedNeedBasis(value ConfirmedNeedBasis) bool {
	switch value {
	case ConfirmedNeedBasisConfirmed, ConfirmedNeedBasisEngineCommitted:
		return true
	default:
		return false
	}
}

// provenanceForConfirmedNeedBasis renders the wire Provenance a carried
// ledger entry discloses for basis. Every basis this axis does not recognize
// (including the zero value) renders the ORIGINAL, pre-existing token --
// every entry this package has ever produced before this axis existed reads
// exactly as it always has.
func provenanceForConfirmedNeedBasis(basis ConfirmedNeedBasis) contractsv1.ContextFabricStructureProvenance {
	if basis == ConfirmedNeedBasisEngineCommitted {
		return contractsv1.ContextFabricStructureEngineCommitted
	}
	return contractsv1.ContextFabricStructureClarificationConfirmed
}

// engineCommittedAnchorForCapture decides whether THIS turn's own resolution
// bound a committed subject to the frame's own scope anchor strongly enough
// to persist as carried state for a later turn. Reuses anchorBound's own
// predicate (DecideCountPopulationScope, count_population_scope.go) --
// the SAME test a served count decision stands on -- rather than a second,
// independent notion of "this is the anchor": the two must never be able to
// disagree about which committed subject the frame's anchor names.
//
// Every subject this returns true for is committed on a basis
// CommitBasis.IdentityProven() reports true for (anchorBound admits only a
// caller_canonical_id match or a matched-term match, and CHAOS-4085's
// post-synthesis affirmation gate never retracts either) -- so the member
// this composes can never describe a subject the served document goes on to
// disown.
// The third return is the underlying scope decision, always populated
// whether or not capture happened -- a caller that logs it gets an honest
// reason for a decline (organization_scope, anchor_unresolved,
// anchor_ambiguous, frame_absent) for free, from the SAME predicate the
// capture gate itself stands on, never a second guess about why.
func engineCommittedAnchorForCapture(frame *QuestionFrame, sampleAnchorKind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) (confirmedStructureMember, bool, CountPopulationScopeDecision) {
	// CohortMemberSourceNotApplicable: this decision runs right after
	// ResolveSubjects, before DiscoverContext ever produces a GraphContext --
	// there is no member-source disclosure question in scope here, only
	// whether resolution bound the frame's own anchor.
	scope := DecideCountPopulationScope(frame, sampleAnchorKind, resolution, bases, CohortMemberSourceNotApplicable)
	if scope.Decision != CountPopulationScopeAnchorCommitted {
		return confirmedStructureMember{}, false, scope.Decision
	}
	return confirmedStructureMember{
		Member:       contractsv1.ContextFabricStructureNeedSubjectAnchor,
		AppliedKind:  scope.AnchorSubjectKind,
		AppliedValue: scope.AnchorID,
		Basis:        ConfirmedNeedBasisEngineCommitted,
	}, true, scope.Decision
}

// confirmedNeedsForCaptureWithCommittedAnchor is Investigate's own ONE
// computation site for folding an engine-committed anchor into the outgoing
// per-need ledger: base is the ledger already computed before resolution ran
// (mergeConfirmedNeedsLedger's own call, unaffected for every other member
// and for every turn this function declines), member is what
// engineCommittedAnchorForCapture found (ok=false is a no-op, returning base
// unchanged), and alreadyStated is whether THIS turn's own receipt or
// explicit field already confirmed subject_anchor -- a caller speaking this
// turn always wins over a subject the engine merely resolved to, exactly as
// statedNeedMembers already rules for every other axis, so this never
// overrides a fresh confirmation.
func confirmedNeedsForCaptureWithCommittedAnchor(remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember, request InvestigationRequest, base []ConfirmedNeedEntry, member confirmedStructureMember, ok bool) []ConfirmedNeedEntry {
	if !ok {
		return base
	}
	if statedNeedMembers(request, confirmedThisTurn)[contractsv1.ContextFabricStructureNeedSubjectAnchor] {
		return base
	}
	augmented := append(append([]confirmedStructureMember{}, confirmedThisTurn...), member)
	return mergeConfirmedNeedsLedger(remembered, augmented, request)
}

// ConfirmedAnchorAgreement is the closed, disclosure-safe verdict comparing a
// turn's own APPLIED subject_anchor ledger entry -- receipt-redeemed or
// engine-committed alike, this axis draws no distinction between the two --
// against what THAT SAME TURN's own resolution actually committed. A carried
// value is always advisory input INTO resolution (it narrows the pool
// resolution is allowed to search, never a substitute for resolution
// proving it); this is the one place that checks whether the advice and the
// proof still agree.
type ConfirmedAnchorAgreement string

const (
	// ConfirmedAnchorAgreementNotApplicable is the zero value: no
	// subject_anchor ledger entry applied this turn at all, so there is
	// nothing to compare. Named explicitly, matching ConfirmedNeedBasisConfirmed's
	// own reasoning, so a log line never reads an unset axis as a key nobody
	// wrote.
	ConfirmedAnchorAgreementNotApplicable ConfirmedAnchorAgreement = "not_applicable"
	// ConfirmedAnchorAgreementAbsent: an entry applied, but this turn's own
	// resolution committed nothing of the entry's own kind -- the carry
	// played no role in what this turn actually served, so there is nothing
	// to veto.
	ConfirmedAnchorAgreementAbsent ConfirmedAnchorAgreement = "absent"
	// ConfirmedAnchorAgreementAgree: an entry applied, and this turn's own
	// resolution independently committed the SAME (kind, canonical_id) --
	// the carry and the proof point at one subject.
	ConfirmedAnchorAgreementAgree ConfirmedAnchorAgreement = "agree"
	// ConfirmedAnchorAgreementDisagree: an entry applied, and this turn's own
	// resolution committed a DIFFERENT canonical id of the SAME kind. The
	// entry is VETOED: never disclosed as applied, never carried forward to
	// a later turn -- a served document must never claim a subject its own
	// resolution disowned in the same breath.
	ConfirmedAnchorAgreementDisagree ConfirmedAnchorAgreement = "disagree"
)

// carriedAnchorAgreementFor decides, once per turn immediately after
// resolution runs, whether this turn's own applied subject_anchor ledger
// entry (appliedNeeds, chaos5639_confirmed_need.go) still agrees with what
// resolution independently committed. Returns the agreement, the entry
// itself (zero value when not_applicable), and vetoed=true exactly on
// disagree -- the one case a caller must act on.
func carriedAnchorAgreementFor(appliedNeeds map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, resolution SubjectResolution) (ConfirmedAnchorAgreement, confirmedStructureMember, bool) {
	entry, ok := appliedNeeds[contractsv1.ContextFabricStructureNeedSubjectAnchor]
	if !ok {
		return ConfirmedAnchorAgreementNotApplicable, confirmedStructureMember{}, false
	}
	sawSameKind := false
	for _, subject := range resolution.Committed {
		if subject.Kind != entry.AppliedKind {
			continue
		}
		if subject.CanonicalID == entry.AppliedValue {
			return ConfirmedAnchorAgreementAgree, entry, false
		}
		sawSameKind = true
	}
	if sawSameKind {
		return ConfirmedAnchorAgreementDisagree, entry, true
	}
	return ConfirmedAnchorAgreementAbsent, entry, false
}

// carriedStructureEntriesForDecisive replaces entries' subject_anchor member
// with a vetoed_conflict disposition when vetoed is true, leaving every
// other member (and entries itself, when not vetoed) unchanged. Applied only
// at the call sites that can see a genuine post-resolution disagreement --
// the decisive save and the supersession veto that can follow it -- so the
// served document's own confirmed-structure echo can never claim "applied"
// for a subject this turn's own resolution disowned.
func carriedStructureEntriesForDecisive(entries []*contractsv1.ContextFabricConfirmedStructureEntry, vetoedEntry confirmedStructureMember, vetoed bool) []*contractsv1.ContextFabricConfirmedStructureEntry {
	if !vetoed {
		return entries
	}
	out := make([]*contractsv1.ContextFabricConfirmedStructureEntry, len(entries))
	for i, entry := range entries {
		if entry != nil && entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor && entry.AppliedValue == vetoedEntry.AppliedValue {
			replaced := *entry
			replaced.Disposition = contractsv1.ContextFabricStructureDispositionVetoedConflict
			out[i] = &replaced
			continue
		}
		out[i] = entry
	}
	return out
}

// confirmedNeedsForCaptureWithoutVetoedAnchor drops the subject_anchor member
// from entries when vetoed is true -- a disagreement proven THIS turn must
// not reach a LATER turn naming this one as parent either, exactly as final
// for inheritance as a reverify-time drop already is.
func confirmedNeedsForCaptureWithoutVetoedAnchor(entries []ConfirmedNeedEntry, vetoed bool) []ConfirmedNeedEntry {
	if !vetoed {
		return entries
	}
	out := make([]ConfirmedNeedEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			continue
		}
		out = append(out, entry)
	}
	return out
}
