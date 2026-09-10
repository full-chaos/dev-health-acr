package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// THE INTERPRETATION BOUNDARY, ON THE TRACE.
//
// A grouped question that the model re-expressed as a flat one used to leave
// no trace of the grouping anywhere a reader of the logs could see it. The
// frame-validation line said `proposed_kind=discovered_kind outcome=valid
// frame_gate=passed`, which is also exactly what a question that never asked
// for a grouping produces. Proving that the question HAD asked for one took a
// join of the interpret receipts against the stored results -- a defect that
// only the store can prove is an observability defect in its own right.
//
// These fields put the whole decision on the one line that already carries
// the validator's verdict:
//
//   - pre-entry: what the model's hints REQUESTED (`requested_group_hint`,
//     `requested_member_hint`),
//   - pre-decision: what the proposed frame EXPRESSED (`proposed_kind`,
//     already on the line, plus `proposed_group_kind`, `proposed_member_kind`),
//   - decision and reason: `outcome`, `failed_invariant`, `failure_detail`
//     and `frame_gate`, already on the line,
//   - post-decision: what became of the group axis (`group_axis`).
//
// Every value is a closed token or an explicit absence token. A kind the
// model did not state reads `absent`, a slot the variant has but left empty
// reads `unset`, a slot the variant does not have reads `not_applicable`, and
// a value the sanitizer dropped as out of vocabulary reads `unrecognized` --
// four different facts that an empty string would have made one.

// Absence tokens for the boundary's kind fields. None is a subject kind, so
// none can be mistaken for one.
const (
	boundaryKindAbsent        = "absent"
	boundaryKindUnset         = "unset"
	boundaryKindNotApplicable = "not_applicable"
	boundaryKindUnrecognized  = "unrecognized"
	boundaryKindUnclassified  = "unclassified"
)

// GroupAxisDecision is what became of the group axis at the interpretation
// boundary. Closed, because it reaches a field consumers group on.
type GroupAxisDecision string

const (
	// GroupAxisNotRequested: no group hint, and the frame expressed no
	// grouping. The ordinary flat question.
	GroupAxisNotRequested GroupAxisDecision = "not_requested"
	// GroupAxisKept: the frame expressed a grouping and the gate let it
	// through.
	GroupAxisKept GroupAxisDecision = "kept"
	// GroupAxisRefused: the frame expressed a grouping and the gate refused
	// the frame -- for a self-group, invariant i6, named on the same line.
	GroupAxisRefused GroupAxisDecision = "refused"
	// GroupAxisDroppedAtInterpretation: the model's own hint asked for a
	// grouping and the frame it proposed expresses none. This is the
	// signature of a grouped question re-expressed as a flat one before the
	// server could judge it -- the state the prompt now tells the model not
	// to produce, made countable so that a model still producing it is
	// visible rather than silently answered flat.
	GroupAxisDroppedAtInterpretation GroupAxisDecision = "dropped_at_interpretation"
)

var canonicalGroupAxisDecisions = map[GroupAxisDecision]struct{}{
	GroupAxisNotRequested:            {},
	GroupAxisKept:                    {},
	GroupAxisRefused:                 {},
	GroupAxisDroppedAtInterpretation: {},
}

// ValidGroupAxisDecision reports membership in the closed vocabulary.
func ValidGroupAxisDecision(value GroupAxisDecision) bool {
	_, member := canonicalGroupAxisDecisions[value]
	return member
}

// InterpretationBoundary is the requested-versus-proposed half of the
// frame-validation line. See the file comment for what each field answers.
type InterpretationBoundary struct {
	RequestedGroupHint  string
	RequestedMemberHint string
	ProposedGroupKind   string
	ProposedMemberKind  string
	GroupAxis           GroupAxisDecision
}

// InterpretationBoundaryFrom reads the boundary off ONE interpretation: the
// receipt's sanitized hints, the frame as the model PROPOSED it (before any
// normalization, so the line shows what the model said), and the gate the
// validator's result decided.
func InterpretationBoundaryFrom(receipt ModelExecutionReceipt, proposed QuestionFrame, gate FrameGate) InterpretationBoundary {
	expression := proposed.SubjectExpression
	boundary := InterpretationBoundary{
		RequestedGroupHint:  hintKindToken(receipt.GroupKind, receipt.GroupKindUnrecognized),
		RequestedMemberHint: hintKindToken(receipt.RequestedSubjectKind, receipt.RequestedSubjectKindUnrecognized),
		ProposedGroupKind:   boundaryKindNotApplicable,
		ProposedMemberKind:  boundaryKindNotApplicable,
	}
	if expression.Kind == SubjectExpressionGroupedMembers {
		group, _ := expression.GroupKind()
		boundary.ProposedGroupKind = slotKindToken(group, receipt.FrameGroupKindUnrecognized)
	}
	if expressionHasMemberSlot(expression.Kind) {
		member, _ := expression.MemberKind()
		boundary.ProposedMemberKind = slotKindToken(member, receipt.FrameMemberKindUnrecognized)
	}
	boundary.GroupAxis = groupAxisDecisionFor(receipt.GroupKind != "" || receipt.GroupKindUnrecognized, expression.Kind, gate)
	return boundary
}

// groupAxisDecisionFor is the boundary's one classification: what became of
// the group axis, from whether a hint asked for one, whether the proposed
// frame expressed one, and whether the gate let that frame through.
func groupAxisDecisionFor(hintRequested bool, proposedKind SubjectExpressionKind, gate FrameGate) GroupAxisDecision {
	if proposedKind == SubjectExpressionGroupedMembers {
		if gate.Refuses() {
			return GroupAxisRefused
		}
		return GroupAxisKept
	}
	if hintRequested {
		return GroupAxisDroppedAtInterpretation
	}
	return GroupAxisNotRequested
}

// expressionHasMemberSlot reports whether the variant carries a member (or
// declared) kind at all. explicit_set does not -- its kinds live on its
// operands -- and neither does an out-of-vocabulary variant.
func expressionHasMemberSlot(kind SubjectExpressionKind) bool {
	switch kind {
	case SubjectExpressionNamed, SubjectExpressionDiscoveredKind, SubjectExpressionChildrenOfScope,
		SubjectExpressionGroupedMembers, SubjectExpressionOrganizationScope:
		return true
	}
	return false
}

// hintKindToken renders a sanitized receipt hint.
func hintKindToken(kind SubjectKind, unrecognized bool) string {
	switch {
	case kind != "":
		return closedKindToken(kind)
	case unrecognized:
		return boundaryKindUnrecognized
	default:
		return boundaryKindAbsent
	}
}

// slotKindToken renders a kind slot the variant HAS.
func slotKindToken(kind SubjectKind, unrecognized bool) string {
	switch {
	case kind != "":
		return closedKindToken(kind)
	case unrecognized:
		return boundaryKindUnrecognized
	default:
		return boundaryKindUnset
	}
}

// closedKindToken is the last line before a log field: a kind outside the
// published vocabulary is named as such rather than written verbatim, so no
// model text can reach the line through a kind slot.
func closedKindToken(kind SubjectKind) string {
	if !contractsv1.ValidContextFabricSubjectKind(kind) {
		return boundaryKindUnclassified
	}
	return string(kind)
}
