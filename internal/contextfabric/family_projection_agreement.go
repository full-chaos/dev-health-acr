package contextfabric

// The SHADOW AGREEMENT counters: what the frame's projection would route,
// beside what the precedence table actually routes, with the disagreement
// classified by its structural CAUSE.
//
// NOTHING HERE ROUTES ANYTHING. Every value this file produces is recorded
// and counted. The precedence table decides the family that reaches a plan,
// an offer, a budget and the wire, exactly as it does today. That is a
// required, provable property of this slice, not an intention: no function
// in this file is called from a decision, and the projection's value never
// reaches a caller.
//
// WHY A SHADOW AND NOT A FLIP. The projection changes routing on real
// questions -- deliberately, in named ways (a term-count comparison no
// longer steals a grouped question; an org-wide question stops being
// answered as a cohort ranking). Those are behaviour changes on a REQUIRED
// wire field consumers branch on, and the design's own rule is that they
// move on measured data, never on a design assertion. This file is the
// instrument that produces the data. Seam 7 is where the flip happens, and
// it happens after someone reads these numbers.
//
// WHY THE CLASS AND NOT ONLY THE COUNT. "They disagreed 8% of the time" is
// not a decidable input: 8% of stolen comparisons and 8% of unexplained
// divergence are different facts with different consequences. Each class
// below is keyed on the pair of ROWS the two tables fired -- a structural
// property of the decision, not a judgement about the question -- so the
// classification is derived and cannot be tuned to make a number look
// better.

// FamilyAgreementClass names WHY the projected family and the precedence
// family differ, or that they do not. Closed vocabulary, telemetry-safe: it
// carries no question text and no subject identifier.
type FamilyAgreementClass string

const (
	// FamilyAgreementAgreed: both tables produced the same family.
	FamilyAgreementAgreed FamilyAgreementClass = "agreed"

	// FamilyAgreementProjectionUnclassified: the projection refused and
	// the precedence table did not. This is the frame layer declining to
	// guess where the precedence table guessed -- usually a frame that
	// never validated, so there was no topology to read.
	FamilyAgreementProjectionUnclassified FamilyAgreementClass = "projection_unclassified"

	// FamilyAgreementPrecedenceUnclassified: the precedence table refused
	// and the projection did not. The frame carried a topology the
	// precedence table's signals could not see -- its row 7 catches, among
	// others, an explicit cohort whose members were never named as two
	// distinct terms.
	FamilyAgreementPrecedenceUnclassified FamilyAgreementClass = "precedence_unclassified"

	// FamilyAgreementGoalRowUnreachable: the projection fired a GOAL row
	// (trend or investment_allocation) that the precedence table declares
	// deliberately unreachable in its slice. Not a mis-route on either
	// side: the two tables have different reachable ranges, and this class
	// counts exactly that difference.
	FamilyAgreementGoalRowUnreachable FamilyAgreementClass = "goal_row_unreachable_in_precedence"

	// FamilyAgreementPrecedenceComparisonRow: the precedence table fired its
	// COMPARISON row and the projection read the topology instead.
	//
	// NARROWED, NOT PATCHED, and the rename is the narrowing. This class was
	// called `comparison_term_count` and its documentation claimed the B6
	// behaviour specifically -- a grouped question stolen because the sample
	// carried ">=2 distinct subject terms". Review showed the precedence
	// comparison row fires on EITHER that term count OR a single non-empty
	// comparison term, and the class was reporting both under a name that
	// asserts only the first. A sample with one comparison term and no
	// subject terms at all was being counted as a term-count theft.
	//
	// The classifier's LOGIC is deliberately unchanged: it fires on the
	// precedence comparison row, which is exactly what it can observe and
	// exactly what its negative control proves. What changed is that the
	// name and the documentation no longer claim more than that. The B6
	// sub-condition is REPORTED as not distinguished (see the
	// enforced-versus-reported table in the tests) rather than asserted.
	//
	// Distinguishing the two sub-conditions would need the sample's terms,
	// which are free-text model output and never reach a telemetry field --
	// so it is not a thing this class can be made to do, and saying so is
	// more useful than a fourth attempt at the arm.
	FamilyAgreementPrecedenceComparisonRow FamilyAgreementClass = "precedence_comparison_row"

	// FamilyAgreementOrganizationRoute: the precedence table sent an
	// org-wide question to the discovered-cohort RANKING (its row 4 reads
	// Shape in {discovered_cohort, open}) and the projection sent it to a
	// single-subject investigation. Behaviour change B5, whose improvement
	// claim is WITHDRAWN until an organization subject can be committed
	// and a state-ish producer serves it -- so this counter measures how
	// often the route would fire, not how often it would help.
	FamilyAgreementOrganizationRoute FamilyAgreementClass = "organization_route"

	// FamilyAgreementShapeDivergence: the precedence table reached a row
	// that reads Shape and the projection read the union discriminator,
	// and they disagreed. Shape is the least stable field in the
	// interpretation -- six replicates of two questions produced three
	// distinct values -- so this class is where that instability shows up
	// as a routing difference.
	FamilyAgreementShapeDivergence FamilyAgreementClass = "shape_divergence"

	// FamilyAgreementUnexplained: the two tables disagreed and no class
	// above describes the pair of rows that fired.
	//
	// THIS CLASS EXISTING IS THE POINT. A classifier with no residual
	// bucket forces every observation into a named cause, and the named
	// causes then look complete because nothing can fall outside them. A
	// non-zero count here is a finding: it says the design's account of
	// how the two tables differ is incomplete, which is a thing to report
	// rather than to absorb.
	FamilyAgreementUnexplained FamilyAgreementClass = "unexplained"
)

var familyAgreementClasses = [...]FamilyAgreementClass{
	FamilyAgreementAgreed,
	FamilyAgreementProjectionUnclassified,
	FamilyAgreementPrecedenceUnclassified,
	FamilyAgreementGoalRowUnreachable,
	FamilyAgreementPrecedenceComparisonRow,
	FamilyAgreementOrganizationRoute,
	FamilyAgreementShapeDivergence,
	FamilyAgreementUnexplained,
}

// FamilyAgreementClassCount is the closed vocabulary's size.
const FamilyAgreementClassCount = len(familyAgreementClasses)

// FamilyAgreementClassVocabulary returns the closed vocabulary in the order
// ClassifyFamilyAgreement evaluates it, so a test can assert the evaluation
// order against the vocabulary rather than against a copied list.
func FamilyAgreementClassVocabulary() [FamilyAgreementClassCount]FamilyAgreementClass {
	return familyAgreementClasses
}

// ValidFamilyAgreementClass reports membership. The empty value is not a
// member: every comparison produces a class, because the classifier is
// total and its last arm is `unexplained`.
func ValidFamilyAgreementClass(value FamilyAgreementClass) bool {
	for _, member := range familyAgreementClasses {
		if member == value {
			return true
		}
	}
	return false
}

// FamilyAgreement is ONE comparison: what each table decided, how, and the
// class of the difference.
type FamilyAgreement struct {
	// ProjectedFamily is what the frame projects to.
	ProjectedFamily QuestionFamily
	// ProjectedRow is which §13.4.1 row fired.
	ProjectedRow FamilyProjectionRow
	// PrecedenceFamily is what the shipped table routed. THIS IS THE ONE
	// THAT IS SERVED.
	PrecedenceFamily QuestionFamily
	// PrecedenceRow is which §4.2 row fired.
	PrecedenceRow FamilyPrecedenceRow
	// Class is the structural cause of the difference, or `agreed`.
	Class FamilyAgreementClass
	// Agreed is a derived convenience, true exactly when Class is
	// `agreed`. Carried on the struct so a counter does not have to
	// re-derive it and so the two cannot drift.
	Agreed bool
}

// ClassifyFamilyAgreement compares a projected family with the precedence
// family and names the structural cause of any difference.
//
// TOTAL, PURE, AND KEYED ON THE ROWS, not on the families. Two tables can
// reach the same family through different rules and different families
// through the same rule; the pair of rows is what says WHY they differ,
// and it is a property of the decisions rather than an opinion about the
// question. Nothing here reads question text, subject terms or any model
// output.
//
// THE ARM ORDER IS THE CLASSIFICATION RULE, from the most specific cause to
// the least, and it is stated rather than left to reading order:
//
//  1. Agreement, first, so no disagreement arm can claim an agreement.
//  2. Either side refusing, next: "one table declined" is a different fact
//     from "the two tables chose differently", and folding it into a
//     divergence class would overstate how often they conflict.
//  3. The projection firing a goal row the precedence table cannot reach.
//     This is a RANGE difference, not a disagreement about one question,
//     and it is more specific than anything below -- the precedence table
//     could not have produced this family whatever it read.
//  4. The precedence comparison row, which fires on a term COUNT. More
//     specific than a Shape divergence because the comparison row is
//     evaluated BEFORE Shape is read, so a question it takes never
//     reached the Shape rows at all.
//  5. The organization route, which is the one named case where the
//     precedence Shape row and a single-subject projection disagree by
//     design.
//  6. Any other disagreement in which the precedence table read Shape.
//  7. Everything else, named `unexplained` rather than absorbed.
func ClassifyFamilyAgreement(projection FamilyProjection, precedence FamilySampleOutcome) FamilyAgreement {
	agreement := FamilyAgreement{
		ProjectedFamily:  projection.Family,
		ProjectedRow:     projection.Row,
		PrecedenceFamily: precedence.Family,
		PrecedenceRow:    precedence.Row,
	}
	agreement.Class = classifyFamilyAgreement(projection, precedence)
	agreement.Agreed = agreement.Class == FamilyAgreementAgreed
	return agreement
}

func classifyFamilyAgreement(projection FamilyProjection, precedence FamilySampleOutcome) FamilyAgreementClass {
	if projection.Family == precedence.Family {
		return FamilyAgreementAgreed
	}
	if projection.Family == QuestionFamilyUnclassified {
		return FamilyAgreementProjectionUnclassified
	}
	if precedence.Family == QuestionFamilyUnclassified {
		return FamilyAgreementPrecedenceUnclassified
	}
	// The RANGE difference -- but only when the precedence side has no
	// structural cause of its own to report.
	//
	// This arm fired on the projection row ALONE, which is the same defect
	// the organization-route arm had: claiming a cause without verifying
	// it. A named-subject trend frame whose receipt carried a spurious
	// GroupKind routed the precedence table to a grouped cohort by its
	// row 1, and this arm absorbed that into "trend is unreachable" --
	// attributing a real structural mis-signal to a range difference. The
	// identical spurious signal was classified `unexplained` on a
	// non-trend frame, so one bad GroupKind was reported two ways
	// depending on a goal that had nothing to do with it.
	//
	// The range difference is only the cause when the precedence table read
	// the SAME structure and merely could not reach the family. A precedence
	// row that asserts its own structure signal has told us something the
	// range difference does not explain, and it must be reported as that.
	if (projection.Row == FamilyProjectionRowInvestment || projection.Row == FamilyProjectionRowTrend) &&
		!precedenceRowAssertsItsOwnStructure(precedence.Row) {
		return FamilyAgreementGoalRowUnreachable
	}
	if precedence.Row == FamilyPrecedenceRowComparison {
		return FamilyAgreementPrecedenceComparisonRow
	}
	// The ORGANIZATION route, keyed on the frame's own TOPOLOGY.
	//
	// It was keyed on the projection ROW, which is wrong and was found by
	// review: row 7 covers a named subject and the organization scope
	// alike, so every named-subject frame whose interpretation emitted an
	// open shape was counted as a B5 organization route. That corrupts the
	// exact number the flip decision reads -- inflating the one class whose
	// improvement claim the design has already withdrawn.
	//
	// Keying on the discriminator the projection actually read makes the
	// class say what its name says. A named-subject frame in the same
	// situation now falls through to shape_divergence, which is what it is.
	if precedence.Row == FamilyPrecedenceRowCohortShape &&
		projection.Row == FamilyProjectionRowSubject &&
		projection.Topology == SubjectExpressionOrganizationScope {
		return FamilyAgreementOrganizationRoute
	}
	if precedence.Row == FamilyPrecedenceRowCohortShape || precedence.Row == FamilyPrecedenceRowSingleSubject {
		return FamilyAgreementShapeDivergence
	}
	return FamilyAgreementUnexplained
}

// precedenceRowAssertsItsOwnStructure reports whether a precedence row
// fired on a STRUCTURE SIGNAL the sample carried, rather than on the shape
// or on nothing.
//
// Rows 1-3 of the precedence table each fire on a signal the interpretation
// emitted -- a grouping kind, a scope-anchor asymmetry, a comparison term
// count. When one of those fires, the precedence table has named a cause,
// and any agreement class that attributes the disagreement to something
// else is hiding it. Rows 4-5 read Shape, which is a classification rather
// than an emitted structure signal, and row 7 fired on nothing at all.
//
// Enumerated positively and asserted total against the row vocabulary by
// TestEveryPrecedenceRowIsClassifiedAsStructureOrNot, so a row added later
// cannot default into "asserts nothing" and silently widen every class that
// consults this.
func precedenceRowAssertsItsOwnStructure(row FamilyPrecedenceRow) bool {
	switch row {
	case FamilyPrecedenceRowGroupKind, FamilyPrecedenceRowScopeAnchor, FamilyPrecedenceRowComparison:
		return true
	default:
		return false
	}
}

// FamilyAgreementCounters is the tally over a population of comparisons.
//
// Every class carries a count INCLUDING THE ZEROES. A distribution that
// omits its empty classes is indistinguishable from one whose classifier
// never reaches them, and a tier nothing lands in reads exactly like a
// check that always answers no -- which is how a dead classification arm
// survives for its whole life looking green.
type FamilyAgreementCounters struct {
	// Total is every comparison offered, agreements included. Without it
	// "12 disagreements" has no denominator and "the counter never ran"
	// and "nothing disagreed" are the same observation.
	Total int
	// Agreed is the agreement count, carried separately so a reader does
	// not have to subtract.
	Agreed int
	// ByClass counts every class in the closed vocabulary, zeroes present.
	ByClass map[FamilyAgreementClass]int
}

// NewFamilyAgreementCounters returns counters with every class present at
// zero.
func NewFamilyAgreementCounters() *FamilyAgreementCounters {
	counters := &FamilyAgreementCounters{ByClass: make(map[FamilyAgreementClass]int, FamilyAgreementClassCount)}
	for _, class := range familyAgreementClasses {
		counters.ByClass[class] = 0
	}
	return counters
}

// Observe records one comparison.
func (c *FamilyAgreementCounters) Observe(agreement FamilyAgreement) {
	c.Total++
	if agreement.Agreed {
		c.Agreed++
	}
	c.ByClass[agreement.Class]++
}

// Disagreed is the count of comparisons that were not agreements. Derived
// from Total and Agreed rather than accumulated separately, so the three
// numbers cannot drift apart.
func (c *FamilyAgreementCounters) Disagreed() int { return c.Total - c.Agreed }

// FamilyAgreementShadow is the event the production comparison reports.
//
// CLOSED ENUMS ONLY, on the same rule the family-resolution event follows:
// no question text, no subject identifier, no anchor term. Every field here
// is a vocabulary member or a boolean.
type FamilyAgreementShadow struct {
	// FrameObserved is whether a VALIDATED frame existed for this
	// interpretation at all.
	//
	// This is the denominator, and it is a separate fact from agreement.
	// "The model emitted no frame", "the frame was refused" and "the two
	// tables disagreed" are three different states, and an event that
	// reported only the last would make the first two invisible -- the
	// exact countability gap the frame-validation event was widened to
	// close for its own axes.
	FrameObserved bool
	// FrameOutcome is the frame's validation outcome, so a run can tell a
	// missing frame from a refused one.
	FrameOutcome FrameValidationOutcome
	// Agreement is the comparison, valid only when FrameObserved.
	Agreement FamilyAgreement
	// ProjectionVersion is the frame version the projection ran against,
	// so a persisted disagreement can be replayed against the table that
	// produced it.
	ProjectionVersion string
}

// ShadowFamilyAgreement builds the production comparison from one
// interpretation's receipt and the family resolution that same call
// produced.
//
// BOTH SIDES COME FROM THE SAME INTERPRETATION. The frame is the one this
// call's receipt carries and the resolution is the one this call's resolver
// produced, so the comparison is of two readings of ONE model output rather
// than of two calls. A comparison across calls would measure the sampler's
// variance, which is a real thing and a different thing.
//
// IT COMPARES AGAINST WHAT IS ACTUALLY ROUTED -- outcome.Family -- not
// against the winning sample's own verdict. At N=1 they are the same value.
// Above N=1 they are not: a plurality with no strict majority routes
// `unclassified` while every individual sample resolved to something, and a
// shadow keyed on the winner would then report agreement with a family the
// engine did not use. Reading outcome.Family means this counter keeps
// measuring the right thing when the ensemble is turned on, rather than
// becoming quietly wrong at the moment N changes.
//
// TOTAL AND NIL-SAFE. A receipt with no frame, or with a refused one,
// produces an event that says so rather than a zero value that reads like
// agreement.
func ShadowFamilyAgreement(receipt ModelExecutionReceipt, outcome QuestionFamilyOutcome) FamilyAgreementShadow {
	shadow := FamilyAgreementShadow{FrameOutcome: receipt.FrameOutcome}
	if receipt.QuestionFrame == nil || receipt.FrameOutcome != FrameValidationOutcomeValid {
		// No validated frame: there is nothing to project. Deliberately
		// NOT reported as a disagreement -- counting "the model emitted no
		// frame" as the projection disagreeing would inflate the
		// disagreement rate with a fact about emission, and the flip
		// decision reads that rate.
		return shadow
	}
	frame := *receipt.QuestionFrame
	shadow.FrameObserved = true
	shadow.ProjectionVersion = frame.Version

	// The ROUTED family, with the row that produced it. On a rejected
	// plurality no row fired at all, and `no_row_matched` is the honest
	// name for that rather than the winning sample's row -- which would
	// attribute the routing to a rule the engine did not act on.
	routed := FamilySampleOutcome{Family: outcome.Family, Row: outcome.Winner.Row}
	if outcome.WinningSampleIndex < 0 {
		routed.Row = FamilyPrecedenceRowNone
	}
	shadow.Agreement = ClassifyFamilyAgreement(DeriveQuestionFamily(frame), routed)
	return shadow
}
