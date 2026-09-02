package contextfabric

// The question family as a DERIVED PROJECTION of the frame (design §13.4.1).
//
// SHADOW ONLY IN THIS SLICE. Nothing routes on the value this file
// produces. The shipped precedence table (chaos4632_question_family_
// precedence.go) still decides every family that reaches a plan, an offer,
// a budget or the wire; the projection is computed beside it, compared,
// and counted (family_projection_agreement.go). Removing the precedence
// table and the interpreter's family list is seam 7's job, and it happens
// only after this slice's shadow data says what the flip would cost.
//
// WHY A PROJECTION AT ALL. QuestionFamily mixes five independent axes --
// subject topology, analytical operation, temporal operation, business
// domain and answer shape -- into an eight-member enum, so a composed
// question either loses part of itself or needs a family per cross-product
// cell. The frame carries the axes separately; the family becomes what it
// always semantically was, a lossy READ of the frame, and the loss is
// declared here rather than absorbed by adding a ninth family (§3.2's rule
// is still in force).
//
// THE PURITY IS THE POINT, not an implementation nicety. This function
// reads the frame and nothing else: no registry, no capability table, no
// resolution state, no model text, and no family string. That is what
// makes "the family is a projection of the frame" true rather than
// approximate, and it is what lets the projection be REPLAYED from a
// persisted receipt long after the turn that produced it.

// FamilyProjectionRow names WHICH row of the §13.4.1 table decided a
// frame's family. Closed vocabulary, telemetry-safe: it carries no
// question text and no subject identifier, only which rule fired.
//
// It exists for the same reason FamilyPrecedenceRow does -- "the family
// was X" is not a diagnosable statement on its own, and two frames
// reaching the same family through different rows are different states
// worth telling apart. It is also what makes a shadow DISAGREEMENT
// diagnosable: the disagreement class names the pair of rows, so an
// operator reads which rule each side fired rather than only that they
// differed.
type FamilyProjectionRow string

const (
	// FamilyProjectionRowGrouped is row 1: Kind == grouped_members.
	FamilyProjectionRowGrouped FamilyProjectionRow = "topology_grouped"
	// FamilyProjectionRowScoped is row 2: Kind == children_of_scope.
	FamilyProjectionRowScoped FamilyProjectionRow = "topology_scoped"
	// FamilyProjectionRowExplicit is row 3: Kind == explicit_set.
	//
	// NARROWER than precedence row 3 on purpose, and the narrowing is
	// behaviour change B6. The precedence row fires on a TERM COUNT (">=2
	// distinct subject terms"), which stole Q-A: both of its typo
	// replicates carry two distinct subject terms, so the comparison row
	// fired before Shape was ever read and a grouped question routed to
	// explicit_comparison. Under the union that theft cannot happen -- a
	// grouped question emits grouped_members and two terms inside it are
	// just terms, not an implied comparison. The count test belongs to
	// resolution (invariant I12), not to routing.
	FamilyProjectionRowExplicit FamilyProjectionRow = "topology_explicit"
	// FamilyProjectionRowDiscovered is row 4: Kind == discovered_kind.
	FamilyProjectionRowDiscovered FamilyProjectionRow = "topology_discovered"
	// FamilyProjectionRowInvestment is row 5: a single-subject topology
	// whose goals include allocate_investment.
	FamilyProjectionRowInvestment FamilyProjectionRow = "single_subject_investment"
	// FamilyProjectionRowTrend is row 6: a single-subject topology whose
	// goals include describe_trend.
	FamilyProjectionRowTrend FamilyProjectionRow = "single_subject_trend"
	// FamilyProjectionRowSubject is row 7: any other single-subject
	// topology.
	FamilyProjectionRowSubject FamilyProjectionRow = "single_subject_default"
	// FamilyProjectionRowNone is row 8: the discriminator is not a
	// vocabulary member -- an unset or refused frame. Refuse to guess.
	FamilyProjectionRowNone FamilyProjectionRow = "no_row_matched"
)

var familyProjectionRows = [...]FamilyProjectionRow{
	FamilyProjectionRowGrouped,
	FamilyProjectionRowScoped,
	FamilyProjectionRowExplicit,
	FamilyProjectionRowDiscovered,
	FamilyProjectionRowInvestment,
	FamilyProjectionRowTrend,
	FamilyProjectionRowSubject,
	FamilyProjectionRowNone,
}

// FamilyProjectionRowCount is the closed vocabulary's size.
const FamilyProjectionRowCount = len(familyProjectionRows)

// FamilyProjectionRowVocabulary returns the closed vocabulary in its
// published order, which is the TABLE ORDER -- rows are evaluated in the
// order this array lists them, so a test can assert the evaluation order
// against the vocabulary rather than against a hand-copied list.
func FamilyProjectionRowVocabulary() [FamilyProjectionRowCount]FamilyProjectionRow {
	return familyProjectionRows
}

// ValidFamilyProjectionRow reports membership. The empty value is NOT a
// member: a row is always named, because the table is total.
func ValidFamilyProjectionRow(value FamilyProjectionRow) bool {
	for _, member := range familyProjectionRows {
		if member == value {
			return true
		}
	}
	return false
}

// FamilyProjection is the projection's verdict for ONE frame.
type FamilyProjection struct {
	// Family is the projected family. Never empty: the table is TOTAL and
	// its last row is unclassified.
	Family QuestionFamily
	// Row names which rule fired.
	Row FamilyProjectionRow
}

// DeriveQuestionFamily projects a frame onto the shipped eight-member
// family vocabulary. Pure, total, first-match.
//
// TOPOLOGY DECIDES; GOALS DECIDE ONLY UNDER A SINGLE SUBJECT. Rows 1-4
// read the union discriminator alone, and only rows 5-7 read Goals. That
// order is round 4's finding N1, and it is not a preference: the frozen
// table put the two goal rows ahead of the topology rows, so "which teams'
// health is trending down?" -- a discovered cohort with a trend goal --
// projected to `trend`, whose registry row is SubjectAxisOne with the
// single-subject budget profile and single-subject clarification axes. The
// projection would have reintroduced exactly the single-subject garble
// CHAOS-4622 removed for Q-B. The family is read for clarification axes
// and budget, both of which are TOPOLOGY properties, so a table whose
// early rows are not topology-driven breaks the very compatibility
// contract that permits reading the family at all (§13.4.2).
//
// THE ROW ORDER IS THE GOAL PRECEDENCE. Goals is a SET -- it has to be, or
// "which teams are struggling and what are the driving factors" is
// unrepresentable, because no single goal makes both ranking and drivers
// required. A set needs a precedence to route deterministically, and the
// row order supplies it, which keeps the choice SERVER-SIDE. The
// alternative considered and rejected was asking the model to nominate a
// primary goal: that is a routing decision handed to model output.
//
// THE TREND ROW HAS NO TEMPORAL CLAUSE, deliberately (round-1 F4). It once
// read `Temporal in {time_series, period_comparison}`, which made the
// derivation semantically non-total while remaining mechanically total:
// {describe_trend, named_subject, bounded_window} is a legal frame (I8
// permits bounded_window), missed the row, and fell through to
// subject_investigation -- while the SAME intent expressed as time_series
// produced trend. Two permitted readings of one intent gave two families,
// which is precisely the instability this design exists to remove. I8
// already guarantees Temporal != current for a trend goal, so the temporal
// clause did no work except break totality. Oracle O2 is the property.
//
// explain_change is NOT on the trend row, also deliberately: it is a
// drivers question about a change, and subject_investigation carrying
// principal_drivers + period_delta is the better projection than trend,
// whose answer contract is a series.
//
// THE PROJECTION IS LOSSY AND THE LOSS IS DECLARED, not repaired. A count
// over a discovered kind ({count_or_aggregate, discovered_kind}) is legal
// under I9 and lands on row 4, a family whose NAME says ranking while the
// frame derives no ranking obligation. The eight-member vocabulary has no
// count member, so a count question must project onto a topology family
// and the name necessarily overstates. The OPERATION is not lost -- it
// survives on the frame's obligations, where the plan reads it -- and the
// derived require_ranking for that plan is false, which is the honest
// value on a family whose name says otherwise. That is behaviour change
// B7, and it is why no new stage may read the family.
func DeriveQuestionFamily(frame QuestionFrame) FamilyProjection {
	switch frame.SubjectExpression.Kind {
	// Rows 1-4 -- TOPOLOGY. The discriminator alone decides; no goal, no
	// temporal, no dimension is read.
	case SubjectExpressionGroupedMembers:
		return FamilyProjection{QuestionFamilyGroupedCohortStatus, FamilyProjectionRowGrouped}
	case SubjectExpressionChildrenOfScope:
		return FamilyProjection{QuestionFamilyScopedCohortStatus, FamilyProjectionRowScoped}
	case SubjectExpressionExplicitSet:
		return FamilyProjection{QuestionFamilyExplicitComparison, FamilyProjectionRowExplicit}
	case SubjectExpressionDiscoveredKind:
		return FamilyProjection{QuestionFamilyDiscoveredCohortRanking, FamilyProjectionRowDiscovered}

	// Rows 5-7 -- a SINGLE SUBJECT. Only here do goals decide, and the row
	// order is their precedence.
	//
	// organization_scope shares these rows with named_subject because the
	// organization IS the subject of "how are we doing?" -- stage 1 routed
	// that to a cohort RANKING, which discovers a member set and orders
	// it, answering a question the user did not ask. That reroute is
	// behaviour change B5 and it is DEFERRED, not claimed: nothing commits
	// an organization subject today and only one producer serves that
	// subject kind, so an organization_scope frame with a state-ish goal
	// derives unavailable outcomes. The route stays in the table because
	// it is the correct projection; what is withdrawn is any claim that it
	// improves an answer today.
	case SubjectExpressionNamed, SubjectExpressionOrganizationScope:
		switch {
		case frame.HasGoal(GoalAllocateInvestment):
			return FamilyProjection{QuestionFamilyInvestmentAllocation, FamilyProjectionRowInvestment}
		case frame.HasGoal(GoalDescribeTrend):
			return FamilyProjection{QuestionFamilyTrend, FamilyProjectionRowTrend}
		default:
			return FamilyProjection{QuestionFamilySubjectInvestigation, FamilyProjectionRowSubject}
		}

	// Row 8 -- the discriminator is not a vocabulary member. A refused or
	// unset frame projects to unclassified, which is the refuse-to-guess
	// answer and today's unchanged behaviour, NOT an error and NOT a
	// downgrade.
	//
	// A validated frame never reaches here on an empty goal set: I15 makes
	// that a validation failure rather than a default, so the goal axis
	// cannot silently vanish into row 7. This row catches the frames that
	// never became valid at all.
	default:
		return FamilyProjection{QuestionFamilyUnclassified, FamilyProjectionRowNone}
	}
}
