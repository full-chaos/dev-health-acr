package v1

import (
	"fmt"
	"strings"
)

// The outcome layer: what happened to each thing the answer was supposed to
// contain, and what the reader loses where it did not happen.
//
// The problem it solves is stated by the block it extends.
// ContextFabricAnswerCompleteness is a CENSUS -- a status, a pointer at which
// disclosure channel fired, and two counts. A reader learns that something
// degraded and which channel said so. They do not learn what the answer was
// supposed to contain and no longer does, because the census never had a
// requirement to name.
//
// An outcome names it, per requirement, from closed vocabularies. Two
// invariants make that claim true of the document a caller actually receives
// rather than of an intermediate one:
//
//	APPEND. Every narrowing stage between planning and the served document
//	APPENDS outcome rows. No stage rewrites or removes a row another stage
//	wrote.
//
//	DERIVE LAST. Completeness is a pure function of the whole outcome set,
//	computed at the surface that serves the answer.
//
// Together they forbid the failure this layer exists to prevent: measuring
// completeness and then shrinking the document somewhere the measurement
// cannot see. The shrink is itself an outcome, appended before completeness
// is computed, so nothing is decided before the quantity it depends on
// exists.

// ContextFabricPlanRequirementOutcome says what happened to ONE requirement.
// CLOSED, five members.
type ContextFabricPlanRequirementOutcome string

const (
	// ContextFabricRequirementSatisfied: served in full, at the declared
	// scope and quantifier.
	//
	// It is an EXPLICIT member rather than the zero value's meaning so
	// that the satisfied path stays representable and therefore testable:
	// a vocabulary that omits the token cannot assert its absence.
	ContextFabricRequirementSatisfied ContextFabricPlanRequirementOutcome = "satisfied"
	// ContextFabricRequirementNarrowed: served, but over a REDUCED
	// subject, member or window set than the requirement declared.
	ContextFabricRequirementNarrowed ContextFabricPlanRequirementOutcome = "narrowed"
	// ContextFabricRequirementUnavailable: could not be served at all, and
	// nothing stands in for it.
	ContextFabricRequirementUnavailable ContextFabricPlanRequirementOutcome = "unavailable"
	// ContextFabricRequirementNotApplicable: the question did not ask for
	// it; the row exists only because a wider derivation produced the
	// coordinate.
	//
	// This member is what lets an omittable field ship populated at all.
	// Without it an absent field carries two meanings -- "not produced
	// because the question did not ask" and "asked for but unavailable" --
	// and one token standing for both is the shape "missing is not
	// healthy" forbids.
	ContextFabricRequirementNotApplicable ContextFabricPlanRequirementOutcome = "not_applicable"
	// ContextFabricRequirementNotAttempted: a declared cap prevented the
	// attempt BEFORE any read.
	//
	// Distinct from both neighbours, and the distinction is not
	// decorative. Reporting a never-attempted read as `unavailable` would
	// be false -- nothing was tried, so nothing was found missing -- and
	// reporting it as `narrowed` would be false too, because nothing was
	// served.
	ContextFabricRequirementNotAttempted ContextFabricPlanRequirementOutcome = "not_attempted"
)

var contextFabricPlanRequirementOutcomes = [...]ContextFabricPlanRequirementOutcome{
	ContextFabricRequirementSatisfied,
	ContextFabricRequirementNarrowed,
	ContextFabricRequirementUnavailable,
	ContextFabricRequirementNotApplicable,
	ContextFabricRequirementNotAttempted,
}

// ContextFabricPlanRequirementOutcomeCount is the vocabulary size as a
// compile-time constant.
const ContextFabricPlanRequirementOutcomeCount = len(contextFabricPlanRequirementOutcomes)

// ContextFabricPlanRequirementOutcomeVocabulary returns the closed
// vocabulary in published order. An ARRAY return, so the caller gets a copy.
func ContextFabricPlanRequirementOutcomeVocabulary() [ContextFabricPlanRequirementOutcomeCount]ContextFabricPlanRequirementOutcome {
	return contextFabricPlanRequirementOutcomes
}

// ValidContextFabricPlanRequirementOutcome reports membership.
func ValidContextFabricPlanRequirementOutcome(value ContextFabricPlanRequirementOutcome) bool {
	for _, member := range contextFabricPlanRequirementOutcomes {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricOutcomeStage says WHICH stage produced an outcome row.
// CLOSED, four members.
//
// It is its own vocabulary rather than a reuse of
// ContextFabricPlanNarrowingStage, and the reason is a shape rule this layer
// is held to elsewhere: that vocabulary names the three points at which the
// COHORT is narrowed, and it has no member for planning and none for the
// projection. Borrowing it would have forced every row this layer writes to
// claim a stage it did not come from -- a vocabulary NARROWER than its
// producer, which fails the closure test for the same reason one wider than
// its producer does. Widening the shipped vocabulary instead was refused: it
// is consumed as a closed union by a pinned client, and it would have gained
// members that mean nothing to the narrowing steps that already use it.
type ContextFabricOutcomeStage string

const (
	// ContextFabricOutcomeStagePlanning: the row was seeded when this
	// turn's requirements were derived, before any narrowing.
	ContextFabricOutcomeStagePlanning ContextFabricOutcomeStage = "planning"
	// ContextFabricOutcomeStageAssembledResult: the row was appended when
	// the assembled result was measured against the response budget.
	ContextFabricOutcomeStageAssembledResult ContextFabricOutcomeStage = "assembled_result"
	// ContextFabricOutcomeStageProjection: the row was appended when the
	// answer was projected onto a caller's own budget.
	//
	// This member is why the set must be re-derived at the serving
	// surface rather than copied: the projection cuts AFTER the canonical
	// completeness would otherwise have been computed, and a copied state
	// would describe a document the caller never receives.
	ContextFabricOutcomeStageProjection ContextFabricOutcomeStage = "projection"
	// ContextFabricOutcomeStageReuse: the row was appended when a REUSED
	// answer was degraded because evidence it carried is no longer visible
	// to this caller.
	//
	// This member exists because the reuse degrade is a narrowing stage
	// between planning and the served document, and the APPEND invariant
	// applies to every such stage. It strips evidence references and, when
	// stripping empties an object the contract requires to carry evidence,
	// drops whole candidates, cohort members, drivers, findings and paths.
	// Without a row for it, a stored answer whose requirements all read
	// `satisfied` served a genuinely smaller document while still claiming
	// `complete` -- the same measure-then-shrink defect this layer exists
	// to forbid, on the one surface the layer originally did not cover.
	//
	// It is a WORSE instance than the assembly case, which is why it gets
	// its own member rather than borrowing `assembled_result`: assembly at
	// least refused, and this path serves. A reader must be able to tell
	// which surface cut the answer, because the two have different causes
	// and different remedies -- a budget the caller can widen, versus an
	// authorization that changed underneath a cached answer.
	ContextFabricOutcomeStageReuse ContextFabricOutcomeStage = "reuse"
)

var contextFabricOutcomeStages = [...]ContextFabricOutcomeStage{
	ContextFabricOutcomeStagePlanning,
	ContextFabricOutcomeStageAssembledResult,
	ContextFabricOutcomeStageProjection,
	ContextFabricOutcomeStageReuse,
}

// ContextFabricOutcomeStageCount is the vocabulary size.
const ContextFabricOutcomeStageCount = len(contextFabricOutcomeStages)

// ContextFabricOutcomeStageVocabulary returns the closed vocabulary in
// published order.
func ContextFabricOutcomeStageVocabulary() [ContextFabricOutcomeStageCount]ContextFabricOutcomeStage {
	return contextFabricOutcomeStages
}

// ValidContextFabricOutcomeStage reports membership.
func ValidContextFabricOutcomeStage(value ContextFabricOutcomeStage) bool {
	for _, member := range contextFabricOutcomeStages {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricAnswerImpactKind says what the READER loses when a row is not
// satisfied. It is the reader-facing half; the outcome is the engine-facing
// half. CLOSED, four members.
type ContextFabricAnswerImpactKind string

const (
	// ContextFabricAnswerImpactNone: nothing was lost. Legal only
	// alongside `satisfied` or `not_applicable`.
	ContextFabricAnswerImpactNone ContextFabricAnswerImpactKind = "none"
	// ContextFabricAnswerImpactScope: fewer subjects or members reached
	// the reader than the question named.
	ContextFabricAnswerImpactScope ContextFabricAnswerImpactKind = "scope"
	// ContextFabricAnswerImpactDepth: the named subjects, with less
	// evidence per subject.
	ContextFabricAnswerImpactDepth ContextFabricAnswerImpactKind = "depth"
	// ContextFabricAnswerImpactDimension: a whole obligation is absent --
	// a driver family, a trend, a rank.
	ContextFabricAnswerImpactDimension ContextFabricAnswerImpactKind = "dimension"
)

var contextFabricAnswerImpactKinds = [...]ContextFabricAnswerImpactKind{
	ContextFabricAnswerImpactNone,
	ContextFabricAnswerImpactScope,
	ContextFabricAnswerImpactDepth,
	ContextFabricAnswerImpactDimension,
}

// ContextFabricAnswerImpactKindCount is the vocabulary size.
const ContextFabricAnswerImpactKindCount = len(contextFabricAnswerImpactKinds)

// ContextFabricAnswerImpactKindVocabulary returns the closed vocabulary in
// published order.
func ContextFabricAnswerImpactKindVocabulary() [ContextFabricAnswerImpactKindCount]ContextFabricAnswerImpactKind {
	return contextFabricAnswerImpactKinds
}

// ValidContextFabricAnswerImpactKind reports membership.
func ValidContextFabricAnswerImpactKind(value ContextFabricAnswerImpactKind) bool {
	for _, member := range contextFabricAnswerImpactKinds {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricAnswerCompletenessState is what the outcome set adds up to.
// CLOSED, four members.
type ContextFabricAnswerCompletenessState string

const (
	// ContextFabricAnswerCompletenessNotDerived: NO outcome rows exist for
	// this answer.
	//
	// This member exists because the alternative is a lie. An empty
	// outcome set derives "complete" under any total function over the
	// other three -- vacuously, every row being satisfied -- and that
	// would let an answer whose outcomes were never derived claim the
	// strongest completeness there is. "We did not derive this" and "we
	// derived it and nothing was lost" are different states and must not
	// share a token.
	ContextFabricAnswerCompletenessNotDerived ContextFabricAnswerCompletenessState = "not_derived"
	// ContextFabricAnswerCompletenessComplete: every row is satisfied or
	// not_applicable.
	ContextFabricAnswerCompletenessComplete ContextFabricAnswerCompletenessState = "complete"
	// ContextFabricAnswerCompletenessPartial: at least one row is narrowed
	// or not_attempted, and none is unavailable.
	ContextFabricAnswerCompletenessPartial ContextFabricAnswerCompletenessState = "partial"
	// ContextFabricAnswerCompletenessDegraded: at least one row is
	// unavailable.
	ContextFabricAnswerCompletenessDegraded ContextFabricAnswerCompletenessState = "degraded"
)

var contextFabricAnswerCompletenessStates = [...]ContextFabricAnswerCompletenessState{
	ContextFabricAnswerCompletenessNotDerived,
	ContextFabricAnswerCompletenessComplete,
	ContextFabricAnswerCompletenessPartial,
	ContextFabricAnswerCompletenessDegraded,
}

// ContextFabricAnswerCompletenessStateCount is the vocabulary size.
const ContextFabricAnswerCompletenessStateCount = len(contextFabricAnswerCompletenessStates)

// ContextFabricAnswerCompletenessStateVocabulary returns the closed
// vocabulary in published order.
func ContextFabricAnswerCompletenessStateVocabulary() [ContextFabricAnswerCompletenessStateCount]ContextFabricAnswerCompletenessState {
	return contextFabricAnswerCompletenessStates
}

// ValidContextFabricAnswerCompletenessState reports membership.
func ValidContextFabricAnswerCompletenessState(value ContextFabricAnswerCompletenessState) bool {
	for _, member := range contextFabricAnswerCompletenessStates {
		if member == value {
			return true
		}
	}
	return false
}

// contextFabricAnswerObligations MIRRORS internal/contextfabric's own closed
// AnswerObligation vocabulary onto the wire.
//
// It is a mirror, not an import: contextfabric already imports this package,
// so the dependency cannot run the other way. The mirror is held honest the
// way the three fact-scope vocabularies above it are -- internal/contextfabric
// carries a parity test asserting the two sets are EQUAL IN BOTH DIRECTIONS,
// so a new obligation cannot ship without its wire mirror and a mirror entry
// cannot outlive its domain member.
//
// It exists because a length bound is not a value domain. Without it,
// `obligation` would be a permitted key holding a token-shaped string from no
// vocabulary at all -- which is exactly the shape a closed-vocabulary gate is
// written to prevent, and exactly the shape that has defeated one before.
var contextFabricAnswerObligations = [...]string{
	"state", "completion", "readiness", "health", "principal_drivers",
	"ranking", "remaining_work", "evidence", ContextFabricAnswerObligationCoverage,
	ContextFabricAnswerObligationCount,
	"allocation_breakdown", "trend_series", "period_delta",
}

// ContextFabricAnswerObligationCount and ContextFabricAnswerObligationCoverage
// are the two members of the obligation mirror that RULES elsewhere in this
// file have to name.
//
// They exist so a rule can be written against the vocabulary instead of
// against a string literal that happens to match it. A literal in a guard is
// a second, silent copy of the vocabulary: it does not move when the mirror
// moves, nothing relates the two, and the parity test that keeps the mirror
// honest cannot see it. The array above is therefore written in terms of
// these, not the other way round -- a constant the vocabulary does not
// contain would not compile into it.
const (
	ContextFabricAnswerObligationCoverage = "coverage"
	ContextFabricAnswerObligationCount    = "count"
)

// ContextFabricAnswerObligationVocabulary returns the mirrored vocabulary,
// for the domain-side parity test.
func ContextFabricAnswerObligationVocabulary() [len(contextFabricAnswerObligations)]string {
	return contextFabricAnswerObligations
}

// ValidContextFabricAnswerObligation reports membership in the mirror.
func ValidContextFabricAnswerObligation(value string) bool {
	return stringInVocabulary(value, contextFabricAnswerObligations[:])
}

// contextFabricRequirementIdentitySegments is how many "/"-separated parts a
// requirement identity has: the obligation, the subject role, and the subject
// kind -- the coordinate the derivation is itself keyed on.
const contextFabricRequirementIdentitySegments = 3

// Bounds. ContextFabricPlanRequirementOutcomeMaxCount is generous against the
// derivation's own measured size (a live frame derives on the order of a few
// dozen requirement cells) plus the stages that append to it, and exists so a
// malformed producer cannot make a document unbounded.
const (
	ContextFabricPlanRequirementOutcomeMaxCount = 200
	ContextFabricRequirementIdentityMaxLength   = 256
	ContextFabricRequirementObligationMaxLength = 64
)

// ContextFabricPlanRequirementOutcomeRow is ONE outcome.
//
// Everything on it is a closed token or an integer. That is deliberate and
// it is the constraint that keeps this from becoming a third free-text
// channel: Coverage.DegradedReasons and Limitations are already arbitrary,
// dynamically formatted or model-authored text, and a row carrying a string
// cause would be a third such channel wearing a closed vocabulary's name.
type ContextFabricPlanRequirementOutcomeRow struct {
	// Stage names WHICH stage produced this row. REQUIRED on every row,
	// including a seed row, so the append invariant is legible from the
	// document: a reader can see which stage each row came from, and
	// therefore that later stages added rather than rewrote.
	Stage ContextFabricOutcomeStage `json:"stage"`
	// Requirement is the requirement row's own identity, when the
	// reduction can be attributed to one. It is EMPTY when the served
	// document was narrowed on a turn for which no requirement rows were
	// derived -- an honest state, not a defect, and one the reader can
	// tell apart because the completeness state then says `not_derived`.
	//
	// It is not re-derived here. It is carried from the derivation that
	// owns it.
	Requirement string `json:"requirement,omitempty"`
	// Obligation is the requirement's obligation, copied so a reader of
	// the row alone knows what was at stake. Empty exactly when
	// Requirement is empty.
	Obligation string `json:"obligation,omitempty"`
	// Outcome and Impact are the two halves: what happened, and what the
	// reader loses. Their legal pairings are enforced by
	// ValidateContextFabricPlanRequirementOutcomeRow, not by care.
	Outcome ContextFabricPlanRequirementOutcome `json:"outcome"`
	Impact  ContextFabricAnswerImpactKind       `json:"impact"`
	// The three CAUSE fields name WHICH mechanism produced a non-satisfied
	// outcome, each from a vocabulary that ALREADY SHIPS. No new cause
	// enum is minted and no shipped one is widened: minting a second
	// vocabulary for events these already name would create two
	// authorities for one fact.
	//
	// CauseOverrun is the declared ceiling that forced the reduction.
	// CauseCoverage is a coverage event that caused it. CauseNarrowing is
	// the selection order that chose the survivors. A non-satisfied row
	// carries at least one of them.
	CauseOverrun   ContextFabricBudgetOverrun      `json:"cause_overrun,omitempty"`
	CauseCoverage  ContextFabricCoverageDetailCode `json:"cause_coverage,omitempty"`
	CauseNarrowing ContextFabricNarrowingBasis     `json:"cause_narrowing,omitempty"`
	// CauseObserved distinguishes a cause a mechanism REPORTED from one
	// the assembly layer DEFAULTED to.
	//
	// Without it a defaulted cause reads as an observed one, and the
	// grouped-narrowing disclosure that shipped one layer down is the
	// proof that assumption is unsafe: a reader must otherwise assume the
	// named mechanism ran.
	CauseObserved bool `json:"cause_observed"`
	// Served and Declared are the NUMBERS, so the row is diagnosable from
	// itself rather than by joining it against telemetry.
	Served   int `json:"served"`
	Declared int `json:"declared"`
	// Refinements are the reduction STEPS behind those two numbers, in the
	// order they were taken.
	//
	// Served and Declared are a before and an after with everything between
	// them erased. Two rows reading 3 of 10 are indistinguishable whether
	// one stage cut seven or three stages cut two, three and two -- and
	// "which rows were refined and WHEN" is precisely what a reader of the
	// artifact alone cannot otherwise reconstruct.
	//
	// The shape is the plan's own narrowing record: which stage acted, on
	// what declared basis, and from what count to what count. It is the
	// same disclosure one level down, per requirement instead of per plan.
	//
	// APPEND-ONLY, like the rows themselves. A stage that reduces this
	// requirement appends a refinement; no stage rewrites one another
	// stage wrote. That is what makes the arithmetic checkable rather than
	// asserted: the last refinement's After must equal Served, so a stage
	// that shrank the document without recording it leaves a gap its own
	// validator names.
	Refinements []ContextFabricRequirementRefinement `json:"refinements,omitempty"`
}

// ContextFabricRequirementRefinementMaxCount bounds how many refinement steps
// one requirement may record.
//
// It is the outcome-stage vocabulary's size rather than a number chosen for
// it: a refinement is appended by a stage, no stage appends twice to one
// requirement, so a row cannot honestly record more refinements than there
// are stages able to make one.
const ContextFabricRequirementRefinementMaxCount = ContextFabricOutcomeStageCount

// ContextFabricRequirementRefinement is ONE recorded reduction of one
// requirement: which stage narrowed it, on what basis, and from what to what.
//
// It is modelled on ContextFabricPlanNarrowing, deliberately and not
// incidentally -- that type is the established shape for "a narrowing that
// happened, disclosed" and a second shape for the same statement would be a
// second thing to learn.
//
// It does NOT reuse ContextFabricPlanNarrowingStage. That vocabulary names
// the three points at which the COHORT is narrowed and has no member for
// planning, the projection or a reuse degrade; borrowing it would force a
// refinement appended by the projection to claim a stage it did not come
// from. The outcome stage vocabulary is the one that already names every
// stage able to append here, and it is the same vocabulary the enclosing row
// stamps -- so a refinement and the row that carries it speak one language.
type ContextFabricRequirementRefinement struct {
	// Stage is which stage took this step. Its vocabulary is the enclosing
	// row's own.
	Stage ContextFabricOutcomeStage `json:"stage"`
	// Basis is the declared selection order that chose the survivors, and
	// Overrun is the declared ceiling that forced the reduction. AT LEAST
	// ONE, never neither -- and Basis is OPTIONAL, which is the correction
	// that made this type producible at all.
	//
	// The first revision required a Basis. That made the record
	// unproducible at the only site in this service that actually reduces
	// anything: candidateNarrowingOutcomeRow truncates a candidate list at
	// its own declared order and its own header refuses to name a selection
	// basis, because "naming a selection basis here would state that an
	// order chose the survivors when a ceiling did". A required Basis
	// therefore demanded the one cause that site will not invent, and the
	// field shipped with nothing able to write it.
	//
	// The shape now mirrors the enclosing row's cause model in FULL --
	// ordering, ceiling and coverage, at least one required -- rather than
	// insisting on a particular kind of cause.
	//
	// All THREE, because a sweep of every reducing site found two causes was
	// still not enough: the reuse degrade names a COVERAGE code and no
	// ceiling and no ordering, so a two-cause refinement could not represent
	// it either. That would have been the same defect a second time, fixed
	// per instance instead of as a class. The vocabularies are the ones the
	// row already uses; nothing is re-declared here.
	Basis    ContextFabricNarrowingBasis     `json:"basis,omitempty"`
	Overrun  ContextFabricBudgetOverrun      `json:"overrun,omitempty"`
	Coverage ContextFabricCoverageDetailCode `json:"coverage,omitempty"`
	// Before and After are counts of the thing that was narrowed.
	Before int `json:"before"`
	After  int `json:"after"`
}

// Validate enforces the two closed vocabularies and the count arithmetic.
func (r ContextFabricRequirementRefinement) Validate() error {
	if !ValidContextFabricOutcomeStage(r.Stage) {
		return fmt.Errorf("refinement stage %q is not a vocabulary member", r.Stage)
	}
	if r.Basis != "" && !ValidContextFabricNarrowingBasis(r.Basis) {
		return fmt.Errorf("refinement basis %q is not a vocabulary member", r.Basis)
	}
	if r.Overrun != "" && !ValidContextFabricBudgetOverrun(r.Overrun) {
		return fmt.Errorf("refinement overrun %q is not a vocabulary member", r.Overrun)
	}
	if r.Coverage != "" && !validCoverageDetailCode(r.Coverage) {
		return fmt.Errorf("refinement coverage %q is not a vocabulary member", r.Coverage)
	}
	// A reduction with no named cause is the generic truncation this whole
	// layer exists to replace, moved one level down. Both directions of the
	// enclosing row's own cause rule, minus the "must name none when
	// lossless" half, which cannot arise here: a refinement that reduced
	// nothing is already refused below.
	if r.Basis == "" && r.Overrun == "" && r.Coverage == "" {
		return fmt.Errorf("refinement at stage %q names no cause; a reduction must say what forced it", r.Stage)
	}
	if r.Before < 0 || r.After < 0 || r.After > r.Before {
		return fmt.Errorf("refinement must reduce a non-negative count, got before=%d after=%d", r.Before, r.After)
	}
	// A step that reduced nothing is not a refinement. Recording one would
	// put a stage's name against a reduction it did not make, which is the
	// same false attribution CauseObserved exists to prevent one field up.
	if r.Before == r.After {
		return fmt.Errorf("refinement at stage %q records before=after=%d, which is not a reduction", r.Stage, r.Before)
	}
	return nil
}

// DeriveContextFabricAnswerCompletenessState is THE derivation. It is total:
// every legal outcome multiset returns a value, and there is no arm that
// falls through to a default.
//
// It is a pure function of the rows and of nothing else. That is what makes
// the outcome set the single authority: any surface that serves an answer
// calls this over the rows it holds, and two surfaces holding the same rows
// cannot disagree.
func DeriveContextFabricAnswerCompletenessState(rows []ContextFabricPlanRequirementOutcomeRow) ContextFabricAnswerCompletenessState {
	if len(rows) == 0 {
		return ContextFabricAnswerCompletenessNotDerived
	}
	state := ContextFabricAnswerCompletenessComplete
	for _, row := range rows {
		switch row.Outcome {
		case ContextFabricRequirementUnavailable:
			// Degraded is absorbing: one unavailable requirement is the
			// strongest thing the set can say, so no later row can walk
			// it back.
			return ContextFabricAnswerCompletenessDegraded
		case ContextFabricRequirementNarrowed, ContextFabricRequirementNotAttempted:
			state = ContextFabricAnswerCompletenessPartial
		case ContextFabricRequirementSatisfied, ContextFabricRequirementNotApplicable:
			// Contributes nothing; the running state stands.
		}
	}
	return state
}

// ValidateContextFabricPlanRequirementOutcomeRow is the PER-FIELD predicate.
//
// It is written as a predicate over every field rather than as a membership
// check over names, and that is the point rather than a style preference. A
// vocabulary is closed over its MEMBERS; the domain of what carries it is a
// separate question, and left unstated it is open by default. A gate that
// checks only which keys are present accepts a permitted key holding a
// token-shaped value from no vocabulary at all.
func ValidateContextFabricPlanRequirementOutcomeRow(row ContextFabricPlanRequirementOutcomeRow) error {
	if !ValidContextFabricOutcomeStage(row.Stage) {
		return fmt.Errorf("outcome row stage %q is not a vocabulary member", row.Stage)
	}
	if !ValidContextFabricPlanRequirementOutcome(row.Outcome) {
		return fmt.Errorf("outcome %q is not a vocabulary member", row.Outcome)
	}
	if !ValidContextFabricAnswerImpactKind(row.Impact) {
		return fmt.Errorf("impact %q is not a vocabulary member", row.Impact)
	}
	if !stringLengthBetween(row.Requirement, 0, ContextFabricRequirementIdentityMaxLength) ||
		!stringLengthBetween(row.Obligation, 0, ContextFabricRequirementObligationMaxLength) {
		return fmt.Errorf("outcome row requirement or obligation violates v1 bounds")
	}
	// An obligation without the requirement it belongs to, or the reverse,
	// is a half-attributed row: it names a thing at stake without saying
	// which requirement held it, or names a requirement whose stake is
	// unstated. Neither is diagnosable, so neither is legal.
	if (row.Requirement == "") != (row.Obligation == "") {
		return fmt.Errorf("outcome row requirement and obligation must be present or absent together")
	}
	if row.Obligation != "" {
		// The VALUE DOMAIN, not just the length. A vocabulary is closed
		// over its members; what may carry it is a separate question, and
		// left unstated it is open by default.
		if !ValidContextFabricAnswerObligation(row.Obligation) {
			return fmt.Errorf("outcome row obligation %q is not a vocabulary member", row.Obligation)
		}
		// The identity is the COORDINATE -- obligation/role/subject kind --
		// so its first segment must be the obligation the row already
		// names. A row whose identity disagreed with its own obligation
		// would give a reader two answers to "which requirement is this",
		// which is the same defect the completeness/outcome agreement
		// check above exists to prevent one level up.
		segments := strings.Split(row.Requirement, "/")
		if len(segments) != contextFabricRequirementIdentitySegments {
			return fmt.Errorf("outcome row requirement %q is not an obligation/role/subject coordinate", row.Requirement)
		}
		if segments[0] != row.Obligation {
			return fmt.Errorf("outcome row requirement %q does not begin with its own obligation %q", row.Requirement, row.Obligation)
		}
		for index, segment := range segments {
			if segment == "" {
				return fmt.Errorf("outcome row requirement %q has an empty segment at position %d", row.Requirement, index)
			}
		}
	}
	// THE PAIRING RULE. `none` means nothing was lost, so it is legal
	// exactly where nothing was: a row whose outcome is `narrowed` and
	// whose impact is `none` is a validation failure, not a tidy default.
	lossless := row.Outcome == ContextFabricRequirementSatisfied || row.Outcome == ContextFabricRequirementNotApplicable
	if lossless != (row.Impact == ContextFabricAnswerImpactNone) {
		return fmt.Errorf("outcome %q and impact %q are not a legal pairing", row.Outcome, row.Impact)
	}
	if row.CauseOverrun != "" && !ValidContextFabricBudgetOverrun(row.CauseOverrun) {
		return fmt.Errorf("outcome row cause_overrun %q is not a vocabulary member", row.CauseOverrun)
	}
	if row.CauseCoverage != "" && !validCoverageDetailCode(row.CauseCoverage) {
		return fmt.Errorf("outcome row cause_coverage %q is not a vocabulary member", row.CauseCoverage)
	}
	if row.CauseNarrowing != "" && !ValidContextFabricNarrowingBasis(row.CauseNarrowing) {
		return fmt.Errorf("outcome row cause_narrowing %q is not a vocabulary member", row.CauseNarrowing)
	}
	named := row.CauseOverrun != "" || row.CauseCoverage != "" || row.CauseNarrowing != ""
	// A loss with no named cause is the generic truncation bit this layer
	// exists to replace, and a cause named on a row that lost nothing
	// states that a mechanism fired when none did. Both directions.
	if lossless == named {
		if lossless {
			return fmt.Errorf("outcome %q lost nothing and must name no cause", row.Outcome)
		}
		return fmt.Errorf("outcome %q must name a cause from a closed vocabulary", row.Outcome)
	}
	// CauseObserved says whether the cause was REPORTED or DEFAULTED. It
	// describes a cause, so it is meaningless -- and misleading -- on a
	// row that names none.
	if lossless && row.CauseObserved {
		return fmt.Errorf("outcome %q names no cause and must not claim an observed one", row.Outcome)
	}
	if row.Served < 0 || row.Declared < 0 || row.Served > row.Declared {
		return fmt.Errorf("outcome row served/declared %d/%d violates v1 bounds", row.Served, row.Declared)
	}
	// `narrowed` means SERVED, over a reduced set. A row claiming a
	// narrowing that served everything it declared narrowed nothing, and a
	// row that served none of it is not narrowed -- it is unavailable.
	//
	// THE ONE EXCEPTION, and it is narrow by construction. A value computed
	// over a population the answer did not see all of loses something on an
	// axis these two numbers do not measure: the count over the RESOLVED set
	// is exact, so served == declared is the truthful pair, while the answer
	// is still not the census the question asked for. Before this, that
	// state had no legal row shape at all -- so the honest outcome could not
	// be written and a false `satisfied` stood by default, which is the
	// defect the exception exists to remove.
	//
	// Three conjuncts, each carrying its own weight:
	//
	//	served == declared -- and not `>=`. Serving MORE than was declared is
	//	  refused by its own bound above, and writing `==` here means this
	//	  exception can never become the thing that lets such a row through.
	//	CauseObserved -- a DEFAULTED cause would let any producer opt out of
	//	  the reduction rule by naming a code it never measured, which is the
	//	  assumption CauseObserved was added to make unsafe.
	//	a population-qualifying code -- an allow-list, so a coverage code
	//	  about a fact READ can never license equal counts.
	//	the row class itself -- stage, obligation and impact. The three
	//	  conjuncts above say the row is HONEST; these three say the
	//	  exception is being taken by the ONE producer it was written for.
	//	  Without them the exception is a property of any narrowed row that
	//	  can arrange equal counts and a census code: a `state` obligation,
	//	  a `planning` seed row, an `impact: depth` row that claims a scope
	//	  loss. None of those is a count over a population, and admitting
	//	  them is the same one-token-two-states defect this exception exists
	//	  to remove, moved up a layer. The producer sets all three on the
	//	  single site that emits this shape (the membership-cardinality
	//	  step), so narrowing to them costs nothing real and closes the
	//	  false negative.
	censusQualified := row.Stage == ContextFabricOutcomeStageAssembledResult &&
		row.Obligation == ContextFabricAnswerObligationCount &&
		row.Impact == ContextFabricAnswerImpactScope &&
		row.Served == row.Declared &&
		row.CauseObserved &&
		coverageDetailCodeQualifiesPopulation(row.CauseCoverage)
	if row.Outcome == ContextFabricRequirementNarrowed && row.Declared > 0 && row.Served >= row.Declared && !censusQualified {
		return fmt.Errorf("outcome narrowed served %d of %d declared, which is not a reduction", row.Served, row.Declared)
	}
	return validateContextFabricRequirementRefinements(row)
}

// validateContextFabricRequirementRefinements enforces that the refinement
// chain ACCOUNTS FOR the row's own two numbers.
//
// A refinement list that did not have to reconcile with Declared and Served
// would be decoration: a stage could append a plausible-looking step, or omit
// one, and nothing would notice. Chaining it end to end is what converts the
// list into an audit -- every item removed between Declared and Served is
// attributed to a named stage on a named basis, and a stage that shrank the
// document without recording it leaves a gap this names.
func validateContextFabricRequirementRefinements(row ContextFabricPlanRequirementOutcomeRow) error {
	if len(row.Refinements) == 0 {
		return nil
	}
	if len(row.Refinements) > ContextFabricRequirementRefinementMaxCount {
		return fmt.Errorf("outcome row records %d refinements, more than the %d stages able to append one",
			len(row.Refinements), ContextFabricRequirementRefinementMaxCount)
	}
	// ONLY `narrowed` may carry a refinement, and the rule is written as an
	// ALLOW-LIST rather than as a list of refusals.
	//
	// A refinement says: this requirement was served over a population that
	// shrank from Before to After. That sentence is only true of `narrowed`.
	// The vocabulary's own doc comments say why for each of the others --
	// `satisfied` and `not_applicable` lost nothing; `unavailable` "could not
	// be served at all", so there is no surviving population to have shrunk;
	// `not_attempted` was stopped BEFORE any read, so there was never a
	// Before to reduce from.
	//
	// It was previously a deny-list naming `satisfied` and `not_applicable`.
	// That let `unavailable` and `not_attempted` carry a reduction chain
	// describing a population neither of them ever had. A deny-list is also
	// wrong by construction for a CLOSED vocabulary: the sixth outcome added
	// would be permitted by default, and permitted silently.
	if row.Outcome != ContextFabricRequirementNarrowed {
		return fmt.Errorf("outcome %q records a refinement; only %q describes a population that was reduced and still served",
			row.Outcome, ContextFabricRequirementNarrowed)
	}
	for index, refinement := range row.Refinements {
		if err := refinement.Validate(); err != nil {
			return fmt.Errorf("refinement %d: %w", index, err)
		}
	}
	// Each step continues the previous one. A gap or an overlap means the
	// steps describe two different populations, and their counts cannot
	// both be about this requirement.
	for index := 1; index < len(row.Refinements); index++ {
		if row.Refinements[index].Before != row.Refinements[index-1].After {
			return fmt.Errorf("refinement %d begins at %d but refinement %d ended at %d; the chain is broken",
				index, row.Refinements[index].Before, index-1, row.Refinements[index-1].After)
		}
	}
	if first := row.Refinements[0].Before; first != row.Declared {
		return fmt.Errorf("refinement chain begins at %d but the row declared %d", first, row.Declared)
	}
	if last := row.Refinements[len(row.Refinements)-1].After; last != row.Served {
		return fmt.Errorf("refinement chain ends at %d but the row served %d", last, row.Served)
	}
	return nil
}

// ContextFabricReductionRefinement derives the refinement a reduced outcome
// row implies, from THAT ROW'S OWN counts and causes.
//
// One authority, deliberately. Every reducing stage already states what it
// cut and why, in the row it returns; a refinement hand-built beside that
// statement would be a second place for the same fact to be written, and the
// two would disagree the first time one of them changed. Deriving it means a
// stage cannot record a step that contradicts its own row.
//
// Returns false when the row describes no reduction -- a satisfied row, or a
// row whose declared and served counts are equal. A refinement must reduce,
// so there is nothing honest to record in those cases and an empty chain is
// the correct output rather than a zero-length step.
func ContextFabricReductionRefinement(row ContextFabricPlanRequirementOutcomeRow) (ContextFabricRequirementRefinement, bool) {
	if row.Declared <= row.Served {
		return ContextFabricRequirementRefinement{}, false
	}
	// The same allow-list the validator applies, for the same reason. The
	// two halves are stated separately on purpose: this one keeps the
	// derivation from MINTING a step no outcome can carry, and the validator
	// keeps a hand-built row from carrying one anyway. A derivation that
	// emitted what the validator rejects would fail at the wire rather than
	// at the call, which is the harder place to read it.
	if row.Outcome != ContextFabricRequirementNarrowed {
		return ContextFabricRequirementRefinement{}, false
	}
	refinement := ContextFabricRequirementRefinement{
		Stage:    row.Stage,
		Basis:    row.CauseNarrowing,
		Overrun:  row.CauseOverrun,
		Coverage: row.CauseCoverage,
		Before:   row.Declared,
		After:    row.Served,
	}
	// A row that named no cause cannot produce a refinement that names one.
	// The row's own validator already refuses a causeless reduction, so this
	// is unreachable on a valid row -- handled rather than assumed, because
	// the alternative is emitting a step this type would reject.
	if refinement.Basis == "" && refinement.Overrun == "" && refinement.Coverage == "" {
		return ContextFabricRequirementRefinement{}, false
	}
	return refinement, true
}

// ContextFabricWithReductionRefinement returns row with its own reduction
// recorded, when it describes one.
//
// It is the call every reducing site makes instead of building a chain by
// hand, so "which stage cut what" is stated once per stage rather than twice.
func ContextFabricWithReductionRefinement(row ContextFabricPlanRequirementOutcomeRow) ContextFabricPlanRequirementOutcomeRow {
	if len(row.Refinements) > 0 {
		return row
	}
	if refinement, ok := ContextFabricReductionRefinement(row); ok {
		row.Refinements = []ContextFabricRequirementRefinement{refinement}
	}
	return row
}
