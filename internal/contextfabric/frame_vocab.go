package contextfabric

import (
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4452 stage 2 (S7b-i), design §13.2.2: the five closed vocabularies
// the compositional QuestionFrame is built from.
//
// SHADOW ONLY, and shadow in the strong sense chaos3900_window_vocab.go
// established for W0 and chaos4632_question_family_vocab.go repeated for
// S2: every type in this file and its siblings is defined DIRECTLY in
// package contextfabric with ZERO wire-contract surface. Nothing here
// appears in internal/contracts/v1, in contracts/jsonschema, in the
// OpenAPI document, or in the MCP manifest, so nothing here can reach
// ask-dev's additionalProperties:false validator (CHAOS-4623) or require a
// two-step deploy. The frame rides ModelExecutionReceipt, exactly as the
// W0 window classification and the S2 family capture already do.
//
// WHY THE SHADOW IS THE POINT rather than a staging convenience. §13.4.3
// makes phase 1 explicit: "the frame is derived, validated, persisted on
// the RECEIPT ONLY and telemetered; the shipped §4.2 precedence table
// still decides the family; a differential counter records
// derived_family == precedence_family. No wire field, so no two-step
// deploy in phase 1 -- this is the same 'cheaper falsification available
// first' move S2 used for GroupKind/ScopeAnchorTerm, and it tests the
// derivation before any contract moves." The promotion to an optional
// wire field is phase 2 and is gated on the shadow data.
//
// STRIKE-THREE COMPLIANCE, stated once for the whole slice. The standing
// design principle of 2026-08-31 13:35 bans phrasing tables, template
// parsers, regexes over internal strings and canonical-question matching:
// language is the model layer's job at both boundaries. Every vocabulary
// below is a CLOSED ENUM the model PICKS FROM and the server VALIDATES --
// determinism as a safety property, which that principle explicitly
// permits ("Hardcode only where determinism is a safety property
// (contracts, receipts, validation, fact grounding)"). There is no string
// matching against question text anywhere in this slice. The two keyword
// matchers that used to decide structure from prose were L6 inventory rows
// 1 and 2, and CHAOS-4736 (seam 7) DELETED both -- their identifiers are
// deliberately not spelled here, because that ticket's acceptance is a grep
// showing no declaration and no caller. Nothing here adds a third.

// InvestigationGoal is the closed vocabulary of what the user is asking
// the system to establish. Design §13.2.2, EIGHT members.
//
// Goals is a SET, not a scalar, and that reversal is load-bearing rather
// than cosmetic. Round 2 of the design review falsified the singular shape
// with the design's own governing example -- BAR question Q2, "What teams
// are struggling and what are the driving factors?" -- for which no legal
// single-goal frame made both `ranking` and `principal_drivers` REQUIRED:
// rank_or_survey gave ranking without drivers, explain_drivers gave
// drivers without ranking, and widening drivers made them advisory so a
// bare ranking counted as complete. Rank and explain-drivers are two
// operations on the GOAL axis itself and no other axis can carry the
// second (§13.2.3, recorded there as a lane judgement overturned by
// evidence).
//
// The set was chosen over a Goal + SecondaryGoals pair for a reason the
// design records as DECIDING rather than as a tiebreaker: the
// primary/secondary shape would make the MODEL author the routing
// decision, because "primary" IS the routing choice promoted to a wire
// field. §13.2.1's authorship rule forbids that. Family derivation
// therefore reads the set through a fixed row order (§13.4.1), not
// through a model-nominated primary.
type InvestigationGoal string

const (
	// GoalAssessState: what is the current state of the subject.
	GoalAssessState InvestigationGoal = "assess_state"
	// GoalExplainDrivers: what is driving that state.
	GoalExplainDrivers InvestigationGoal = "explain_drivers"
	// GoalCompare: set two or more named operands side by side.
	GoalCompare InvestigationGoal = "compare"
	// GoalRankOrSurvey: order or survey a set of subjects.
	GoalRankOrSurvey InvestigationGoal = "rank_or_survey"
	// GoalDescribeTrend: describe movement over a time axis.
	GoalDescribeTrend InvestigationGoal = "describe_trend"
	// GoalExplainChange: explain a change between two periods.
	GoalExplainChange InvestigationGoal = "explain_change"
	// GoalAllocateInvestment: where is effort going.
	GoalAllocateInvestment InvestigationGoal = "allocate_investment"
	// GoalCountOrAggregate: how many / how much.
	GoalCountOrAggregate InvestigationGoal = "count_or_aggregate"
)

var investigationGoals = [...]InvestigationGoal{
	GoalAssessState,
	GoalExplainDrivers,
	GoalCompare,
	GoalRankOrSurvey,
	GoalDescribeTrend,
	GoalExplainChange,
	GoalAllocateInvestment,
	GoalCountOrAggregate,
}

// InvestigationGoalCount is eight. §13.2.2 says eight; a ninth appearing
// without the design changing is drift this build should not survive, and
// the registry test asserts it.
const InvestigationGoalCount = len(investigationGoals)

// InvestigationGoalVocabulary returns the closed vocabulary in design
// order. Design order is also the GOAL PRECEDENCE order §13.4.1 rows 5
// and 6 read, so it is not an arbitrary listing.
func InvestigationGoalVocabulary() [InvestigationGoalCount]InvestigationGoal {
	return investigationGoals
}

// ValidInvestigationGoal reports membership. The empty value is not a
// member.
func ValidInvestigationGoal(value InvestigationGoal) bool {
	for _, member := range investigationGoals {
		if member == value {
			return true
		}
	}
	return false
}

// SubjectExpressionKind is the closed discriminator of the
// SubjectExpression union. Design §13.2.2, SIX members.
type SubjectExpressionKind string

const (
	// SubjectExpressionNamed: one or more named subjects.
	SubjectExpressionNamed SubjectExpressionKind = "named_subject"
	// SubjectExpressionExplicitSet: an explicitly enumerated set of
	// operands, each a named subject or a scoped set.
	SubjectExpressionExplicitSet SubjectExpressionKind = "explicit_set"
	// SubjectExpressionDiscoveredKind: find the members of a kind.
	SubjectExpressionDiscoveredKind SubjectExpressionKind = "discovered_kind"
	// SubjectExpressionChildrenOfScope: the members of a kind reachable
	// from a named anchor.
	SubjectExpressionChildrenOfScope SubjectExpressionKind = "children_of_scope"
	// SubjectExpressionGroupedMembers: members of one kind grouped by
	// another.
	SubjectExpressionGroupedMembers SubjectExpressionKind = "grouped_members"
	// SubjectExpressionOrganizationScope: the organization itself is the
	// subject.
	SubjectExpressionOrganizationScope SubjectExpressionKind = "organization_scope"
)

var subjectExpressionKinds = [...]SubjectExpressionKind{
	SubjectExpressionNamed,
	SubjectExpressionExplicitSet,
	SubjectExpressionDiscoveredKind,
	SubjectExpressionChildrenOfScope,
	SubjectExpressionGroupedMembers,
	SubjectExpressionOrganizationScope,
}

// SubjectExpressionKindCount is six.
const SubjectExpressionKindCount = len(subjectExpressionKinds)

// SubjectExpressionKindVocabulary returns the closed vocabulary in design
// order.
func SubjectExpressionKindVocabulary() [SubjectExpressionKindCount]SubjectExpressionKind {
	return subjectExpressionKinds
}

// ValidSubjectExpressionKind reports membership. The empty value is not a
// member.
func ValidSubjectExpressionKind(value SubjectExpressionKind) bool {
	for _, member := range subjectExpressionKinds {
		if member == value {
			return true
		}
	}
	return false
}

// TemporalIntent is the closed temporal axis. Design §13.2.2, FOUR
// members, exactly one per frame.
type TemporalIntent string

const (
	// TemporalIntentCurrent: as of now. The derived default when unset.
	TemporalIntentCurrent TemporalIntent = "current"
	// TemporalIntentBoundedWindow: within a stated window. Discharged as a
	// requirement PROPERTY, not an obligation (§13.2.3 table 2, law L2).
	TemporalIntentBoundedWindow TemporalIntent = "bounded_window"
	// TemporalIntentPeriodComparison: this period against another.
	TemporalIntentPeriodComparison TemporalIntent = "period_comparison"
	// TemporalIntentTimeSeries: a series over time.
	TemporalIntentTimeSeries TemporalIntent = "time_series"
)

var temporalIntents = [...]TemporalIntent{
	TemporalIntentCurrent,
	TemporalIntentBoundedWindow,
	TemporalIntentPeriodComparison,
	TemporalIntentTimeSeries,
}

// TemporalIntentCount is four.
const TemporalIntentCount = len(temporalIntents)

// TemporalIntentVocabulary returns the closed vocabulary in design order.
func TemporalIntentVocabulary() [TemporalIntentCount]TemporalIntent {
	return temporalIntents
}

// ValidTemporalIntent reports membership. The empty value is not a member;
// an unset Temporal DERIVES TemporalIntentCurrent during normalization
// (§13.2.1's authorship table) rather than being validated as current.
func ValidTemporalIntent(value TemporalIntent) bool {
	for _, member := range temporalIntents {
		if member == value {
			return true
		}
	}
	return false
}

// AnswerEmphasis is the closed vocabulary of which ends of an established
// ordering the answer must speak to. Design §13.2.2, TWO members.
//
// EMPHASIS IS NOT AN OBLIGATION, and the design records why in as many
// words: the feedback listed positive/negative outliers in both places,
// which is one axis under two names. The direction of the fix is fixed by
// chris's ruling of 2026-08-31 12:42 PDT on the paraphrase-robustness
// ticket -- "expected = same investigation, same answer, nothing new
// built". If both-ends emphasis derived an additional obligation it would
// derive an additional requirement and therefore an additional fact read,
// so a paraphrase would build something new, which that ruling forbids.
// On an ordered result the two ends are two ends of ONE ranking, already
// read. Emphasis therefore ADDS NO FACT READ (behaviour change B3, whose
// gate is an equivalence test: emphasis present vs absent plans the
// identical requirement set).
type AnswerEmphasis string

const (
	// EmphasisPositiveOutliers: the answer must address the strong end.
	EmphasisPositiveOutliers AnswerEmphasis = "positive_outliers"
	// EmphasisNegativeOutliers: the answer must address the weak end.
	EmphasisNegativeOutliers AnswerEmphasis = "negative_outliers"
)

var answerEmphases = [...]AnswerEmphasis{
	EmphasisPositiveOutliers,
	EmphasisNegativeOutliers,
}

// AnswerEmphasisCount is two.
const AnswerEmphasisCount = len(answerEmphases)

// AnswerEmphasisVocabulary returns the closed vocabulary in design order.
func AnswerEmphasisVocabulary() [AnswerEmphasisCount]AnswerEmphasis {
	return answerEmphases
}

// ValidAnswerEmphasis reports membership. The empty value is not a member.
func ValidAnswerEmphasis(value AnswerEmphasis) bool {
	for _, member := range answerEmphases {
		if member == value {
			return true
		}
	}
	return false
}

// AnswerObligation is the closed vocabulary of what an answer must
// ESTABLISH. Design §13.2.2, THIRTEEN members.
//
// SERVER-DERIVED from the whole frame (§13.2.3). The model's own emission
// is admitted as WIDENING-ONLY: it may add a member from this vocabulary,
// it may never remove a derived one, and a model-widened obligation is
// `advisory` and can never degrade answer completeness (§13.2.4). The
// failure direction is what that buys: a spurious extra obligation costs
// an extra read and a visible unsatisfied outcome; a missing one silently
// changes the question.
//
// `count` and `allocation_breakdown` are here because the feedback's own
// list had no member for the count_or_aggregate or allocate_investment
// goals, so two of its eight goals would have derived no obligation and
// planned no evidence. `evidence` and `coverage` are answer-CONTRACT
// obligations (North Star check 11 -- the answer contract is richer than
// the prose), not fact kinds.
type AnswerObligation string

const (
	// ObligationState: the subject's current state.
	ObligationState AnswerObligation = "state"
	// ObligationCompletion: how much of the declared work is done.
	ObligationCompletion AnswerObligation = "completion"
	// ObligationReadiness: whether it is fit to release.
	ObligationReadiness AnswerObligation = "readiness"
	// ObligationHealth: the health reading behind the state.
	ObligationHealth AnswerObligation = "health"
	// ObligationPrincipalDrivers: what is driving the state -- never a
	// bare score (North Star check 8).
	ObligationPrincipalDrivers AnswerObligation = "principal_drivers"
	// ObligationRanking: an ordering over the member set. COMPUTED, not
	// read -- see AnswerObligationKind.
	ObligationRanking AnswerObligation = "ranking"
	// ObligationRemainingWork: what is left, and what blocks it.
	ObligationRemainingWork AnswerObligation = "remaining_work"
	// ObligationEvidence: the answer contract's evidence field.
	ObligationEvidence AnswerObligation = "evidence"
	// ObligationCoverage: the answer contract's coverage field.
	ObligationCoverage AnswerObligation = "coverage"
	// ObligationCount: a cardinality. COMPUTED, not read.
	ObligationCount AnswerObligation = "count"
	// ObligationAllocationBreakdown: where effort went, by category.
	ObligationAllocationBreakdown AnswerObligation = "allocation_breakdown"
	// ObligationTrendSeries: a series over the time axis.
	ObligationTrendSeries AnswerObligation = "trend_series"
	// ObligationPeriodDelta: the difference between two periods.
	ObligationPeriodDelta AnswerObligation = "period_delta"
)

var answerObligations = [...]AnswerObligation{
	ObligationState,
	ObligationCompletion,
	ObligationReadiness,
	ObligationHealth,
	ObligationPrincipalDrivers,
	ObligationRanking,
	ObligationRemainingWork,
	ObligationEvidence,
	ObligationCoverage,
	ObligationCount,
	ObligationAllocationBreakdown,
	ObligationTrendSeries,
	ObligationPeriodDelta,
}

// AnswerObligationCount is thirteen.
const AnswerObligationCount = len(answerObligations)

// AnswerObligationVocabulary returns the closed vocabulary in design
// order.
func AnswerObligationVocabulary() [AnswerObligationCount]AnswerObligation {
	return answerObligations
}

// ValidAnswerObligation reports membership. The empty value is not a
// member.
func ValidAnswerObligation(value AnswerObligation) bool {
	for _, member := range answerObligations {
		if member == value {
			return true
		}
	}
	return false
}

// AnswerObligationKind classifies HOW an obligation is satisfied. Design
// §13.2.3's kinds table, fixed there because the finalizer's executed
// trace against the real fact registry showed the frozen requirement
// layer mis-typed two of them (round 4, N3).
//
// The consequence that forced this classification into the vocabulary
// rather than leaving it to the requirement layer: the frozen skeleton
// modelled `ranking` as a READ with a required table shape no producer
// declares, so BAR question Q2's DEFINING obligation derived an empty
// fact-kind set -- unavailable by construction. `ranking` is computed by
// RankCohort over already-read facts; `count` is a cardinality over the
// discovered/scoped/grouped set. A computed obligation has NO FactKinds
// of its own and is unavailable only when its inputs are.
type AnswerObligationKind string

const (
	// ObligationKindRead is satisfied by fact reads whose kinds are
	// derived in S7b-ii (§13.15). NOT this slice's concern -- this
	// classification names WHICH obligations that layer must serve, and
	// deliberately stops there.
	ObligationKindRead AnswerObligationKind = "read"
	// ObligationKindComputed is satisfied by a named SERVER step over
	// already-read facts or over the resolved/discovered set.
	ObligationKindComputed AnswerObligationKind = "computed"
	// ObligationKindAnswerContract is satisfied by the answer contract
	// itself; no read.
	ObligationKindAnswerContract AnswerObligationKind = "answer_contract"
)

var answerObligationKinds = [...]AnswerObligationKind{
	ObligationKindRead,
	ObligationKindComputed,
	ObligationKindAnswerContract,
}

// AnswerObligationKindCount is three.
const AnswerObligationKindCount = len(answerObligationKinds)

// AnswerObligationKindVocabulary returns the closed vocabulary in design
// order.
//
// It exists because the kind reaches the WIRE on a plan requirement row, and
// a wire mirror can only be held equal to its domain in both directions if
// the domain publishes a list to compare against. Before this, the three
// constants had no closed list, so a fourth could have been added with
// nothing to notice that the mirror had not gained it.
func AnswerObligationKindVocabulary() [AnswerObligationKindCount]AnswerObligationKind {
	return answerObligationKinds
}

// ValidAnswerObligationKind reports membership; the empty value is not one.
func ValidAnswerObligationKind(value AnswerObligationKind) bool {
	for _, member := range answerObligationKinds {
		if member == value {
			return true
		}
	}
	return false
}

// obligationKinds is §13.2.3's kinds table, verbatim. Every member of
// AnswerObligationVocabulary has exactly one entry; the registry test
// asserts totality, because an obligation with no kind is precisely the
// hole that let `ranking` be planned as a read.
var obligationKinds = map[AnswerObligation]AnswerObligationKind{
	ObligationState:               ObligationKindRead,
	ObligationCompletion:          ObligationKindRead,
	ObligationReadiness:           ObligationKindRead,
	ObligationHealth:              ObligationKindRead,
	ObligationPrincipalDrivers:    ObligationKindRead,
	ObligationRemainingWork:       ObligationKindRead,
	ObligationAllocationBreakdown: ObligationKindRead,
	ObligationTrendSeries:         ObligationKindRead,
	ObligationPeriodDelta:         ObligationKindRead,

	ObligationRanking: ObligationKindComputed,
	ObligationCount:   ObligationKindComputed,

	ObligationEvidence: ObligationKindAnswerContract,
	ObligationCoverage: ObligationKindAnswerContract,
}

// KindOfObligation returns the obligation's kind and whether it is a
// vocabulary member. Total over AnswerObligationVocabulary.
func KindOfObligation(value AnswerObligation) (AnswerObligationKind, bool) {
	kind, ok := obligationKinds[value]
	return kind, ok
}

// ComputedObligationStep names the SERVER step that satisfies a computed
// obligation. §13.2.3 requires a computed obligation to name its step;
// "every computed obligation names its server step" is half of oracle O9,
// and naming them here is what lets S7b-ii assert it without inventing
// the mapping.
//
// Closed vocabulary, telemetry-safe: these are step names, never prose.
type ComputedObligationStep string

const (
	// ComputedStepRankCohort is RankCohort's five-signal ordering. It
	// consumes five NAMED FACT KINDS, one per signal family -- see
	// computedStepInputs, which declares them, and the note there on the
	// earlier claim that this step "depends on the read obligation
	// principal_drivers". It does not read an obligation.
	ComputedStepRankCohort ComputedObligationStep = "rank_cohort"
	// ComputedStepMembershipCardinality counts the discovered, scoped or
	// grouped member set. It consumes NO fact -- declared positively as
	// ComputedInputResolvedMemberSet rather than as an empty kinds list, so
	// that "reads nothing" cannot be read as "nothing declared yet".
	ComputedStepMembershipCardinality ComputedObligationStep = "membership_cardinality"
)

var computedObligationSteps = map[AnswerObligation]ComputedObligationStep{
	ObligationRanking: ComputedStepRankCohort,
	ObligationCount:   ComputedStepMembershipCardinality,
}

// computedObligationStepVocabulary is the closed step list, in design order.
//
// A fixed-length array rather than a slice for the reason the telemetry
// arrays give: a step added to the vocabulary changes the array length, and
// every read of it fails to compile rather than silently counting one fewer
// member.
var computedObligationStepVocabulary = [...]ComputedObligationStep{
	ComputedStepRankCohort,
	ComputedStepMembershipCardinality,
}

// ComputedObligationStepCount is two.
const ComputedObligationStepCount = len(computedObligationStepVocabulary)

// ComputedObligationStepVocabulary returns the closed step list.
func ComputedObligationStepVocabulary() [ComputedObligationStepCount]ComputedObligationStep {
	return computedObligationStepVocabulary
}

// ComputedStepInputClass says WHERE a computed step's inputs come from.
//
// THE AMENDMENT TO §13.2.3, AND WHY IT NEEDS A CLASS RATHER THAN JUST A LIST.
// §13.2.3 required a computed obligation to name its server STEP and stopped
// there, so a computed requirement row could say what would satisfy it and
// never what it consumes. The six-authority parity proof could therefore not
// rule that a lost fact kind was NOT an input of a computed step, and had to
// treat every loss on such a frame as possibly load-bearing -- which is why
// no authority was retirable on that evidence.
//
// A bare kinds list would not close it. The two live steps do not have the
// same SHAPE of input: rank_cohort consumes canonical facts, and
// membership_cardinality consumes the resolved member set and reads no fact
// at all. Spelling the second as "an empty kinds list" would make it read
// exactly like "nobody has declared this yet" -- the silent emptiness this
// whole seam exists to forbid, reproduced inside its own fix. The class makes
// "consumes no fact" a POSITIVE statement.
//
// Closed vocabulary, telemetry-safe: these are class names, never prose.
type ComputedStepInputClass string

const (
	// ComputedInputFactKinds: the step consumes already-read canonical
	// facts, of the kinds it names.
	ComputedInputFactKinds ComputedStepInputClass = "fact_kinds"
	// ComputedInputResolvedMemberSet: the step consumes the resolved member
	// set and reads no fact. A kind lost on a frame whose only computation
	// is of this class is therefore not an input of anything, which is what
	// lets the parity proof rule on the loss instead of assuming.
	ComputedInputResolvedMemberSet ComputedStepInputClass = "resolved_member_set"
)

var computedStepInputClasses = [...]ComputedStepInputClass{
	ComputedInputFactKinds,
	ComputedInputResolvedMemberSet,
}

// ComputedStepInputClassCount is two.
const ComputedStepInputClassCount = len(computedStepInputClasses)

// ComputedStepInputClassVocabulary returns the closed class list.
func ComputedStepInputClassVocabulary() [ComputedStepInputClassCount]ComputedStepInputClass {
	return computedStepInputClasses
}

// ValidComputedStepInputClass reports membership. The empty value is not a
// member, so an undeclared row and a declared one are distinguishable.
func ValidComputedStepInputClass(value ComputedStepInputClass) bool {
	for _, member := range computedStepInputClasses {
		if member == value {
			return true
		}
	}
	return false
}

// ComputedStepExecution says whether the server ACTUALLY RUNS a step, or
// merely names it.
//
// WHY THIS EXISTS, and it is the second half of the §13.2.3 amendment rather
// than an extra. Declaring that a step consumes no fact is a statement about
// THAT STEP. It is only evidence about the ANSWER if that step is the thing
// producing the answer. Where a computed obligation is named but nothing
// executes it, the value still reaches the user -- today, through the model's
// narration over whatever facts the plan happened to read (see
// status_shadow.go's `count` row: "a cardinality is carried in the answer
// text, not in a countable field"). Under that mechanism a loss of read facts
// CAN change the answer, so "the step consumes nothing" does not license
// retiring whatever caused those facts to be read.
//
// Found by an adversarial review round on this change, which is exactly the
// mirror of the rule this amendment already states in the other direction:
// declaring an INPUT is not planning a read, and declaring NO input is not
// proof the answer needs nothing.
//
// Closed vocabulary, telemetry-safe.
type ComputedStepExecution string

const (
	// ComputedStepServerExecuted: a server function computes this step, so
	// its declared inputs describe what the answer is actually built from.
	ComputedStepServerExecuted ComputedStepExecution = "server_executed"
	// ComputedStepDeclaredOnly: the step is named by the vocabulary but no
	// server code satisfies the obligation with it. The value reaches the
	// answer some other way, so this step's input declaration says nothing
	// about what the answer depends on.
	ComputedStepDeclaredOnly ComputedStepExecution = "declared_only"
)

var computedStepExecutions = [...]ComputedStepExecution{
	ComputedStepServerExecuted,
	ComputedStepDeclaredOnly,
}

// ComputedStepExecutionCount is two.
const ComputedStepExecutionCount = len(computedStepExecutions)

// ComputedStepExecutionVocabulary returns the closed execution list.
func ComputedStepExecutionVocabulary() [ComputedStepExecutionCount]ComputedStepExecution {
	return computedStepExecutions
}

// ValidComputedStepExecution reports membership; the empty value is not one,
// so "undeclared" stays distinguishable from either answer.
func ValidComputedStepExecution(value ComputedStepExecution) bool {
	for _, member := range computedStepExecutions {
		if member == value {
			return true
		}
	}
	return false
}

// ComputedStepInputs is what a computed step CONSUMES.
//
// FactKinds is non-empty exactly when Class is ComputedInputFactKinds; the
// registry test asserts both directions, because a fact-reading step with no
// kinds is an undeclared input wearing a declaration.
type ComputedStepInputs struct {
	Class     ComputedStepInputClass
	FactKinds []FactKind
	// Execution says whether a server function actually runs this step. It
	// is NOT derivable from Class or FactKinds -- a step that consumes
	// nothing and a step nobody executes look identical from the input side,
	// and conflating them is what would let an unexecuted step's "consumes
	// nothing" authorize a retirement.
	Execution ComputedStepExecution
}

// computedStepInputs is the declaration table.
//
// rank_cohort's kinds are cohortRankingFormulaKinds ITSELF, not a copy of it.
// That set is RankCohort's five signal families -- investmentMixSignal reads
// FactInvestment, healthRiskSignal FactHealth, deficiencySeveritySignal
// FactOperationalDeficiencies, readinessGapSignal FactReadiness,
// workloadPressureSignal FactWorkload -- and it is already named once in this
// package, where the engine's unconditional cohort injection reads it. Two
// hand-maintained copies of one formula's inputs would drift, and this
// package has paid for that shape more than once.
//
// A CORRECTION THIS TABLE RECORDS. ComputedStepRankCohort's own comment said
// it "depends on the read obligation principal_drivers". RankCohort reads no
// obligation: it reads five named fact kinds directly, and principal_drivers
// is served by a different and wider set. The declaration is built from what
// the step EXECUTES, and the docstring is corrected to match rather than the
// other way round.
var computedStepInputs = map[ComputedObligationStep]ComputedStepInputs{
	ComputedStepRankCohort: {
		Class:     ComputedInputFactKinds,
		FactKinds: cohortRankingFormulaKinds,
		// RankCohort is wired between ReadFacts and Synthesize (engine.go's
		// Investigate) and computes the ordering from already-read facts.
		Execution: ComputedStepServerExecuted,
	},
	ComputedStepMembershipCardinality: {
		Class: ComputedInputResolvedMemberSet,
		// NOTHING satisfies `count` with this step today. The name exists in
		// the vocabulary; no production call site computes a cardinality for
		// the obligation (cohortMemberCount serves BUDGET fitting, not the
		// answer), and status_shadow.go records `count` as unobserved
		// because "a cardinality is carried in the answer text, not in a
		// countable field". Declared honestly rather than aspirationally:
		// marking it executed would make this table assert something the
		// tree does not do, and the parity proof would retire authorities on
		// the strength of it.
		Execution: ComputedStepDeclaredOnly,
	},
}

// InputsForComputedStep returns what a computed step consumes, and whether
// the step is declared at all.
//
// The returned FactKinds is a COPY, sorted in fact-kind vocabulary order.
// Both matter: the backing array is cohortRankingFormulaKinds, which the
// ENGINE reads when it injects the cohort ranking requirements, so a caller
// that sorted the returned slice in place would reorder what production plans
// to read. Sorting here rather than at each call site means the requirement
// rows, the regenerated artifact and the telemetry histogram all see one
// order, which is what makes two runs of one frame diffable.
func InputsForComputedStep(step ComputedObligationStep) (ComputedStepInputs, bool) {
	declared, ok := computedStepInputs[step]
	if !ok {
		return ComputedStepInputs{}, false
	}
	return ComputedStepInputs{
		Class:     declared.Class,
		FactKinds: sortedFactKinds(declared.FactKinds),
		Execution: declared.Execution,
	}, true
}

// sortedFactKinds returns a deduplicated copy in fact-kind VOCABULARY order.
//
// Vocabulary order, not lexical: it is the order every other closed-vocabulary
// rendering in this package uses, and a kind renamed in a later contract
// revision must not silently reorder a persisted artifact.
func sortedFactKinds(kinds []FactKind) []FactKind {
	if len(kinds) == 0 {
		return nil
	}
	seen := make(map[FactKind]bool, len(kinds))
	out := make([]FactKind, 0, len(kinds))
	for _, member := range contractsv1.ContextFabricFactKindVocabulary() {
		for _, kind := range kinds {
			if kind == member && !seen[member] {
				seen[member] = true
				out = append(out, member)
			}
		}
	}
	// A kind outside the closed vocabulary would otherwise vanish here,
	// which would make a declaration error read as a shorter list. Append
	// any such kind in input order so the totality test can see it.
	for _, kind := range kinds {
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	return out
}

// factKindIndex resolves a kind's position in the closed vocabulary, for the
// telemetry histogram. The second return distinguishes "index 0" from "not a
// member", which a bare int cannot.
func factKindIndex(value FactKind) (int, bool) {
	for index, member := range contractsv1.ContextFabricFactKindVocabulary() {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

// StepForComputedObligation returns the server step for a computed
// obligation. The second return is false for any obligation that is not
// computed, which is what the registry test uses to assert the two tables
// agree rather than drifting apart.
func StepForComputedObligation(value AnswerObligation) (ComputedObligationStep, bool) {
	step, ok := computedObligationSteps[value]
	return step, ok
}

// ObligationRequiredness records WHO put an obligation in the set.
// SERVER-DERIVED, never emitted (§13.2.1's authorship table).
type ObligationRequiredness string

const (
	// RequirednessRequired: derived by the server from the frame. An
	// unsatisfied required obligation degrades answer completeness.
	RequirednessRequired ObligationRequiredness = "required"
	// RequirednessAdvisory: added by a model WIDENING. An unsatisfied
	// advisory obligation is reported and disclosed and NOTHING MORE --
	// it may not degrade answer completeness (§13.2.4 rule 1). That is
	// what keeps the failure direction safe in both directions.
	RequirednessAdvisory ObligationRequiredness = "advisory"
)

// SanitizeInvestigationGoals closes a raw goal SET against the
// vocabulary. Design §13.2.1: "Each member sanitized: an unknown string is
// DROPPED from the set, never an error."
//
// The result is DEDUPLICATED and returned in vocabulary order, because
// Goals is a SET: two frames whose goal lists differ only by order or by a
// repeat are the same frame, and a derivation that saw them as different
// would make the family a function of emission order. dropped counts what
// was discarded so an all-unknown emission is countable rather than
// silent -- that count is what distinguishes "the model emitted nothing"
// from "the model emitted only names we do not know", which invariant I15
// then fails on either way but telemetry must be able to tell apart.
func SanitizeInvestigationGoals(raw []string) (goals []InvestigationGoal, dropped int) {
	seen := make(map[InvestigationGoal]bool, len(raw))
	for _, value := range raw {
		candidate := InvestigationGoal(strings.TrimSpace(value))
		if candidate == "" {
			continue
		}
		if !ValidInvestigationGoal(candidate) {
			dropped++
			continue
		}
		seen[candidate] = true
	}
	goals = make([]InvestigationGoal, 0, len(seen))
	for _, member := range investigationGoals {
		if seen[member] {
			goals = append(goals, member)
		}
	}
	return goals, dropped
}

// SanitizeSubjectExpressionKind closes the union discriminator. An
// unrecognized value becomes unset plus unrecognized=true, on the same
// never-fail-the-interpretation rule as SanitizeQuestionFamily. An unset
// Kind then fails invariant I1, which is the honest outcome: a union with
// no discriminator is not a subject expression.
func SanitizeSubjectExpressionKind(raw string) (kind SubjectExpressionKind, unrecognized bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	candidate := SubjectExpressionKind(trimmed)
	if !ValidSubjectExpressionKind(candidate) {
		return "", true
	}
	return candidate, false
}

// SanitizeTemporalIntent closes the temporal axis. Unset is NOT an error
// and NOT unrecognized: §13.2.1 says an unset Temporal derives `current`,
// and that derivation happens in normalization, not here. Sanitization
// reports what the model said; normalization decides what the frame holds.
// Keeping those separate is what makes the A1/A2 split honest -- an A1
// invariant may only read what the model emitted.
func SanitizeTemporalIntent(raw string) (temporal TemporalIntent, unrecognized bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	candidate := TemporalIntent(trimmed)
	if !ValidTemporalIntent(candidate) {
		return "", true
	}
	return candidate, false
}

// SanitizeAnswerEmphasis closes the emphasis SET, deduplicated and in
// vocabulary order for the same set-not-list reason as the goals.
func SanitizeAnswerEmphasis(raw []string) (emphasis []AnswerEmphasis, dropped int) {
	seen := make(map[AnswerEmphasis]bool, len(raw))
	for _, value := range raw {
		candidate := AnswerEmphasis(strings.TrimSpace(value))
		if candidate == "" {
			continue
		}
		if !ValidAnswerEmphasis(candidate) {
			dropped++
			continue
		}
		seen[candidate] = true
	}
	emphasis = make([]AnswerEmphasis, 0, len(seen))
	for _, member := range answerEmphases {
		if seen[member] {
			emphasis = append(emphasis, member)
		}
	}
	return emphasis, dropped
}

// SanitizeHealthDimensions closes the dimension SET against the shipped
// nine (chaos4632_question_family_registry.go). Deduplicated, in published
// order.
//
// ADDITIVE-ONLY is a property of what Dimensions DOES (§13.2.3 table 3,
// §13.2.4 rule 2), not of this function: sanitization only bounds what a
// member can BE. The first revision of the design called Dimensions "a
// filter", which is a NARROWING power and strictly worse than the problem
// it was solving; law L1 killed it. Nothing here may ever remove a derived
// obligation.
func SanitizeHealthDimensions(raw []string) (dimensions []HealthDimension, dropped int) {
	seen := make(map[HealthDimension]bool, len(raw))
	for _, value := range raw {
		candidate := HealthDimension(strings.TrimSpace(value))
		if candidate == "" {
			continue
		}
		if !ValidHealthDimension(candidate) {
			dropped++
			continue
		}
		seen[candidate] = true
	}
	published := HealthDimensionVocabulary()
	dimensions = make([]HealthDimension, 0, len(seen))
	for _, member := range published {
		if seen[member] {
			dimensions = append(dimensions, member)
		}
	}
	return dimensions, dropped
}

// SanitizeAnswerObligations closes a MODEL-EMITTED obligation set. It
// exists only for the widening path (§13.2.4): the server derives the
// authoritative set, and anything the model emits is admitted as a
// WIDENING whose members are `advisory`.
//
// Deliberately NOT used to build the derived set. Derivation reads the
// frame's other axes (frame_obligations.go) and never a
// model-emitted obligation list, because an obligation set the model could
// author is an obligation set the model could NARROW, which is the exact
// failure §13.2.1 forbids.
func SanitizeAnswerObligations(raw []string) (obligations []AnswerObligation, dropped int) {
	seen := make(map[AnswerObligation]bool, len(raw))
	for _, value := range raw {
		candidate := AnswerObligation(strings.TrimSpace(value))
		if candidate == "" {
			continue
		}
		if !ValidAnswerObligation(candidate) {
			dropped++
			continue
		}
		seen[candidate] = true
	}
	obligations = make([]AnswerObligation, 0, len(seen))
	for _, member := range answerObligations {
		if seen[member] {
			obligations = append(obligations, member)
		}
	}
	return obligations, dropped
}

// SubjectTermMaxBytes bounds a free-string retrieval pointer -- a
// Named.Terms entry or a Scoped.AnchorTerms entry. It is deliberately the
// SAME bound SanitizeScopeAnchorTerm already applies, because these fields
// are the same thing: retrieval pointers, never values (§13.5.1).
const SubjectTermMaxBytes = ScopeAnchorTermMaxBytes

// SanitizeSubjectTerms trims, bounds and drops empty entries from a free-
// string term list, preserving ORDER.
//
// ORDER IS PRESERVED HERE AND NOWHERE ELSE IN THIS FILE, and the asymmetry
// is deliberate. Goals, Emphasis and Dimensions are SETS: order carries no
// meaning and normalizing it away is what stops two orderings of one
// question from being two frames. Terms are a LIST of retrieval pointers
// handed to the graph in the order the user named them, and reordering
// them would change which candidate a tie resolves to.
//
// Truncation over rejection, for the reason SanitizeScopeAnchorTerm gives:
// this runs after the interpretation has already validated, and a capture
// must never become a new way for a sound interpretation to fail.
// Truncation is counted so it stays countable rather than silent.
func SanitizeSubjectTerms(raw []string) (terms []string, truncated int) {
	terms = make([]string, 0, len(raw))
	for _, value := range raw {
		trimmed, cut := SanitizeScopeAnchorTerm(value)
		if trimmed == "" {
			continue
		}
		if cut {
			truncated++
		}
		terms = append(terms, trimmed)
	}
	return terms, truncated
}

// SanitizeSubjectKind closes a member/group/anchor kind against the
// EXISTING ContextFabricSubjectKind registry rather than inventing a
// parallel vocabulary -- the same decision SanitizeGroupKind already made
// for S2, and for the same reason: a second subject-kind vocabulary is a
// second authority, which law L6 bans.
func SanitizeSubjectKind(raw string) (kind SubjectKind, unrecognized bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	candidate := SubjectKind(trimmed)
	if !contractsv1.ValidContextFabricSubjectKind(candidate) {
		return "", true
	}
	return candidate, false
}

// sortedObligations returns a copy in vocabulary order. Obligation sets
// are SETS; every derivation and every comparison in this slice goes
// through here so that set equality is decidable by slice equality and an
// oracle can assert an EXACT set (O1) rather than a subset.
func sortedObligations(in []AnswerObligation) []AnswerObligation {
	index := make(map[AnswerObligation]int, AnswerObligationCount)
	for position, member := range answerObligations {
		index[member] = position
	}
	out := make([]AnswerObligation, 0, len(in))
	seen := make(map[AnswerObligation]bool, len(in))
	for _, member := range in {
		if seen[member] {
			continue
		}
		seen[member] = true
		out = append(out, member)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return index[out[i]] < index[out[j]]
	})
	return out
}
