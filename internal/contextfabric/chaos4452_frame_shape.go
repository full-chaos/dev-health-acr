package contextfabric

// CHAOS-4452 stage 2 (S7b-i), design §13.8b: the legacy `Shape` becomes a
// DERIVED projection of SubjectExpression.Kind.
//
// SHADOW ONLY in this slice. The function lands here because invariant I18
// (phase A2) compares the derived Shape to the one the sampler emitted and
// records the divergence, and because the telemetry vocabulary spans
// i1...i19 from this slice onward. NOTHING IN RETRIEVAL CHANGES HERE:
// discovery, the census gate and cohort-chart intent still read
// interpretation.Shape exactly as they do on main. Making them CONSUME
// this derivation is the retrieval slice's work, and oracle O11 lands with
// it.
//
// WHY THE DERIVATION EXISTS AT ALL (§13.8b, finding R1). Today the frame
// would declare structure and the substrate would decide it from prose:
// the cohort kind comes from a substring match over RequestedJudgment +
// SubjectTerms that defaults to `team` and can never return `repository`,
// a second whole-word matcher feeds kind-hinted pool search, and discovery
// and the census gate still key on the field stage 1 itself calls "the
// unstable variable". Two authorities, and the frame is not the one
// retrieval obeys -- a direct law-L6 violation with a banned strike-three
// shape inside it.

// DeriveShape projects a SubjectExpression onto the shipped four-member
// InvestigationShape vocabulary. Pure, total, and a function of Kind
// ALONE.
//
// THE children_of_scope ROW IS LOAD-BEARING AND IT IS NOT THE OBVIOUS ONE.
// A scoped cohort is a cohort, so `discovered_cohort` reads as the natural
// mapping -- and it is the mapping the frozen design carried. Round 4
// (N2) found what it does. The org-wide kind census is ADMITTED exactly
// when Shape == discovered_cohort (falkorgraph/reader.go:736;
// chaos4622_cohort_census_gate.go:71-72), and DiscoveredCohort then keeps
// EVERY node of the requested kind. BAR question Q-B -- "What are the
// statuses of the fullchaos team's projects?" -- is protected today only
// because it arrives as explicit_cohort WITH an anchor set, which DENIES
// the census. Under the frozen mapping a scoped frame would have derived
// discovered_cohort, admitted the census, and returned EVERY PROJECT IN
// THE ORGANIZATION for a question that named one team's.
//
// So children_of_scope derives explicit_cohort, which sends scoped frames
// down the anchor-set path -- the path their hop-walked members actually
// come from. Until the census gate keys on SubjectExpression.Kind (the
// retrieval slice's acceptance), THE DERIVED SHAPE IS THE ONLY THING
// STANDING BETWEEN A SCOPED QUESTION AND THE ORG CENSUS. Oracle O11 pins
// it: for every children_of_scope frame, the census eligibility gate must
// return eligible=false.
//
// The organization_scope row is the second non-obvious one, and it is
// deliberate rather than incidental: stage 1 routed an org-wide question
// (`open`) to a cohort RANKING, which answers a question the user did not
// ask. Stage 2 routes it to a single-subject investigation on the
// organization. That routing change is behaviour change B5 and is
// DEFERRED behind a resolver-and-producer precondition -- nothing commits
// an organization subject today and only one producer serves it -- so the
// mapping is correct as a projection while the improvement claim stays
// withdrawn.
func DeriveShape(expression SubjectExpression) InvestigationShape {
	switch expression.Kind {
	case SubjectExpressionNamed:
		return ShapeSingleSubject
	case SubjectExpressionExplicitSet:
		return ShapeExplicitCohort
	case SubjectExpressionChildrenOfScope:
		// NOT discovered_cohort. See the doc comment above -- this row
		// is what denies the org-wide census for a scoped question.
		return ShapeExplicitCohort
	case SubjectExpressionDiscoveredKind:
		return ShapeDiscoveredCohort
	case SubjectExpressionGroupedMembers:
		return ShapeDiscoveredCohort
	case SubjectExpressionOrganizationScope:
		return ShapeOpen
	default:
		// An unset or unrecognized Kind has no projection. The caller is
		// invariant I1, which has already failed the frame; returning
		// the zero shape rather than guessing one keeps "refuse to
		// guess" true at every layer.
		return ""
	}
}

// ShapeDivergence records that the model's emitted Shape disagreed with
// the frame's derived one. Invariant I18: "Shape as DERIVED from the frame
// equals the Shape the sampler emitted, or the divergence is recorded and
// THE DERIVED VALUE WINS."
//
// That is the same "server validates the model's pick and downgrades
// deterministically" contract stage 1 already ships for ClassifyWindow and
// for the family, applied to the last structural field that escaped it.
// I18 is therefore NOT a rejection: a divergent frame is still valid, and
// what changes is which value downstream reads.
type ShapeDivergence struct {
	// Emitted is the sampler's own Shape. Closed enum.
	Emitted InvestigationShape
	// Derived is the frame's projection. Closed enum, and the winner.
	Derived InvestigationShape
}

// ShapeAgreement compares the emitted Shape to the derived one.
//
// An EMPTY emitted Shape is not a divergence. The subject-terms-omission
// ticket measured the interpreter dropping structured fields it is asked
// for -- 11/14 emission on a field that has been on the contract for
// months -- so treating "the model said nothing" as "the model
// contradicted us" would manufacture divergences out of a known omission
// class and drown the signal I18 exists to measure.
func ShapeAgreement(emitted InvestigationShape, expression SubjectExpression) (ShapeDivergence, bool) {
	derived := DeriveShape(expression)
	if emitted == "" || derived == "" || emitted == derived {
		return ShapeDivergence{}, false
	}
	return ShapeDivergence{Emitted: emitted, Derived: derived}, true
}
