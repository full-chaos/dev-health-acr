package contextfabric

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

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
	// played no role in what this turn actually served. Dropped exactly as a
	// disagreement is: a later turn naming this one as parent must inherit
	// only what THIS turn's own resolution actually stood behind, and a
	// resolution that never touched the entry's kind at all never stood
	// behind it.
	ConfirmedAnchorAgreementAbsent ConfirmedAnchorAgreement = "absent"
	// ConfirmedAnchorAgreementAgree: an entry applied, and this turn's own
	// resolution independently committed the SAME (kind, canonical_id) as
	// the SOLE identity-proven commit of that kind -- the carry and the
	// proof point at one subject, and nothing else this turn committed on a
	// proven basis contests it.
	ConfirmedAnchorAgreementAgree ConfirmedAnchorAgreement = "agree"
	// ConfirmedAnchorAgreementDisagree: this turn's own resolution committed
	// a DIFFERENT canonical id of the SAME kind ON AN IDENTITY-PROVEN BASIS
	// (CommitBasis.IdentityProven -- a caller canonical id or an
	// authoritative keyed identity, never a score comparison) -- whether or
	// not resolution ALSO re-committed the carried id itself alongside it: a
	// turn that committed two identity-proven subjects of the same kind
	// proved an ambiguity, never an agreement, so the carry can never read
	// as confirmed beside a distinct proven commit. The entry is VETOED:
	// never disclosed as applied, never carried forward to a later turn -- a
	// served document must never claim a subject its own resolution
	// disowned in the same breath. A same-kind, different-id STATISTICAL
	// commit is never a conflict: anchorBound itself refuses to bind such a
	// commit (count_population_scope.go), so treating it as grounds to drop
	// a genuinely proven carry would let the weaker signal evict the
	// stronger one.
	ConfirmedAnchorAgreementDisagree ConfirmedAnchorAgreement = "disagree"
)

// carriedAnchorAgreementFor decides, once per turn immediately after
// resolution runs, whether this turn's own applied subject_anchor ledger
// entry (appliedNeeds, chaos5639_confirmed_need.go) still agrees with what
// resolution independently committed. bases is the SAME CommitBasisSet
// ResolveSubjects returned this turn -- only a same-kind, different-id
// commit that basis itself reports IdentityProven can ever disagree; a
// statistical or unrecorded-basis commit of the same kind is invisible to
// this check, exactly as it is to anchorBound. The whole committed set is
// read before any verdict is chosen -- a distinct proven commit anywhere in
// it outranks a same-id match found earlier in the slice, so agreement can
// never turn on iteration order. Returns the agreement, the entry itself
// (zero value when not_applicable), and drop=true on disagree OR absent --
// the two cases a caller must remove from the outgoing ledger, whatever
// wire disposition (anchorLedgerDisposition) each one discloses.
func carriedAnchorAgreementFor(appliedNeeds map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, resolution SubjectResolution, bases CommitBasisSet) (ConfirmedAnchorAgreement, confirmedStructureMember, bool) {
	entry, ok := appliedNeeds[contractsv1.ContextFabricStructureNeedSubjectAnchor]
	if !ok {
		return ConfirmedAnchorAgreementNotApplicable, confirmedStructureMember{}, false
	}
	matchedSelf := false
	distinctProven := false
	for _, subject := range resolution.Committed {
		if subject.Kind != entry.AppliedKind {
			continue
		}
		if subject.CanonicalID == entry.AppliedValue {
			matchedSelf = true
			continue
		}
		if bases.For(subject).IdentityProven() {
			distinctProven = true
		}
	}
	switch {
	case distinctProven:
		return ConfirmedAnchorAgreementDisagree, entry, true
	case matchedSelf:
		return ConfirmedAnchorAgreementAgree, entry, false
	default:
		return ConfirmedAnchorAgreementAbsent, entry, true
	}
}

// anchorLedgerDisposition is the ONE mapping from what happened to this
// turn's own applied subject_anchor entry to the wire disposition its
// carried-structure echo discloses and this axis's own ledger line reports.
// superseded and a post-resolution agreement verdict can never both fire on
// the same entry: a contest superseding it removes it from the ledger
// before resolution ever runs, so the agreement check that runs after
// always reads not_applicable for an entry the contest already took. Empty
// when agreement is not_applicable and superseded is false: no subject_anchor
// entry applied this turn at all, so there is nothing to disclose a
// disposition for -- mirrors AppliedAnchorKind/AppliedAnchorBasis's own
// empty-when-inapplicable convention (ConfirmedNeedLedgerEvent's own doc
// comment).
func anchorLedgerDisposition(agreement ConfirmedAnchorAgreement, superseded bool) contractsv1.ContextFabricStructureDisposition {
	switch {
	case superseded:
		return contractsv1.ContextFabricStructureDispositionSupersededByCaller
	case agreement == ConfirmedAnchorAgreementDisagree:
		return contractsv1.ContextFabricStructureDispositionVetoedConflict
	case agreement == ConfirmedAnchorAgreementAbsent:
		return contractsv1.ContextFabricStructureDispositionVetoedUnresolved
	case agreement == ConfirmedAnchorAgreementAgree:
		return contractsv1.ContextFabricStructureDispositionApplied
	default:
		return ""
	}
}

// callerSuppliedHintOfKind reports whether hints -- graphRequest's own
// RequestedScope.SubjectHints as built before the engine's carry would
// append to it, whatever reached it from the caller's own request or a
// prior receipt this turn redeemed -- already names kind. Any one of them
// can independently make resolve.go's caller-hint exact-commit channel
// short-circuit-eligible (graphrank's AnyCallerSourced), and that channel
// commits EVERY hint in the set once it fires, not only the one that
// qualified it -- so a same-kind hint already present is the same identity
// question this turn's own carry would also try to answer, and injecting
// the carry beside it would let both commit CommitBasisCallerCanonicalID on
// distinct identities.
func callerSuppliedHintOfKind(hints []contractsv1.ContextFabricSubjectHint, kind contractsv1.ContextFabricSubjectKind) bool {
	for _, hint := range hints {
		if hint.Kind == kind {
			return true
		}
	}
	return false
}

// engineCommittedAnchorHint builds the SubjectHint that lets an
// identity-proven carried anchor reach resolution's own caller-hint exact
// commit exit (graphrank/resolve.go's RequestedScope.SubjectHints loop) --
// the SAME channel a caller-confirmed pick reaches, rather than a second,
// parallel notion of "this is the anchor." Only ever built for the
// engine-committed basis: the identity proof anchorBound already required
// before this entry could be captured (chaos5788_committed_anchor_carry.go's
// own capture gate) is exactly the proof this hint asks resolution to
// re-verify and commit on. ok=false when nothing applies, or the applied
// entry is a receipt-redeemed one -- a caller-confirmed pick already reaches
// this channel through its own receipt-redemption path and must not be
// double-injected here.
func engineCommittedAnchorHint(appliedNeeds map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) (contractsv1.ContextFabricSubjectHint, bool) {
	entry, ok := appliedNeeds[contractsv1.ContextFabricStructureNeedSubjectAnchor]
	if !ok || entry.Basis != ConfirmedNeedBasisEngineCommitted || entry.AppliedValue == "" {
		return contractsv1.ContextFabricSubjectHint{}, false
	}
	return contractsv1.ContextFabricSubjectHint{
		Kind: entry.AppliedKind, ID: entry.AppliedValue, Label: entry.AppliedValue,
		Source: string(hintsource.EngineCommittedAnchorCarry),
	}, true
}

// carriedStructureEntriesForDecisive replaces entries' subject_anchor member
// with disposition when dropped is true, leaving every other member (and
// entries itself, when not dropped) unchanged. Applied only at the call
// sites that can see this turn's own final word on the carry -- the
// decisive save and the supersession veto that can follow it -- so the
// served document's own confirmed-structure echo can never claim "applied"
// for a subject this turn's own request or resolution took away.
func carriedStructureEntriesForDecisive(entries []*contractsv1.ContextFabricConfirmedStructureEntry, droppedEntry confirmedStructureMember, dropped bool, disposition contractsv1.ContextFabricStructureDisposition) []*contractsv1.ContextFabricConfirmedStructureEntry {
	if !dropped {
		return entries
	}
	out := make([]*contractsv1.ContextFabricConfirmedStructureEntry, len(entries))
	for i, entry := range entries {
		if entry != nil && entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor && entry.AppliedValue == droppedEntry.AppliedValue {
			replaced := *entry
			replaced.Disposition = disposition
			out[i] = &replaced
			continue
		}
		out[i] = entry
	}
	return out
}

// confirmedNeedsForCaptureWithoutVetoedAnchor drops the subject_anchor member
// from entries when dropped is true -- a contest, disagreement or absence
// this turn -- must not reach a LATER turn naming this one as parent either,
// exactly as final for inheritance as a reverify-time drop already is.
func confirmedNeedsForCaptureWithoutVetoedAnchor(entries []ConfirmedNeedEntry, dropped bool) []ConfirmedNeedEntry {
	if !dropped {
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
