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
type anchorPoolKindScope struct {
	Kind   contextfabric.SubjectKind
	Source string
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
func decideAnchorPoolKindScope(frame *contextfabric.QuestionFrame, receiptAnchorKind contextfabric.SubjectKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection) anchorPoolKindScope {
	if receiptAnchorKind != "" {
		return anchorPoolKindScope{Kind: receiptAnchorKind, Source: anchorPoolKindScopeReceipt}
	}
	if confirmedAnchor == nil {
		return anchorPoolKindScope{Source: anchorPoolKindScopeNone}
	}
	if kind := contextfabric.ScopeAnchorRetrievalKind(frame, confirmedAnchor.Kind); kind != "" {
		return anchorPoolKindScope{Kind: kind, Source: anchorPoolKindScopeConfirmedAnchor}
	}
	return anchorPoolKindScope{Source: anchorPoolKindScopeNone}
}

// admits reports whether this scope lets a candidate of kind through the
// confirmed-kind filter.
func (s anchorPoolKindScope) admits(kind contextfabric.SubjectKind) bool {
	return s.Kind != "" && kind == s.Kind
}

// observable renders the pair the decision_summary carries. It returns
// explicit tokens on every pass -- never "" -- so an absent scope and a build
// that stopped deciding one can never read alike.
func (s anchorPoolKindScope) observable() (scope string, source string) {
	if s.Kind == "" {
		return anchorPoolKindScopeNone, anchorPoolKindScopeNone
	}
	if s.Source == "" {
		return string(s.Kind), anchorPoolKindScopeNone
	}
	return string(s.Kind), s.Source
}

// splitAnchorFromMembers separates the SCOPE ANCHOR's candidates from the
// member candidates so the two are decided in their own contests.
//
// WHY THEY CANNOT SHARE ONE. The design carries three roles, not one
// candidate set -- SubjectPlan is "group axis, member axis, scope anchor" --
// and discovery flows FROM the scope TO the members. Invariant I11 then
// requires the graph to COMMIT the anchor, and defines "resolved" as exactly
// that. A single shared contest makes I11 unsatisfiable on the frames it
// governs: whenever a member outranks the anchor, nothing commits the anchor
// and there is no resolved anchor to check.
//
// Measured before the split, on the reviewer's own probe: a project at
// confidence .8 committed on the lone-candidate floor, and adding the team
// anchor at .4 turned it into a top-two ambiguity whose clarification asked
// the caller to choose between a member and the scope containing it -- a
// question I11 guarantees has no answer, since the two kinds always differ.
//
// A zero-value scope splits nothing and every non-scope-anchored request
// keeps the single pool it always had.
func splitAnchorFromMembers(candidatesBySubject map[string]contextfabric.SubjectCandidate, anchorScope anchorPoolKindScope) (members, anchors map[string]contextfabric.SubjectCandidate) {
	if anchorScope.Kind == "" {
		return candidatesBySubject, nil
	}
	members = make(map[string]contextfabric.SubjectCandidate, len(candidatesBySubject))
	for key, candidate := range candidatesBySubject {
		if anchorScope.admits(candidate.Subject.Kind) {
			if anchors == nil {
				anchors = make(map[string]contextfabric.SubjectCandidate, 1)
			}
			anchors[key] = candidate
			continue
		}
		members[key] = candidate
	}
	return members, anchors
}

// mergeAnchorResolution folds the anchor's own resolution back into the
// member resolution the caller will return.
//
// THE PROMPT IS NOT MERGED, and the honest reason is narrower than it looks.
// An ambiguous anchor is a question about the SCOPE ("which CHAOS did you
// mean?"), not about the members, so carrying it into the member
// clarification would re-confuse the two roles. But what actually PREVENTS
// that today is that the anchor contest runs with clarification disabled, so
// it never produces a prompt to carry: a mutation adding the merge here
// SURVIVES, because there is nothing for it to move.
//
// That is a REPORTED limit rather than a hidden one, and it is deliberately
// given no battery arm -- an arm known to survive would manufacture a finding
// already adjudicated. If the anchor contest is ever allowed to clarify, this
// guard becomes load-bearing and needs a pin the same day.
//
// What IS pinned: the anchor's claimants all reach the result with their
// states intact, so an ambiguous scope is visible to caller and operator
// rather than silently decided.
func mergeAnchorResolution(members contextfabric.SubjectResolution, anchor contextfabric.SubjectResolution) contextfabric.SubjectResolution {
	members.Candidates = append(members.Candidates, anchor.Candidates...)
	members.Committed = append(members.Committed, anchor.Committed...)
	members.RetrievalDegraded = members.RetrievalDegraded || anchor.RetrievalDegraded
	return members
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

// filterKindTokens renders exactly what the confirmed-kind filter admits:
// the confirmed member kind, plus the anchor kind when one is in scope. A
// resolution with no confirmed kind filters nothing and reports an empty set,
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

// anchorBudgetFor is how many of the shared candidate slots the SCOPE contest
// takes. The budget is shared, never doubled: phase 4's contract is that a
// reserve displaces rather than grows it, and running two contests must not
// return one more subject than the caller asked for.
//
// It is TWO when two or more claimants exist, and the second slot is not
// padding: with one slot the anchor pool is truncated to its top candidate
// before the gate sees it, so a genuinely ambiguous scope silently reads as a
// decided one. An anchor that cannot express ambiguity cannot refuse to guess
// -- which is the property this whole seam exists to protect. One claimant
// needs one slot; none needs none.
func anchorBudgetFor(anchorPool map[string]contextfabric.SubjectCandidate, max int) int {
	switch {
	case len(anchorPool) == 0 || max <= 1:
		return 0
	case len(anchorPool) == 1:
		return 1
	case max <= 3:
		return 1
	default:
		return 2
	}
}
