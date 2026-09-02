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

	// FamilyAgreementComparisonTermCount: the precedence table fired its
	// COMPARISON row, which triggers on ">=2 distinct subject terms", and
	// the projection read the topology instead. This is behaviour change
	// B6 and the class the design most wants counted: both Q-A typo
	// replicates carry two distinct subject terms, so the comparison row
	// fired before Shape was ever read and a grouped question routed to
	// explicit_comparison.
	FamilyAgreementComparisonTermCount FamilyAgreementClass = "comparison_term_count"

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
	FamilyAgreementComparisonTermCount,
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
	if projection.Row == FamilyProjectionRowInvestment || projection.Row == FamilyProjectionRowTrend {
		return FamilyAgreementGoalRowUnreachable
	}
	if precedence.Row == FamilyPrecedenceRowComparison {
		return FamilyAgreementComparisonTermCount
	}
	if precedence.Row == FamilyPrecedenceRowCohortShape && projection.Row == FamilyProjectionRowSubject {
		return FamilyAgreementOrganizationRoute
	}
	if precedence.Row == FamilyPrecedenceRowCohortShape || precedence.Row == FamilyPrecedenceRowSingleSubject {
		return FamilyAgreementShapeDivergence
	}
	return FamilyAgreementUnexplained
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
