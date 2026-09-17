package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// THE KIND THE SCOPE ANCHOR IS ALLOWED TO RESOLVE UNDER, and where that
// statement came from.
//
// A children_of_scope question declares the kind of the MEMBERS it wants and
// the TERMS of the anchor those members hang off. The subject this resolution
// must commit is the ANCHOR, and the design's phase-B table says its kind
// cannot be the member's: "the RESOLVED anchor's kind != MemberKind ... the
// anchor's kind is unknown until the term resolves" (invariant I11).
// Narrowing the pool to the confirmed MEMBER kind therefore cannot merely
// lose the anchor -- it makes resolving one impossible, because every
// candidate that survives such a filter is an I11 violation by construction.
//
// Source is carried, not just the kind, because the two sources fail
// independently and an operator who cannot tell them apart cannot tell a
// model that stopped emitting scope_anchor_kind from a caller that stopped
// redeeming anchor receipts. Both render the same admitted kind.
//
// NoneReason (CHAOS-5825) is populated whenever Kind is empty and names
// WHICH of this function's two inputs was missing: a trace showing
// Source=none alone cannot tell "the model never stated an anchor kind and
// no prior turn confirmed one either" apart from "a confirmed anchor
// existed but no confirmed member kind gated it in" -- two different
// upstream defects that both render identically without this field.
type anchorPoolKindScope struct {
	Kind       contextfabric.SubjectKind
	Source     string
	NoneReason string
}

const (
	// anchorPoolKindScopeNone: this resolution admits no anchor kind, which
	// is every request that is not scope-anchored -- the overwhelming
	// common case, and byte-identical to the pre-ticket pool.
	anchorPoolKindScopeNone = "none"
	// anchorPoolKindScopeReceipt: the classification receipt carried a
	// scope_anchor_kind and it survived ScopeAnchorRetrievalKind's checks.
	anchorPoolKindScopeReceipt = "receipt"
	// anchorPoolKindScopeConfirmedAnchor: the receipt carried none, and the
	// kind came from the caller's own redeemed anchor selection instead.
	anchorPoolKindScopeConfirmedAnchor = "confirmed_anchor"
)

const (
	// anchorPoolKindScopeNoneReasonNotApplicable: Kind is not empty -- a
	// scope was admitted, so there is no "none" to explain.
	anchorPoolKindScopeNoneReasonNotApplicable = "not_applicable"
	// anchorPoolKindScopeNoneReasonNoReceiptNoConfirmedAnchor: the call's own
	// receipt-sourced kind was empty (the model stated no usable
	// scope_anchor_kind for this turn, or ScopeAnchorRetrievalKind's own
	// checks gated it out upstream) AND no confirmed anchor was carried from
	// a prior turn either -- neither input this function reads had anything
	// to admit.
	anchorPoolKindScopeNoneReasonNoReceiptNoConfirmedAnchor = "no_receipt_kind_no_confirmed_anchor"
	// anchorPoolKindScopeNoneReasonConfirmedAnchorNoConfirmedKind: a
	// confirmed anchor WAS carried, but no confirmed member kind gated it in
	// (the fallback's own deliberate gate -- see decideAnchorPoolKindScope's
	// doc comment on why it is gated this way).
	anchorPoolKindScopeNoneReasonConfirmedAnchorNoConfirmedKind = "confirmed_anchor_no_confirmed_kind"
	// anchorPoolKindScopeNoneReasonConfirmedAnchorKindRejected: a confirmed
	// anchor and a confirmed kind both existed, but ScopeAnchorRetrievalKind
	// refused the confirmed anchor's own kind (e.g. it equals the frame's
	// member kind, or the frame is not children_of_scope).
	anchorPoolKindScopeNoneReasonConfirmedAnchorKindRejected = "confirmed_anchor_kind_rejected"
	// anchorPoolKindScopeNoneReasonNotEvaluated: decideAnchorPoolKindScope
	// never ran for this request at all -- the call it would have decided
	// for exited earlier (an error, a cancelled context, a caller-hint
	// short circuit) -- so there is no receipt/confirmed-anchor reading to
	// report on either side. Distinct from
	// NoReceiptNoConfirmedAnchor, which means the decision DID run and
	// found both inputs genuinely empty: this call's own inputs are
	// unknown here, not proven empty, and a caller reporting them as
	// "empty" would be inventing an answer the exit never computed.
	anchorPoolKindScopeNoneReasonNotEvaluated = "not_evaluated"
)

// decideAnchorPoolKindScope prefers the receipt and falls back to the
// confirmed anchor, and the ORDER is the point rather than a detail. The
// receipt's kind is what the model said the question was about; the confirmed
// anchor's kind is what a caller redeemed one offer with. When both exist
// they agree, and when they disagree the model's reading of the whole
// question is the wider statement -- a caller can only ever confirm an option
// this engine already offered it.
//
// The fallback is re-gated through ScopeAnchorRetrievalKind rather than
// trusted, because confirmedAnchor.Kind has been through none of its checks:
// it must still be a children_of_scope frame with anchor terms, an
// in-vocabulary kind, and NOT the member kind. Skipping that last check is
// how this fix would silently admit an I11-violating anchor while claiming to
// enforce I11.
func decideAnchorPoolKindScope(frame *contextfabric.QuestionFrame, receiptAnchorKind contextfabric.SubjectKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection, confirmedKind *contextfabric.ConfirmedExpectedKind) anchorPoolKindScope {
	// THE RECEIPT PATH IS UNCONDITIONAL, because it is not this ticket's to
	// gate: a receipt-declared scope-anchor kind has reached kind-hinted
	// retrieval since the ticket that introduced it, with or without a
	// confirmed member kind, and narrowing that here would silently undo
	// shipped behaviour rather than fix anything.
	if receiptAnchorKind != "" {
		return anchorPoolKindScope{Kind: receiptAnchorKind, Source: anchorPoolKindScopeReceipt}
	}
	if confirmedAnchor == nil {
		return anchorPoolKindScope{Source: anchorPoolKindScopeNone, NoneReason: anchorPoolKindScopeNoneReasonNoReceiptNoConfirmedAnchor}
	}
	// THE FALLBACK IS GATED ON A CONFIRMED MEMBER KIND, and this is the one
	// place this change adds a kind the engine would not otherwise have gone
	// looking for. This ticket's defect is the confirmed-kind FILTER
	// stripping the anchor; with no confirmed kind that filter is a no-op,
	// so there is nothing to rescue and pulling the anchor in anyway only
	// ADDS a candidate that was never removed.
	//
	// Measured, on an adversarial round's probe: doing it unconditionally
	// pulled a team anchor into a pool that had none, and a project which
	// committed on the lone-candidate floor became a top-two ambiguity
	// asking the caller to choose between a member and the scope containing
	// it -- a question invariant I11 guarantees has no answer. Gated here,
	// every request without a confirmed kind keeps the pool, the gates and
	// the prompt it had.
	//
	// The deeper fix -- the scope anchor deciding in its OWN contest rather
	// than beside the members -- is a separate change with its own design
	// cover and its own rig-visibility contract to renegotiate.
	if confirmedKind == nil {
		return anchorPoolKindScope{Source: anchorPoolKindScopeNone, NoneReason: anchorPoolKindScopeNoneReasonConfirmedAnchorNoConfirmedKind}
	}
	if kind := contextfabric.ScopeAnchorRetrievalKind(frame, confirmedAnchor.Kind); kind != "" {
		return anchorPoolKindScope{Kind: kind, Source: anchorPoolKindScopeConfirmedAnchor}
	}
	return anchorPoolKindScope{Source: anchorPoolKindScopeNone, NoneReason: anchorPoolKindScopeNoneReasonConfirmedAnchorKindRejected}
}

// admits reports whether this scope lets a candidate of kind through the
// confirmed-kind filter.
func (s anchorPoolKindScope) admits(kind contextfabric.SubjectKind) bool {
	return s.Kind != "" && kind == s.Kind
}

// observable renders the pair the decision_summary carries. It returns
// explicit tokens on every pass -- never "" -- so an absent scope and a build
// that stopped deciding one can never read alike.
func (s anchorPoolKindScope) observable() (scope string, source string, noneReason string) {
	if s.Kind == "" {
		reason := s.NoneReason
		if reason == "" {
			reason = anchorPoolKindScopeNoneReasonNoReceiptNoConfirmedAnchor
		}
		return anchorPoolKindScopeNone, anchorPoolKindScopeNone, reason
	}
	if s.Source == "" {
		return string(s.Kind), anchorPoolKindScopeNone, anchorPoolKindScopeNoneReasonNotApplicable
	}
	return string(s.Kind), s.Source, anchorPoolKindScopeNoneReasonNotApplicable
}

// kindTokens renders a kind list for the observable. Always a slice, never
// nil, so an empty reserved set and an absent field cannot read alike.
func kindTokens(kinds []contextfabric.SubjectKind) []string {
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if kind != "" {
			out = append(out, string(kind))
		}
	}
	return out
}

// filterKindTokens renders exactly what the confirmed-kind filter admits: the
// confirmed member kind, plus the anchor kind when one is in scope. A
// resolution with no confirmed kind filters nothing and reports an EMPTY set,
// which is a different statement from "the filter ran and admitted nothing".
func filterKindTokens(confirmedKind *contextfabric.ConfirmedExpectedKind, anchorScope anchorPoolKindScope) []string {
	if confirmedKind == nil {
		return []string{}
	}
	out := []string{string(confirmedKind.Kind)}
	if anchorScope.Kind != "" {
		out = append(out, string(anchorScope.Kind))
	}
	return out
}
