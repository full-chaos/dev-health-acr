package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

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
//   - Only when the named subject's terms are the terms retrieval will
//     search: subject resolution searches the interpretation's flat subject
//     terms, not the frame's, so a repaired anchor is the anchor retrieval
//     follows only when the two are the same set (trimmed,
//     case-insensitive). A named subject with several terms is one subject
//     with several retrieval pointers, and I5 admits several anchor terms.
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
//
// A SECOND BOUNDED REPAIR, FOR I7 (CHAOS-5839): a compare goal proposed over
// a grouped cohort names no explicit operand set to compare -- the same
// interpretation's own remaining goals already say what the question is
// asking instead. See repairCompareGroupedCollapse's own doc comment below
// for its bound. Every repair in frameRepairTable answers exactly one
// invariant, runs at most once per proposal (frameRepairBound), and
// revalidates through validateAgainstInterpretation unrelaxed -- the shape
// this file's first repair established, extended here rather than
// replaced.

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
	// FrameRepairDeclinedTermsDiverge: the named subject's terms are not the
	// flat subject terms retrieval searches. Refused on I9.
	FrameRepairDeclinedTermsDiverge FrameRepairDecision = "declined_terms_diverge"
	// FrameRepairDeclinedHintUnservable: no discovery arm serves the hinted
	// kind. Refused on I9.
	FrameRepairDeclinedHintUnservable FrameRepairDecision = "declined_hint_unservable"
	// FrameRepairDeclinedBoundReached: the result already carries the bounded
	// number of attempts. Refused on the failure it carries.
	FrameRepairDeclinedBoundReached FrameRepairDecision = "declined_bound_reached"
	// FrameRepairDeclinedNotGroupedCohort: the I7 failure is not over a
	// grouped_members expression. Refused on I7.
	FrameRepairDeclinedNotGroupedCohort FrameRepairDecision = "declined_not_grouped_cohort"
)

var frameRepairDecisions = [...]FrameRepairDecision{
	FrameRepairNotApplicable,
	FrameRepairApplied,
	FrameRepairRefusedAfterRepair,
	FrameRepairDeclinedNotNamedSubject,
	FrameRepairDeclinedHintAbsent,
	FrameRepairDeclinedHintUnrecognized,
	FrameRepairDeclinedHintIsSubjectKind,
	FrameRepairDeclinedTermsDiverge,
	FrameRepairDeclinedHintUnservable,
	FrameRepairDeclinedBoundReached,
	FrameRepairDeclinedNotGroupedCohort,
}

// FrameRepairTermsMatch is whether the named subject's terms equal the flat
// subject terms retrieval searches. Empty when the comparison did not run.
type FrameRepairTermsMatch string

const (
	// FrameRepairTermsSame: the two term sets are equal.
	FrameRepairTermsSame FrameRepairTermsMatch = "match"
	// FrameRepairTermsDiverge: the two term sets differ.
	FrameRepairTermsDiverge FrameRepairTermsMatch = "diverge"
)

// FrameRepairDecisionCount is the closed vocabulary's size.
const FrameRepairDecisionCount = len(frameRepairDecisions)

// FrameRepairDecisionVocabulary returns the closed vocabulary in declared
// order.
func FrameRepairDecisionVocabulary() [FrameRepairDecisionCount]FrameRepairDecision {
	return frameRepairDecisions
}

// FrameRepairName names a bounded repair.
type FrameRepairName string

// FrameRepairCountKindCollapse is the I9 repair this file implements.
const FrameRepairCountKindCollapse FrameRepairName = "count_kind_collapse"

// FrameRepairCompareGroupedCollapse is the I7 repair this file implements.
const FrameRepairCompareGroupedCollapse FrameRepairName = "compare_grouped_collapse"

var frameRepairNames = [...]FrameRepairName{
	FrameRepairCountKindCollapse,
	FrameRepairCompareGroupedCollapse,
}

// FrameRepairNameCount is the closed vocabulary's size.
const FrameRepairNameCount = len(frameRepairNames)

// FrameRepairNameVocabulary returns the closed vocabulary of bounded
// repairs, in declared order.
func FrameRepairNameVocabulary() [FrameRepairNameCount]FrameRepairName {
	return frameRepairNames
}

var frameRepairTermsMatches = [...]FrameRepairTermsMatch{
	FrameRepairTermsSame,
	FrameRepairTermsDiverge,
}

// FrameRepairTermsMatchCount is the closed vocabulary's size.
const FrameRepairTermsMatchCount = len(frameRepairTermsMatches)

// FrameRepairTermsMatchVocabulary returns the closed vocabulary in declared
// order. The empty value (the comparison did not run) is not a member --
// FrameRepair.ObservableTermsMatch renders it as `not_evaluated` for a log
// line rather than as a member of this type.
func FrameRepairTermsMatchVocabulary() [FrameRepairTermsMatchCount]FrameRepairTermsMatch {
	return frameRepairTermsMatches
}

// frameRepairBound is the most repair attempts one proposal gets.
const frameRepairBound = 1

// FrameRepairCarry is the set of downstream-carried fields a repaired
// proposal must state to be shape-equivalent to the direct proposal it
// repairs into -- every field a consumer beyond this package reads off the
// receipt or frame for that SHAPE, not only the fields I9 itself names.
//
// TestEveryRepairPopulatesEveryCarriedField (frame_repair_test.go) walks
// frameRepairTable by reflection against this struct's own fields: a repair
// that reaches FrameRepairApplied must explicitly account for every field
// here, either populating it or declaring in its own fixture that its
// repaired shape never states it (a field this struct gains later, with no
// repair's fixture updated for it, fails the same way) -- CHAOS-5825:
// repairCountKindCollapse shipped changing only SubjectExpression and left
// the receipt's own ScopeAnchorKind at whatever the model's raw output
// happened to state (typically absent for a named_subject proposal), so
// graphrank's anchor-pool "receipt" source (chaos5393_anchor_pool.go) read
// none/none for a repaired children_of_scope proposal a direct one would
// have carried a real anchor kind for. ScopeAnchorKind is itself scoped to
// children_of_scope (scope_anchor_kind.go: grouped_members has a grouping
// axis, never a scope anchor), so a repair whose shape stays grouped_members
// declares this field zero rather than fabricating one.
type FrameRepairCarry struct {
	// ScopeAnchorKind is the anchor's own kind for a repaired
	// children_of_scope expression -- the value a direct proposal of the
	// same shape would have stated as scope_anchor_kind. Set only when the
	// repair applied.
	ScopeAnchorKind SubjectKind
	// RequestedJudgment is a repair's own closed-phrase substitute for the
	// SAME interpretation's InterpretedQuestion.RequestedJudgment -- the
	// free-text judgment field synthesis reads directly off the
	// interpretation, never off the frame or the receipt (chaos4636_synthesis_assembly.go).
	// A repair that changes Goals makes that free text stale (it still
	// names the goal the repair dropped), and unlike ScopeAnchorKind there
	// is no model-authored value to fall back on for the ACCEPTED shape --
	// the model's own free text described the shape it proposed, not the
	// shape the repair produced. Built from a small closed phrase table
	// keyed on the accepted Goals (requestedJudgmentForGoals), never from
	// model output, so it carries the same content-safety guarantee every
	// other closed field on this struct has. Empty when the repair does
	// not touch Goals (I9's count-kind repair: the model's own text still
	// describes the accepted shape, since only SubjectExpression changed).
	RequestedJudgment string
}

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
	// TermsMatch is the comparison of the named subject's terms with the
	// flat subject terms retrieval searches, empty when it did not run.
	TermsMatch FrameRepairTermsMatch
	// Attempts is how many repairs ran on this proposal.
	Attempts int
	// Carry is the downstream-carried fields this repair stated, populated
	// only on FrameRepairApplied. See FrameRepairCarry's own doc comment.
	Carry FrameRepairCarry
}

// ObservableTermsMatch renders the terms comparison for a log line:
// `not_evaluated` when it did not run.
func (r FrameRepair) ObservableTermsMatch() string {
	if r.TermsMatch == "" {
		return "not_evaluated"
	}
	return string(r.TermsMatch)
}

// sameSubjectTermSet reports whether two term lists name the same set of
// retrieval pointers, case-insensitively, duplicates ignored. Both inputs are
// already bounded upstream: named terms pass I3 (non-empty, no blank term)
// and flat terms pass the interpreted-question contract (no blank or padded
// term).
func sameSubjectTermSet(named, flat []string) bool {
	set := func(terms []string) map[string]bool {
		out := make(map[string]bool, len(terms))
		for _, term := range terms {
			out[strings.ToLower(term)] = true
		}
		return out
	}
	left, right := set(named), set(flat)
	if len(left) != len(right) {
		return false
	}
	for term := range left {
		if !right[term] {
			return false
		}
	}
	return true
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
// was validated from, receipt is the same interpretation's receipt, and
// subjectTerms are its flat subject terms, the terms retrieval searches.
func repairCountKindCollapse(receipt ModelExecutionReceipt, proposed QuestionFrame, emittedShape InvestigationShape, subjectTerms []string, result FrameValidationResult) FrameValidationResult {
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
	// hint that is present). Hoisted (rather than scoped to this check
	// alone) because the Applied path below carries it as the repaired
	// expression's own anchor kind (FrameRepairCarry.ScopeAnchorKind).
	subjectKind, _ := proposed.SubjectExpression.MemberKind()
	if subjectKind == hint || receipt.ScopeAnchorKind == hint {
		return declined(FrameRepairDeclinedHintIsSubjectKind)
	}
	considered.TermsMatch = FrameRepairTermsSame
	if !sameSubjectTermSet(proposed.SubjectExpression.SubjectTerms(), subjectTerms) {
		considered.TermsMatch = FrameRepairTermsDiverge
		return declined(FrameRepairDeclinedTermsDiverge)
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
	// subjectKind is the named subject's own stated kind (the declined-above
	// check already proved it differs from hint) -- exactly the anchor kind
	// a direct children_of_scope proposal of this same shape would have
	// stated as scope_anchor_kind, since the anchor terms below are that
	// same subject's own terms: without carrying it here, the receipt's own
	// ScopeAnchorKind would stay at the model's raw (typically absent)
	// value for a named_subject proposal.
	considered.Carry.ScopeAnchorKind = subjectKind
	return FrameValidationResult{Frame: revalidated.Frame, Outcome: FrameValidationOutcomeRepaired, Repair: considered}
}

// repairCompareGroupedCollapse applies the I7 bound above to one
// validation result and returns the result the turn acts on. A compare
// goal read over a grouped cohort names no explicit operand set -- the
// question is not asking to set two or more named things side by side, and
// the same proposal's own remaining goals (or their absence) already say
// what it is asking instead.
//
// THE BOUND, every clause pinned by frame_repair_test.go:
//
//   - Only an I7 failure. A frame that fails any other invariant first is
//     refused exactly as before.
//   - Only from grouped_members. compare over children_of_scope,
//     discovered_kind, named_subject or organization_scope has no evidenced
//     reading and is refused as before; widening to those kinds without
//     evidence would risk a frame class this repair was never proven
//     against.
//   - Only Goals changes. SubjectExpression, Temporal, Dimensions,
//     Emphasis and every other field are the proposal's own -- I7 reads
//     Goals and the expression kind, so this is the field the violated
//     invariant itself names.
//   - Compare is dropped; explain_change is added when the proposal did not
//     already state it; describe_trend is kept when the proposal already
//     stated it, otherwise assess_state is added -- never both, and never a
//     goal the proposal did not already carry beyond that one slot. Nothing
//     about a trend the proposal never signalled is invented.
//   - The repaired frame passes the SAME validation, unrelaxed. If it does
//     not (I8's temporal requirement not met, most often), the turn is
//     refused with the invariant the repaired frame failed, and the event
//     says a repair ran.
//   - At most frameRepairBound attempts.
//
// CARRY: this repair never touches SubjectExpression, and grouped_members
// never carries a scope anchor kind (scope_anchor_kind.go) -- its Carry
// stays the zero value, exactly what a direct proposal of this same shape
// would also carry. See FrameRepairCarry's own doc comment and this
// repair's fixture in repairTableFixtures (frame_repair_test.go).
func repairCompareGroupedCollapse(receipt ModelExecutionReceipt, proposed QuestionFrame, emittedShape InvestigationShape, subjectTerms []string, result FrameValidationResult) FrameValidationResult {
	if result.Failure.Invariant != FrameInvariantI7 {
		if result.Repair.Decision == "" {
			result.Repair.Decision = FrameRepairNotApplicable
		}
		return result
	}
	considered := FrameRepair{
		Name:       FrameRepairCompareGroupedCollapse,
		Invariant:  FrameInvariantI7,
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
	if proposed.SubjectExpression.Kind != SubjectExpressionGroupedMembers {
		return declined(FrameRepairDeclinedNotGroupedCohort)
	}
	repaired := proposed
	repaired.Goals = replaceCompareGoal(proposed.Goals)
	considered.Attempts++
	considered.KindAfter = repaired.SubjectExpression.Kind
	revalidated := validateAgainstInterpretation(receipt, repaired, emittedShape)
	if revalidated.Outcome != FrameValidationOutcomeValid {
		considered.Decision = FrameRepairRefusedAfterRepair
		return FrameValidationResult{Outcome: revalidated.Outcome, Failure: revalidated.Failure, Repair: considered}
	}
	considered.Decision = FrameRepairApplied
	// Derived from the NORMALIZED accepted Goals (revalidated.Frame.Goals),
	// the same value the frame-validation line's own accepted_goals field
	// reports, so the two never disagree about which shape they describe.
	considered.Carry.RequestedJudgment = requestedJudgmentForGoals(revalidated.Frame.Goals)
	return FrameValidationResult{Frame: revalidated.Frame, Outcome: FrameValidationOutcomeRepaired, Repair: considered}
}

// goalJudgmentPhrase is a TOTAL function over the closed Goal vocabulary
// (frame_vocab.go, InvestigationGoalVocabulary): every member names its own
// fragment, including GoalCompare's, which requestedJudgmentForGoals never
// reaches from this repair's own output (replaceCompareGoal always removes
// it) but which the vocabulary-completeness test below still requires --
// the composition is a function of whatever accepted goal set a FUTURE
// repair or caller passes, not only the shapes this repair happens to
// produce today. TestRequestedJudgmentCoversTheWholeGoalVocabulary walks
// InvestigationGoalVocabulary() by enumeration and fails closed on a
// member with no entry here, so a ninth goal added to the vocabulary
// without a fragment here fails the build's own test suite rather than
// silently composing a phrase that drops it.
var goalJudgmentPhrase = map[InvestigationGoal]string{
	GoalAssessState:        "the current state",
	GoalExplainDrivers:     "what is driving that state",
	GoalCompare:            "a comparison",
	GoalRankOrSurvey:       "a ranking or survey",
	GoalDescribeTrend:      "the trend over time",
	GoalExplainChange:      "an explanation of the change",
	GoalAllocateInvestment: "where effort is going",
	GoalCountOrAggregate:   "a count",
}

// requestedJudgmentForGoals renders an accepted Goals set as a closed-phrase
// substitute for InterpretedQuestion.RequestedJudgment -- see
// FrameRepairCarry.RequestedJudgment's own doc comment for why one is
// needed at all. A total function of the FULL accepted list through
// goalJudgmentPhrase, composed in the list's own order, so a goal this
// repair did not expect to see (a co-occurring goal replaceCompareGoal
// only ever passes through, never invents) still contributes its own
// fragment instead of vanishing from the composed text silently.
func requestedJudgmentForGoals(goals []InvestigationGoal) string {
	parts := make([]string, 0, len(goals))
	for _, goal := range goals {
		if text, known := goalJudgmentPhrase[goal]; known {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " and ")
}

// replaceCompareGoal is the I7 repair's goal transform: compare is dropped;
// explain_change is guaranteed present; describe_trend is kept if the
// proposal already stated it, otherwise assess_state is added so the
// repaired set never loses the "what is the subject's standing" reading a
// direct proposal of this shape would carry alongside explain_change.
// Every other goal the proposal stated (unevidenced today, since compare
// was observed paired only with describe_trend and/or explain_change) is
// kept, in the proposal's own order.
func replaceCompareGoal(goals []InvestigationGoal) []InvestigationGoal {
	hasTrend, hasState, hasChange := false, false, false
	kept := make([]InvestigationGoal, 0, len(goals)+2)
	for _, goal := range goals {
		switch goal {
		case GoalCompare:
			continue
		case GoalDescribeTrend:
			hasTrend = true
		case GoalAssessState:
			hasState = true
		case GoalExplainChange:
			hasChange = true
		}
		kept = append(kept, goal)
	}
	if !hasChange {
		kept = append(kept, GoalExplainChange)
	}
	if !hasTrend && !hasState {
		kept = append(kept, GoalAssessState)
	}
	return kept
}

// frameRepairFunc is one bounded repair's shape -- exactly
// repairCountKindCollapse's own signature, so every repair in
// frameRepairTable is invoked identically by validateProposedFrame.
type frameRepairFunc func(receipt ModelExecutionReceipt, proposed QuestionFrame, emittedShape InvestigationShape, subjectTerms []string, result FrameValidationResult) FrameValidationResult

// frameRepairTable is every bounded repair this package runs, tried in
// order against the same evolving result. TestEveryRepairPopulatesEveryCarriedField
// (frame_repair_test.go) walks this slice by reflection against
// FrameRepairCarry's own fields, so a repair joins production only once its
// fixture proves it, for every field on that struct, either populates the
// field or declares that its shape never states it.
var frameRepairTable = []frameRepairFunc{
	repairCountKindCollapse,
	repairCompareGroupedCollapse,
}
