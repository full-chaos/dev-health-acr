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
func engineCommittedAnchorForCapture(frame *QuestionFrame, sampleAnchorKind SubjectKind, resolution SubjectResolution, bases CommitBasisSet) (confirmedStructureMember, bool) {
	// CohortMemberSourceNotApplicable: this decision runs right after
	// ResolveSubjects, before DiscoverContext ever produces a GraphContext --
	// there is no member-source disclosure question in scope here, only
	// whether resolution bound the frame's own anchor.
	scope := DecideCountPopulationScope(frame, sampleAnchorKind, resolution, bases, CohortMemberSourceNotApplicable)
	if scope.Decision != CountPopulationScopeAnchorCommitted {
		return confirmedStructureMember{}, false
	}
	return confirmedStructureMember{
		Member:       contractsv1.ContextFabricStructureNeedSubjectAnchor,
		AppliedKind:  scope.AnchorSubjectKind,
		AppliedValue: scope.AnchorID,
		Basis:        ConfirmedNeedBasisEngineCommitted,
	}, true
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
