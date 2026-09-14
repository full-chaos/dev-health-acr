package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// THE ONE BOUNDED REPAIR (CHAOS-5723): a count over a named subject, re-read
// as the scoped cohort the same interpretation's member hint names.
//
// Invariant I9 refuses a count goal over a single named subject -- a count
// needs a set. The interpretation call that proposed such a frame also emits
// its requested member kind (the receipt's RequestedSubjectKind, logged as
// `requested_member_hint`): the kind the answer is about. When that kind is a
// servable member kind and is not the named subject's own kind, the question
// counts members of that kind under the named subject, and the union already
// has that reading: children_of_scope, anchored on the named subject. §13.6
// admits one bounded repair on a frame that fails validation; this is that
// repair, for I9 only.
//
// THE BOUND, every clause pinned by frame_repair_test.go:
//
//   - Only an I9 failure of the PROPOSAL. A frame that fails any other
//     invariant first is refused exactly as before.
//   - Only from named_subject. An explicit_set enumerates named operands; it is
//     not a scoped cohort, so a count over one is refused as before.
//   - Only with a recognised hint. An absent hint, or a hint the sanitizer
//     dropped, gives the repair nothing to read, and it never guesses one.
//   - Never with a hint equal to a kind the interpretation stated for the
//     named subject itself (its ExpectedKind, or the sample's scope-anchor
//     kind): members of a kind under an anchor of that same kind is not a
//     scoped cohort (phase-B invariant I11).
//   - Only a member kind a discovery arm serves, decided by
//     CohortMemberKindFor, the predicate the gate refuses on. A repair into a
//     frame the gate would refuse on its basis would trade one refusal for
//     another and hide the first.
//   - Only SubjectExpression changes: the named subject's terms become the
//     anchor terms, the hint becomes the member kind, and every other field is
//     the proposal's own. I9 reads Goals and the expression kind, so this kind
//     change is one the violated invariant names; `refused_kind_change` stays
//     reserved for a repair that changes a kind its invariant does not name.
//   - The repaired frame passes the SAME validation, unrelaxed, including the
//     requested-group-axis check. If it does not, the turn is refused with the
//     invariant the repaired frame failed, and the event says a repair ran.
//   - At most frameRepairBound attempts.
//
// ValidateFrame never calls this. The hint is the receipt's, and the invariant
// table reads the frame alone; validateProposedFrame is the one place both
// halves of one model call are in hand. A carried frame is revalidated, never
// repaired (composeAcceptedContext).

// FrameRepairDecision is what the bounded repair decided for one proposal.
// Closed, because it reaches a log field operators group on.
type FrameRepairDecision string

const (
	// FrameRepairNotApplicable: the proposal did not fail I9, so no repair
	// was considered.
	FrameRepairNotApplicable FrameRepairDecision = "not_applicable"
	// FrameRepairApplied: the repaired frame passed validation and is the
	// frame the turn acts on. The outcome is `repaired`.
	FrameRepairApplied FrameRepairDecision = "applied"
	// FrameRepairRefusedAfterRepair: the repair ran and the repaired frame
	// failed validation. The turn is refused with that failure.
	FrameRepairRefusedAfterRepair FrameRepairDecision = "refused_after_repair"
	// FrameRepairDeclinedNotNamedSubject: the I9 failure is not over a
	// named_subject (explicit_set). Refused on I9.
	FrameRepairDeclinedNotNamedSubject FrameRepairDecision = "declined_not_named_subject"
	// FrameRepairDeclinedHintAbsent: no member hint. Refused on I9.
	FrameRepairDeclinedHintAbsent FrameRepairDecision = "declined_hint_absent"
	// FrameRepairDeclinedHintUnrecognized: the hint was outside the subject
	// kind vocabulary. Refused on I9.
	FrameRepairDeclinedHintUnrecognized FrameRepairDecision = "declined_hint_unrecognized"
	// FrameRepairDeclinedHintIsSubjectKind: the hint names the named subject's
	// own stated kind. Refused on I9.
	FrameRepairDeclinedHintIsSubjectKind FrameRepairDecision = "declined_hint_is_subject_kind"
	// FrameRepairDeclinedHintUnservable: no discovery arm serves the hinted
	// kind. Refused on I9.
	FrameRepairDeclinedHintUnservable FrameRepairDecision = "declined_hint_unservable"
	// FrameRepairDeclinedBoundReached: the result already carries the bounded
	// number of attempts. Refused on the failure it carries.
	FrameRepairDeclinedBoundReached FrameRepairDecision = "declined_bound_reached"
)

var frameRepairDecisions = [...]FrameRepairDecision{
	FrameRepairNotApplicable,
	FrameRepairApplied,
	FrameRepairRefusedAfterRepair,
	FrameRepairDeclinedNotNamedSubject,
	FrameRepairDeclinedHintAbsent,
	FrameRepairDeclinedHintUnrecognized,
	FrameRepairDeclinedHintIsSubjectKind,
	FrameRepairDeclinedHintUnservable,
	FrameRepairDeclinedBoundReached,
}

// FrameRepairDecisionCount is the closed vocabulary's size.
const FrameRepairDecisionCount = len(frameRepairDecisions)

// FrameRepairDecisionVocabulary returns the closed vocabulary in declared
// order.
func FrameRepairDecisionVocabulary() [FrameRepairDecisionCount]FrameRepairDecision {
	return frameRepairDecisions
}

// FrameRepairName names a bounded repair. One member today.
type FrameRepairName string

// FrameRepairCountKindCollapse is the I9 repair this file implements.
const FrameRepairCountKindCollapse FrameRepairName = "count_kind_collapse"

// frameRepairBound is the most repair attempts one proposal gets.
const frameRepairBound = 1

// FrameRepair records the bounded repair's decision on one proposal: the
// deciding fields the frame-validation line carries, so the trace alone says
// whether a repair ran, why or why not, and what it changed.
type FrameRepair struct {
	// Decision is the closed decision. Empty only on a result no interpreter
	// decided (ValidateFrame's own callers).
	Decision FrameRepairDecision
	// Name is the repair considered, empty when none was.
	Name FrameRepairName
	// Invariant is the invariant the repair answers, empty when none.
	Invariant FrameInvariant
	// KindBefore is the proposal's expression kind, set whenever a repair was
	// considered.
	KindBefore SubjectExpressionKind
	// KindAfter and MemberKind are the repaired expression's kind and member
	// kind, set only when the repair ran.
	KindAfter  SubjectExpressionKind
	MemberKind SubjectKind
	// Attempts is how many repairs ran on this proposal.
	Attempts int
}

// ObservableDecision renders the decision for a log line: `not_evaluated`
// when no decision was taken, so an undecided result never reads as one that
// was considered and found not applicable.
func (r FrameRepair) ObservableDecision() string {
	if r.Decision == "" {
		return "not_evaluated"
	}
	return string(r.Decision)
}

// noneWhenEmpty renders an empty closed token as the word `none`, so a slot
// the decision left empty never reads as a key nobody wrote.
func noneWhenEmpty(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// repairMemberKindToken renders the repair's member kind: `none` when the
// repair did not run, and a closed kind token otherwise, so no model text can
// reach the line through this slot.
func repairMemberKindToken(kind SubjectKind) string {
	if kind == "" {
		return "none"
	}
	return closedKindToken(kind)
}

// repairCountKindCollapse applies the bound above to one validation result
// and returns the result the turn acts on. proposed is the frame the result
// was validated from, and receipt is the same interpretation's receipt.
func repairCountKindCollapse(receipt ModelExecutionReceipt, proposed QuestionFrame, emittedShape InvestigationShape, result FrameValidationResult) FrameValidationResult {
	if result.Failure.Invariant != FrameInvariantI9 {
		if result.Repair.Decision == "" {
			result.Repair.Decision = FrameRepairNotApplicable
		}
		return result
	}
	considered := FrameRepair{
		Name:       FrameRepairCountKindCollapse,
		Invariant:  FrameInvariantI9,
		KindBefore: proposed.SubjectExpression.Kind,
		Attempts:   result.Repair.Attempts,
	}
	declined := func(decision FrameRepairDecision) FrameValidationResult {
		considered.Decision = decision
		result.Repair = considered
		return result
	}
	if result.Repair.Attempts >= frameRepairBound {
		return declined(FrameRepairDeclinedBoundReached)
	}
	if proposed.SubjectExpression.Kind != SubjectExpressionNamed {
		return declined(FrameRepairDeclinedNotNamedSubject)
	}
	hint := receipt.RequestedSubjectKind
	if receipt.RequestedSubjectKindUnrecognized || (hint != "" && !contractsv1.ValidContextFabricSubjectKind(hint)) {
		return declined(FrameRepairDeclinedHintUnrecognized)
	}
	if hint == "" {
		return declined(FrameRepairDeclinedHintAbsent)
	}
	// The named subject's stated kind is its ExpectedKind, read through the
	// expression's own accessor ("" when it states none, which never equals a
	// hint that is present).
	if subjectKind, _ := proposed.SubjectExpression.MemberKind(); subjectKind == hint || receipt.ScopeAnchorKind == hint {
		return declined(FrameRepairDeclinedHintIsSubjectKind)
	}
	repaired := proposed
	repaired.SubjectExpression = SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: proposed.SubjectExpression.SubjectTerms(), MemberKind: hint},
	}
	if _, _, reason := CohortMemberKindFor(repaired.SubjectExpression); reason != CohortDiscoverable {
		return declined(FrameRepairDeclinedHintUnservable)
	}
	considered.Attempts++
	considered.KindAfter = repaired.SubjectExpression.Kind
	considered.MemberKind = hint
	revalidated := validateAgainstInterpretation(receipt, repaired, emittedShape)
	if revalidated.Outcome != FrameValidationOutcomeValid {
		considered.Decision = FrameRepairRefusedAfterRepair
		return FrameValidationResult{Outcome: revalidated.Outcome, Failure: revalidated.Failure, Repair: considered}
	}
	considered.Decision = FrameRepairApplied
	return FrameValidationResult{Frame: revalidated.Frame, Outcome: FrameValidationOutcomeRepaired, Repair: considered}
}
