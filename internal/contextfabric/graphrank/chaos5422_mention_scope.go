package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// THE KIND THIS RESOLUTION MAY NEVER OFFER OR COMMIT AS ITS SUBJECT, and
// where that statement came from.
//
// CHAOS-5422. A children_of_scope question declares the kind of the MEMBERS
// it wants and the TERMS of the anchor those members hang off. The subject
// the resolution must commit is the ANCHOR, and phase-B invariant I11 states
// the asymmetry: "the RESOLVED anchor's kind != MemberKind ... the anchor's
// kind is unknown until the term resolves." I11 was declared
// (frame_invariants.go) and enforced nowhere, and on the rig that gap cost a
// substituted subject:
//
//	t1  the engine offers the expected_kind axis; the caller redeems the
//	    MEMBER kind, truthfully -- that is the kind it asked about
//	t2  the confirmed member kind narrows the SUBJECT pool to itself
//	    (filterCandidatesByConfirmedKind), the anchor the terms named is
//	    dropped, and the one survivor -- a weak LEXICAL hit of the member's
//	    kind -- is offered with a receipt id
//	t3  the caller answers with that receipt and pre_committed_exact_hint
//	    records the member as the answer's subject on caller_canonical_id
//
// So every candidate that survives a member-kind-only filter on such a frame
// is an I11 violation by construction, and CHAOS-5385's exclusion could not
// see it: that seam withholds VECTOR-ONLY candidates and this one matched
// lexically.
//
// THE SIGNAL IS THE GROUPING/SCOPE AXIS, NOT THE QUESTION'S TEXT. The
// decision below reads SubjectExpression.Kind and the declared MemberKind --
// two closed-vocabulary fields the model filled in deliberately and the
// server validated -- and nothing else. No term, no label and no prose
// reaches it, which is what keeps it from becoming another keyword table
// deciding structure.
//
// Source is carried, not just the kind, for the same reason
// anchorPoolKindScope carries its own: a build that stopped deciding and a
// question that has no scope axis render differently, so an operator can tell
// them apart on one line.
type subjectOfferScope struct {
	// MemberKind is the declared member kind, and the ONLY kind this scope
	// ever withholds. Empty means this resolution withholds nothing.
	MemberKind contextfabric.SubjectKind
	// Source names what decided it, from the closed vocabulary below.
	Source string
}

const (
	// subjectOfferScopeNone: this resolution withholds no kind, which is
	// every question that is not scope-anchored -- the overwhelming common
	// case, and byte-identical to the pre-ticket behaviour.
	subjectOfferScopeNone = "none"
	// subjectOfferScopeFrameMemberKind: the frame's own children_of_scope
	// member kind. The only source today; it is named rather than implied
	// so a second source added later cannot arrive unlabelled.
	subjectOfferScopeFrameMemberKind = "frame_member_kind"
)

// offerPoolAnchorKindWithheldDisposition is the per-candidate offer_pool
// disposition token for this withholding, spelled once here so the emitter
// and every reader name the same string rather than two equal literals.
const offerPoolAnchorKindWithheldDisposition = "anchor_kind_withheld"

// decideSubjectOfferScope is the whole decision, TOTAL and PURE over a
// possibly-nil frame.
//
// ONLY children_of_scope WITHHOLDS, and the four exclusions are load-bearing
// rather than an oversight -- each one names a variant whose declared member
// kind IS a kind it may legitimately commit:
//
//   - named_subject: a named subject is not a member of anything, it IS the
//     subject, and its declared kind lives on ExpectedKind. Withholding it
//     would empty the offer for the most common question the product answers.
//   - discovered_kind: its members ARE the subjects it commits.
//   - grouped_members: both axes are cohort axes; nothing here resolves an
//     anchor, so there is no I11 asymmetry to enforce and no basis for
//     refusing either kind.
//   - explicit_set: its operands' subjects come from resolution itself, so a
//     kind refused here would refuse the operands the question named.
//
// A scope-anchored frame that declares NO member kind decides nothing either:
// with no declared member kind there is no kind I11 excludes, and guessing one
// would be the keyword table this seam exists to avoid.
//
// AND IT IS GATED ON THE CONFIRMED MEMBER KIND, which is the narrowing an
// existing CHAOS-5393 pin forced and is the more honest rule besides. The harm
// is CREATED by filterCandidatesByConfirmedKind: narrowing the SUBJECT pool to
// the confirmed member kind is what drops the anchor the terms named and
// leaves a pool in which every survivor is an I11 violation by construction.
// With no confirmed kind that filter is a no-op, the anchor and the members
// are in the pool together, and the contest between them is a real one --
// TestWithNoConfirmedKindTheAnchorChangesNothing pins that a member committing
// there is existing, accepted behaviour, and refusing it would take a served
// answer away to fix a defect that turn does not have.
//
// A confirmed kind that is NOT the member kind decides nothing either: the
// filter then narrowed to THAT kind, so its survivors are not member-kind
// candidates and there is nothing here to refuse.
func decideSubjectOfferScope(frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind, anchorScope anchorPoolKindScope) subjectOfferScope {
	if frame == nil {
		return subjectOfferScope{Source: subjectOfferScopeNone}
	}
	if frame.SubjectExpression.Kind != contextfabric.SubjectExpressionChildrenOfScope {
		return subjectOfferScope{Source: subjectOfferScopeNone}
	}
	kind, ok := frame.SubjectExpression.MemberKind()
	if !ok || kind == "" {
		return subjectOfferScope{Source: subjectOfferScopeNone}
	}
	if confirmedKind == nil || confirmedKind.Kind != kind {
		return subjectOfferScope{Source: subjectOfferScopeNone}
	}
	// DEFENSIVE, and stated rather than assumed. ScopeAnchorRetrievalKind
	// already refuses an anchor kind equal to the member kind, so this cannot
	// fire today -- but the anchor scope and this one would then be making
	// opposite claims about the same kind, admitting it to the pool while
	// refusing it the offer, and a future widening of either must not be able
	// to reach that state silently.
	if anchorScope.admits(kind) {
		return subjectOfferScope{Source: subjectOfferScopeNone}
	}
	return subjectOfferScope{MemberKind: kind, Source: subjectOfferScopeFrameMemberKind}
}

// withholds reports whether a candidate of kind may neither be offered as
// this resolution's subject nor committed as one.
//
// The empty kind is REFUSED EXPLICITLY rather than by comparison. A candidate
// whose kind failed to resolve carries the zero value, and a zero-value scope
// carries it too -- so a bare `kind == s.MemberKind` would withhold that whole
// class from every offer in the product through a comparison nobody wrote
// deliberately. The same "an absent value is not a match" discipline
// anchorPoolKindScope.admits already applies to its own zero.
func (s subjectOfferScope) withholds(kind contextfabric.SubjectKind) bool {
	return s.MemberKind != "" && kind == s.MemberKind
}

// observable renders the pair the decision line carries. Both halves are
// explicit tokens on every pass -- never "" -- so a resolution that withheld
// nothing and a build that stopped deciding can never read alike.
func (s subjectOfferScope) observable() (kind string, source string) {
	if s.MemberKind == "" {
		return subjectOfferScopeNone, subjectOfferScopeNone
	}
	if s.Source == "" {
		return string(s.MemberKind), subjectOfferScopeNone
	}
	return string(s.MemberKind), s.Source
}

// subjectOfferScopeFor is the ONE expression both call sites evaluate, and it
// exists so there is only one.
//
// The decision needs the anchor scope, which resolveSubjects computes for its
// own consumers and the decision-summary fold cannot see. Spelling the pair out
// at each site would have left two copies of the same derivation one edit apart
// -- and the two sites are the resolution that acts on the scope and the line
// that reports it, so a divergence there would make the observable disagree
// with the behaviour it claims to describe. Both helpers are pure, so a second
// evaluation costs nothing and cannot differ.
func subjectOfferScopeFor(frame *contextfabric.QuestionFrame, scopeAnchorKind contextfabric.SubjectKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection, confirmedKind *contextfabric.ConfirmedExpectedKind) subjectOfferScope {
	return decideSubjectOfferScope(frame, confirmedKind, decideAnchorPoolKindScope(frame, scopeAnchorKind, confirmedAnchor, confirmedKind))
}
