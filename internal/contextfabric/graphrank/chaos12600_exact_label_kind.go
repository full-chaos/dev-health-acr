package graphrank

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12600 (lead ruling 2026-11-02, FINAL): a comparison whose sides each
// name exactly one subject by its full label, all of one kind, with no subject
// of another kind carrying that label, has a RESOLVED kind, not an inferred
// one. The pair is then resolved as if the caller had stated the kind, and the
// answer discloses it. Every other case keeps the kind question CHAOS-12548
// asks: a label that matches two subjects or two kinds, a side with no exact
// match, mixed kinds across the sides, or a census that cannot show it saw
// every subject.

// ComparisonKindBasis names why an admitted comparison has the operand kinds
// it resolves under. Closed vocabulary; it is the comparison policy line's
// kind_basis field.
type ComparisonKindBasis string

const (
	// ComparisonKindBasisNone: the comparison is not resolved per operand.
	ComparisonKindBasisNone ComparisonKindBasis = "none"
	// ComparisonKindBasisStated: every operand stated its kind.
	ComparisonKindBasisStated ComparisonKindBasis = "stated"
	// ComparisonKindBasisExactLabel: the kind was resolved from unique exact
	// full-label matches (this file).
	ComparisonKindBasisExactLabel ComparisonKindBasis = "exact_label"
)

// ExactLabelKindRefusal names why the exact-label kind was NOT resolved for a
// bare-name pair. Closed vocabulary; empty when it was resolved or not tried.
type ExactLabelKindRefusal string

const (
	ExactLabelKindRefusalNoCensus     ExactLabelKindRefusal = "no_exact_name_census"
	ExactLabelKindRefusalCensusFailed ExactLabelKindRefusal = "census_failed"
	ExactLabelKindRefusalTruncated    ExactLabelKindRefusal = "census_truncated"
	ExactLabelKindRefusalSideNoMatch  ExactLabelKindRefusal = "side_no_exact_label"
	ExactLabelKindRefusalSideSeveral  ExactLabelKindRefusal = "side_several_exact_labels"
	ExactLabelKindRefusalMixedKinds   ExactLabelKindRefusal = "mixed_kinds"
)

// exactLabelPairKind decides the CHAOS-12600 rule for one comparison. It acts
// only on a bare-name pair (out_of_cut_unstated_kind, exactly two NAMED
// operands, each with exactly one term). On success it returns the classification with
// every slot's kind set and the admission moved to the admitted pair, so the
// pair resolves through resolveNamedComparison exactly as a stated kind does,
// and the subjects the sides matched, one per side in slot order (both sides
// may match the same subject).
//
// The census is the shared exact-name population (deps.ExactNameCandidates,
// every repository, project and team node). A side counts only nodes whose
// NAME or LABEL equals the side's first (verbatim) term, case-folded; an alias
// match is not a full-label match and is not counted. A truncated census
// cannot show that no other subject carries the label, so it refuses.
// Authorization is not decided here: resolveNamedComparison's own gates run
// on the resolved pair, and an unauthorized node of another kind only makes
// this refuse (the kind question), never commit.
func exactLabelPairKind(ctx context.Context, deps ResolveDeps, comparison contextfabric.ComparisonOperands) (contextfabric.ComparisonOperands, []contextfabric.SubjectRef, ExactLabelKindRefusal, bool, error) {
	if comparison.Admission != contextfabric.ComparisonOutOfCutUnstatedKind || len(comparison.Slots) != 2 {
		return comparison, nil, "", false, nil
	}
	for _, slot := range comparison.Slots {
		// A bare name is ONE term per side (codex r3): a side with several
		// terms is not this rule's case and keeps the kind question.
		if slot.Variant != contextfabric.ComparisonOperandNamed || len(slot.Terms) != 1 || strings.TrimSpace(slot.Terms[0]) == "" {
			return comparison, nil, "", false, nil
		}
	}
	if deps.ExactNameCandidates == nil {
		return comparison, nil, ExactLabelKindRefusalNoCensus, false, nil
	}
	nodes, truncated, err := deps.ExactNameCandidates(ctx)
	if err != nil {
		return comparison, nil, ExactLabelKindRefusalCensusFailed, false, err
	}
	if truncated {
		return comparison, nil, ExactLabelKindRefusalTruncated, false, nil
	}
	var kind contextfabric.SubjectKind
	matched := make([]contextfabric.SubjectRef, 0, len(comparison.Slots))
	for _, slot := range comparison.Slots {
		matches := fullLabelMatches(slot.Terms[0], nodes)
		switch {
		case len(matches) == 0:
			return comparison, nil, ExactLabelKindRefusalSideNoMatch, false, nil
		case len(matches) > 1:
			return comparison, nil, ExactLabelKindRefusalSideSeveral, false, nil
		}
		if kind == "" {
			kind = matches[0].Kind
		} else if matches[0].Kind != kind {
			return comparison, nil, ExactLabelKindRefusalMixedKinds, false, nil
		}
		matched = append(matched, matches[0])
	}
	resolved := contextfabric.ComparisonOperands{Admission: contextfabric.ComparisonAdmittedNamedPair, Slots: make([]contextfabric.ComparisonOperandSlot, len(comparison.Slots))}
	for index, slot := range comparison.Slots {
		slot.Kind = kind
		slot.Terms = append([]string(nil), slot.Terms...)
		resolved.Slots[index] = slot
	}
	return resolved, matched, "", true, nil
}

// committedEvery reports whether every subject in want is among committed.
// The exact-label disclosure is owed only when each side committed the subject
// its name matched; both sides may match one subject, which commits once.
func committedEvery(committed, want []contextfabric.SubjectRef) bool {
	held := make(map[string]bool, len(committed))
	for _, subject := range committed {
		held[SubjectKey(subject)] = true
	}
	for _, subject := range want {
		if !held[SubjectKey(subject)] {
			return false
		}
	}
	return len(want) > 0
}

// fullLabelMatches is the subjects whose name or canonical label equals term,
// case-folded. Narrower than exactNameMatches by design: no alias and no
// provider alias, because the rule is a FULL-LABEL match.
func fullLabelMatches(term string, nodes []CandidateNode) []contextfabric.SubjectRef {
	trimmed := strings.TrimSpace(term)
	var matches []contextfabric.SubjectRef
	seen := map[string]bool{}
	for _, node := range nodes {
		subject, ok := NodeSubject(node)
		if !ok || seen[SubjectKey(subject)] {
			continue
		}
		if strings.EqualFold(trimmed, strings.TrimSpace(node.Name)) || strings.EqualFold(trimmed, strings.TrimSpace(subject.Label)) {
			seen[SubjectKey(subject)] = true
			matches = append(matches, subject)
		}
	}
	return matches
}
