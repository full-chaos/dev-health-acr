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
// CLOSED, three members.
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
)

var contextFabricOutcomeStages = [...]ContextFabricOutcomeStage{
	ContextFabricOutcomeStagePlanning,
	ContextFabricOutcomeStageAssembledResult,
	ContextFabricOutcomeStageProjection,
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
	"ranking", "remaining_work", "evidence", "coverage", "count",
	"allocation_breakdown", "trend_series", "period_delta",
}

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
	if row.Outcome == ContextFabricRequirementNarrowed && row.Declared > 0 && row.Served >= row.Declared {
		return fmt.Errorf("outcome narrowed served %d of %d declared, which is not a reduction", row.Served, row.Declared)
	}
	return nil
}
