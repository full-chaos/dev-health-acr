package contextfabric

// SEAM 7's routing decision: which of the two family readings is SERVED.
//
// PR 2 (#396) made the family a derived projection of the frame and left it
// in shadow: the precedence table decided every family that reached a plan,
// an offer, a budget or the wire, and the projection was counted beside it.
// This file is where that stops being a measurement and becomes a routing
// decision.
//
// THE RULE IS THE ORDER'S RULE, quoted so the code and the order cannot
// drift: routing switches to the projected family ONLY where the shadow
// counters SHOW the agreement class is safe, and every other class is an
// EXPLICIT, named decision rather than a silent carry-over. "Shown safe"
// means observed on the live measurement of record, not merely absent from
// it -- a class with zero observations has no evidence either way, and
// treating no evidence as permission is the false-green shape this program
// has paid for repeatedly.
//
// THE MEASUREMENT OF RECORD is lane-rig-advance-13's shadow agreement table
// at tip 6b0cebd9: 25 rows over the recorded thirteen and the twelve
// labelled questions, run through the rig on real data. Its class totals:
//
//	agreed                             21
//	organization_route                  3
//	shape_divergence                    1
//	projection_unclassified             0
//	precedence_unclassified             0
//	goal_row_unreachable_in_precedence  0
//	precedence_comparison_row           0
//	unexplained                         0
//
// Five of the eight arms were never observed. That is a fact about this
// question set, NOT a claim they are unreachable, and it is exactly why an
// unobserved class does not get switched: there is nothing to have shown it
// safe.

// FamilyRouteSource names which table produced the SERVED family.
//
// It is the telemetry axis the flip decision is read on, and it is distinct
// from ContextFabricQuestionFamilySource (`model`/`carried`/`none`), which
// names where the PRECEDENCE table's inputs came from. The two answer
// different questions and are deliberately not merged: one says which
// derivation decided, the other says what that derivation read.
type FamilyRouteSource string

const (
	// FamilyRouteProjected: the frame's projection decided the served
	// family.
	FamilyRouteProjected FamilyRouteSource = "projected"
	// FamilyRoutePrecedence: the §4.2 precedence table decided it, exactly
	// as it did before this slice.
	FamilyRoutePrecedence FamilyRouteSource = "precedence"
)

var familyRouteSources = [...]FamilyRouteSource{FamilyRouteProjected, FamilyRoutePrecedence}

// FamilyRouteSourceCount is the closed vocabulary's size.
const FamilyRouteSourceCount = len(familyRouteSources)

// FamilyRouteSourceVocabulary returns the closed vocabulary in declared
// order.
func FamilyRouteSourceVocabulary() [FamilyRouteSourceCount]FamilyRouteSource {
	return familyRouteSources
}

// ValidFamilyRouteSource reports membership. The empty value is not a
// member: every decision names a source.
func ValidFamilyRouteSource(value FamilyRouteSource) bool {
	for _, member := range familyRouteSources {
		if member == value {
			return true
		}
	}
	return false
}

// FamilyRouteDisposition is the CLOSED set of reasons a class routes the way
// it does. It exists so the PR body's table and the code are the same
// artifact: a disposition added here without a row there fails the totality
// test, and a class whose disposition changes changes a named constant
// rather than an `if`.
type FamilyRouteDisposition string

const (
	// FamilyRouteIdentical: the two tables produced the same family, so
	// the switch cannot change what is served. Serving the projection here
	// is a no-op by construction, and the AFTER measurement asserts the
	// family column is byte-identical on every such row.
	FamilyRouteIdentical FamilyRouteDisposition = "identical"
	// FamilyRouteIntendedChange: the disagreement is the behaviour change
	// this slice exists to make, and the live table observed it.
	FamilyRouteIntendedChange FamilyRouteDisposition = "intended_change"
	// FamilyRouteDeclinedWithdrawnClaim: the route would fire, but the
	// design has WITHDRAWN its improvement claim. B5: an org-wide question
	// projecting to a single-subject investigation needs an organization
	// subject that can be committed and a state-ish producer that serves
	// it; neither exists, so the projection would route the question
	// somewhere nothing can answer it.
	FamilyRouteDeclinedWithdrawnClaim FamilyRouteDisposition = "declined_withdrawn_claim"
	// FamilyRouteSwitchedAdditiveUnobserved: the precedence side produced
	// NO family, so today's outcome for this class is an unserved question.
	// Routing to the projected family can only ADD an answer where there
	// was none -- there is no served answer to put at risk, which is what
	// separates this class from every other unobserved one and is the whole
	// of the argument for switching it.
	//
	// UNOBSERVED ON THE MEASUREMENT OF RECORD (0 of 25 rows), and switched
	// on DESIGN grounds rather than measured ones. That distinction is the
	// reason this disposition has its own name instead of sharing
	// `intended_change`: a reader must be able to tell which switches the
	// live run actually exercised from which were argued.
	//
	// Every answer served under it carries family_source=projected and its
	// agreement class in telemetry, and the AFTER run's guard is that any
	// row landing here is QUOTED and hand-checked for family correctness
	// before the merge token -- an additive claim still has to be right,
	// not merely additive.
	FamilyRouteSwitchedAdditiveUnobserved FamilyRouteDisposition = "switched_additive_unobserved"
	// FamilyRouteDeclinedNotObserved: the class was never observed on the
	// measurement of record, so nothing has shown it safe. Not a claim it
	// is unreachable or wrong -- a statement that this slice has no
	// evidence for it and does not switch on none.
	FamilyRouteDeclinedNotObserved FamilyRouteDisposition = "declined_not_observed"
	// FamilyRouteDeclinedIndistinguishable: the class cannot be decided
	// from counters at all. The precedence comparison row fires on EITHER
	// ">=2 distinct subject terms" OR "one non-empty comparison term", and
	// PR 2 established the two sub-conditions are PERMANENTLY
	// indistinguishable in telemetry -- separating them needs the sample's
	// terms, which are free-text model output that never reaches a
	// telemetry field. A class that cannot be measured cannot be shown
	// safe by measurement.
	FamilyRouteDeclinedIndistinguishable FamilyRouteDisposition = "declined_indistinguishable"
	// FamilyRouteDeclinedProjectionSilent: the projection refused and the
	// precedence table did not. Switching would turn a served answer into
	// `unclassified` -- strictly fewer answers, which is the opposite of
	// this program's yardstick.
	FamilyRouteDeclinedProjectionSilent FamilyRouteDisposition = "declined_projection_silent"
	// FamilyRouteNoFrameObserved: NO FRAME VALIDATED, so no projection
	// existed and no comparison happened. The precedence table decided,
	// exactly as it did before this slice.
	//
	// THIS IS NOT `agreed`, AND CALLING IT THAT CORRUPTED THE MEASUREMENT.
	// The first version of this code installed Class=agreed /
	// Disposition=identical on the frame-absent path, reasoning that
	// precedence served and nothing changed. Both halves are true and the
	// label is still wrong: `agreed` asserts that TWO tables produced the
	// same family, and here the second table never ran. Every frame-absent
	// turn therefore inflated the `agreed`/`identical` counters -- the exact
	// counters the flip decision reads as evidence that the switch is a
	// no-op on most questions. A counter that cannot tell "both tables
	// agreed" from "there was only one table" is not evidence of agreement.
	//
	// Class is left EMPTY on this disposition, deliberately: the agreement
	// vocabulary describes COMPARISONS, and there was none to describe.
	FamilyRouteNoFrameObserved FamilyRouteDisposition = "no_frame_observed"
	// FamilyRouteCarried: a PRIOR TURN's family was carried onto this
	// outcome after the routing decision was made, so neither table decided
	// what is served -- the earlier turn did.
	//
	// REPORTED LIMIT, NOT A FULL FIX (round 3, Medium). The family-resolution
	// EVENT is emitted inside the interpreter, and `applyCarriedPlan` runs
	// later in the engine; an event already on the wire cannot be amended.
	// So on a carried turn the emitted event records the INTERPRETATION-TIME
	// decision, and the served family can differ from it. Moving the emission
	// after carry means moving it out of the interpreter, which is a
	// restructure this slice is not making.
	//
	// What IS fixed: the OUTCOME is no longer self-contradictory. A carried
	// outcome carries this disposition, so any reader holding the outcome
	// sees that neither table decided. A reader consuming the telemetry
	// STREAM must join on the plan-carry event to see a carried turn, and
	// that is stated here rather than left for someone to discover from a
	// counter that disagrees with the answer.
	FamilyRouteCarried FamilyRouteDisposition = "carried_from_prior_turn"
	// FamilyRouteDeclinedUnexplained: no class describes the pair of rows
	// that fired. A non-zero count here is a FINDING, not a routing input,
	// and the safe thing to do with a finding is not to route on it.
	FamilyRouteDeclinedUnexplained FamilyRouteDisposition = "declined_unexplained"
)

// FamilyRouteDecision is one routing decision, and it is the whole record of
// why the answer got the family it got.
type FamilyRouteDecision struct {
	// Family is the SERVED family -- what reaches the plan and the wire.
	Family QuestionFamily
	// Source names which table produced Family.
	Source FamilyRouteSource
	// Class is the agreement class the decision was keyed on.
	Class FamilyAgreementClass
	// Disposition is why this class routes the way it does.
	Disposition FamilyRouteDisposition
	// Switched is true exactly when the served family DIFFERS from what
	// the precedence table alone would have served. Derived and carried so
	// a counter never has to re-derive it and the two cannot drift.
	//
	// Note it is NOT the same as `Source == projected`: the `agreed` class
	// routes from the projection and changes nothing, which is the whole
	// reason 21 of 25 rows are safe.
	Switched bool
}

var familyRouteDispositions = [...]FamilyRouteDisposition{
	FamilyRouteIdentical,
	FamilyRouteIntendedChange,
	FamilyRouteSwitchedAdditiveUnobserved,
	FamilyRouteDeclinedWithdrawnClaim,
	FamilyRouteDeclinedNotObserved,
	FamilyRouteDeclinedIndistinguishable,
	FamilyRouteDeclinedProjectionSilent,
	FamilyRouteDeclinedUnexplained,
	FamilyRouteNoFrameObserved,
	FamilyRouteCarried,
}

// FamilyRouteDispositionCount is the closed vocabulary's size.
const FamilyRouteDispositionCount = len(familyRouteDispositions)

// FamilyRouteDispositionVocabulary returns the closed vocabulary in declared
// order.
func FamilyRouteDispositionVocabulary() [FamilyRouteDispositionCount]FamilyRouteDisposition {
	return familyRouteDispositions
}

// ValidFamilyRouteDisposition reports membership. The empty value is not a
// member: every decision names a disposition.
func ValidFamilyRouteDisposition(value FamilyRouteDisposition) bool {
	for _, member := range familyRouteDispositions {
		if member == value {
			return true
		}
	}
	return false
}

// familyRouteRule is one row of the closed decision table.
type familyRouteRule struct {
	class       FamilyAgreementClass
	source      FamilyRouteSource
	disposition FamilyRouteDisposition
}

// familyRouteTable is THE table. One row per FamilyAgreementClass member, in
// the agreement vocabulary's own order, so a member added there without a
// row here fails the totality test rather than falling through to a default.
var familyRouteTable = [...]familyRouteRule{
	{FamilyAgreementAgreed, FamilyRouteProjected, FamilyRouteIdentical},
	{FamilyAgreementProjectionUnclassified, FamilyRoutePrecedence, FamilyRouteDeclinedProjectionSilent},
	{FamilyAgreementPrecedenceUnclassified, FamilyRouteProjected, FamilyRouteSwitchedAdditiveUnobserved},
	{FamilyAgreementGoalRowUnreachable, FamilyRoutePrecedence, FamilyRouteDeclinedNotObserved},
	{FamilyAgreementPrecedenceComparisonRow, FamilyRoutePrecedence, FamilyRouteDeclinedIndistinguishable},
	{FamilyAgreementOrganizationRoute, FamilyRoutePrecedence, FamilyRouteDeclinedWithdrawnClaim},
	{FamilyAgreementShapeDivergence, FamilyRouteProjected, FamilyRouteIntendedChange},
	{FamilyAgreementUnexplained, FamilyRoutePrecedence, FamilyRouteDeclinedUnexplained},
}

// RouteQuestionFamily decides which family is SERVED for one comparison.
//
// TOTAL AND PURE. Every FamilyAgreementClass member has a row; an agreement
// carrying a class outside the closed vocabulary (which ClassifyFamilyAgreement
// cannot produce, since its last arm is `unexplained`) routes to precedence
// under the unexplained disposition, because an unrecognized class is exactly
// the state in which serving today's behaviour is the only defensible move.
func RouteQuestionFamily(agreement FamilyAgreement) FamilyRouteDecision {
	for _, rule := range familyRouteTable {
		if rule.class != agreement.Class {
			continue
		}
		decision := FamilyRouteDecision{
			Source: rule.source, Class: rule.class, Disposition: rule.disposition,
		}
		if rule.source == FamilyRouteProjected {
			decision.Family = agreement.ProjectedFamily
		} else {
			decision.Family = agreement.PrecedenceFamily
		}
		// Switched is measured against what the precedence table alone
		// would have served -- the served family BEFORE this slice -- and
		// never inferred from the source. The `agreed` class routes from
		// the projection and is not a switch.
		decision.Switched = decision.Family != agreement.PrecedenceFamily
		return decision
	}
	return FamilyRouteDecision{
		Family:      agreement.PrecedenceFamily,
		Source:      FamilyRoutePrecedence,
		Class:       agreement.Class,
		Disposition: FamilyRouteDeclinedUnexplained,
		Switched:    false,
	}
}
