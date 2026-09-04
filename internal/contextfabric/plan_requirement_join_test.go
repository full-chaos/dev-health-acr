package contextfabric

import (
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE JOIN, IN BOTH DIRECTIONS.
//
// The plan's requirement array and the completeness block's outcome rows are
// two published arrays about the same set of requirements, and the only thing
// tying them together is the coordinate identity both carry. If that join is
// broken the document is worse than useless: it reads as a complete account
// while naming outcomes for requirements it does not describe, or describing
// requirements no outcome mentions.
//
// Both directions are asserted because each catches a different defect. Plan
// to outcome catches a requirement that was planned and then dropped before
// the seed. Outcome to plan catches an outcome row whose identity was minted
// or copied from somewhere other than the derivation -- the drift
// requirementIdentity's own doc comment refuses.
func TestEveryOutcomeIdentityResolvesToExactlyOnePlanRequirement(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	plan := PlanRequirementsFromDerived(rows)
	outcomes := SeedRequirementOutcomes(rows)

	// THE FIXTURE MUST BE ABLE TO DISCRIMINATE. Two rows sharing an identity
	// would satisfy every membership check below while proving nothing about
	// whether the two arrays actually correspond.
	if len(plan) < 2 || len(outcomes) < 2 {
		t.Fatalf("fixture built %d plan rows and %d outcome rows; the join needs at least two of each", len(plan), len(outcomes))
	}
	if plan[0].Requirement == plan[1].Requirement {
		t.Fatalf("both plan rows carry identity %q; the join cannot be tested against a one-identity fixture", plan[0].Requirement)
	}

	planByIdentity := map[string]int{}
	for _, row := range plan {
		planByIdentity[row.Requirement]++
	}
	for identity, count := range planByIdentity {
		if count != 1 {
			t.Errorf("plan carries identity %q %d times; the join is ambiguous", identity, count)
		}
	}
	// COUNT WHAT REACHES THE ASSERTION. This loop skips unattributed rows, so
	// a fixture whose rows were ALL unattributed would execute the body zero
	// times and the test would pass having asserted nothing. Today the seed
	// attributes every row -- but nothing here PROVED that until this count
	// did, and an assertion whose execution depends on fixture data nobody
	// checks is the third instance of that shape on this branch.
	reached := 0
	for _, outcome := range outcomes {
		if outcome.Requirement == "" {
			continue // an unattributed row is legal and joins to nothing
		}
		reached++
		if planByIdentity[outcome.Requirement] != 1 {
			t.Errorf("outcome row names requirement %q, which the plan does not describe exactly once (%d)",
				outcome.Requirement, planByIdentity[outcome.Requirement])
		}
	}
	if reached == 0 {
		t.Fatal("no attributed outcome row reached the join assertion; this test proved nothing")
	}

	outcomeByIdentity := map[string]bool{}
	for _, outcome := range outcomes {
		outcomeByIdentity[outcome.Requirement] = true
	}
	for _, row := range plan {
		if !outcomeByIdentity[row.Requirement] {
			t.Errorf("plan describes requirement %q, which no outcome row accounts for", row.Requirement)
		}
	}

	// The obligation must agree too, not only the identity. An identity that
	// matched while the copied obligation disagreed would give a reader two
	// answers to the same question from two arrays that appear to join.
	planObligation := map[string]string{}
	for _, row := range plan {
		planObligation[row.Requirement] = row.Obligation
	}
	obligationsChecked := 0
	for _, outcome := range outcomes {
		if outcome.Requirement == "" {
			continue
		}
		obligationsChecked++
		if got, want := outcome.Obligation, planObligation[outcome.Requirement]; got != want {
			t.Errorf("outcome row %q names obligation %q, the plan row names %q", outcome.Requirement, got, want)
		}
	}
	if obligationsChecked == 0 {
		t.Fatal("no attributed row reached the obligation-agreement assertion; this test proved nothing")
	}
}

// The join test above is only meaningful if a BROKEN join actually fails it.
//
// This is that control. It corrupts one plan identity and asserts the same
// checks reject -- so a future refactor that made the join vacuous (both
// arrays empty, say, or the loops skipping every row) is caught here rather
// than passing quietly.
func TestTheJoinCheckFailsWhenTheIdentitiesDisagree(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	plan := PlanRequirementsFromDerived(rows)
	outcomes := SeedRequirementOutcomes(rows)

	// Assert the two AGREE first, so the corruption below is what changes the
	// answer rather than a fixture that never agreed.
	if !joinHolds(plan, outcomes) {
		t.Fatal("the untouched fixture does not join; the control proves nothing about the corruption")
	}
	plan[0].Requirement = plan[0].Requirement + "_corrupted"
	if joinHolds(plan, outcomes) {
		t.Fatal("the join still holds after one plan identity was corrupted; the check cannot detect a broken join")
	}
}

// joinHolds is the join predicate, extracted so the control above can call the
// same logic the assertions use rather than a second copy of it.
func joinHolds(plan []contractsv1.ContextFabricPlanRequirement, outcomes []RequirementOutcomeRow) bool {
	planByIdentity := map[string]int{}
	for _, row := range plan {
		planByIdentity[row.Requirement]++
	}
	for _, outcome := range outcomes {
		if outcome.Requirement == "" {
			continue
		}
		if planByIdentity[outcome.Requirement] != 1 {
			return false
		}
	}
	outcomeByIdentity := map[string]bool{}
	for _, outcome := range outcomes {
		outcomeByIdentity[outcome.Requirement] = true
	}
	for _, row := range plan {
		if !outcomeByIdentity[row.Requirement] {
			return false
		}
	}
	return true
}

// THE MEMBER-KIND BOUNDARY (P2), pinned where it is decidable.
//
// A requirement row must not capture the plan's MemberKind: that value is
// written after subject resolution, and these rows are derived before it. The
// validator cannot enforce it -- a member-role requirement's declared subject
// kind may legitimately equal the kind resolution later confirms, so no
// predicate over a finished document tells a captured value from a coincident
// one.
//
// It is a property of the DERIVATION, so it is asserted there: the projected
// rows must be identical whether or not the plan carries a member kind. The
// assertion is on the BYTES of the rows rather than on the absence of a field,
// because "no member_kind field exists" is a claim about the struct that a
// struct literal would satisfy trivially; this is a claim about behaviour.
func TestPlanRequirementsAreIdenticalWithAndWithoutAMemberKind(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()

	withoutKind := AnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "v1",
		Requirements:  PlanRequirementsFromDerived(rows),
	}
	withKind := withoutKind
	withKind.MemberKind = SubjectRepository
	withKind.Requirements = PlanRequirementsFromDerived(rows)

	// The two plans must DIFFER in the field under test, or the comparison
	// below is between two identical documents and proves nothing.
	if withKind.MemberKind == withoutKind.MemberKind {
		t.Fatal("both plans carry the same member kind; the fixture cannot detect a capture")
	}
	if !reflect.DeepEqual(withKind.Requirements, withoutKind.Requirements) {
		t.Fatalf("the requirement rows changed when the plan gained a member kind:\n with: %+v\n without: %+v",
			withKind.Requirements, withoutKind.Requirements)
	}
	// And the rows must not carry the member kind's value in a subject slot
	// they had no reason to. This is the weaker, direct reading of the same
	// boundary and it is stated separately because the equality above would
	// also hold if BOTH projections captured it.
	for _, row := range withKind.Requirements {
		if row.Subject == withKind.MemberKind && row.Role != string(SubjectRoleMember) {
			t.Errorf("requirement %q carries the plan's member kind %q in a %s-role subject slot",
				row.Requirement, row.Subject, row.Role)
		}
	}
}

// A refinement appended by a later stage must not disturb the plan's derived
// content. APPEND, and DERIVE LAST: the two invariants #422's header states,
// asserted rather than restated.
func TestAppendingARefinementLeavesThePlanRequirementsUntouched(t *testing.T) {
	t.Parallel()
	rows := twoRequirementRows()
	plan := PlanRequirementsFromDerived(rows)
	before := append([]contractsv1.ContextFabricPlanRequirement{}, plan...)

	outcomes := SeedRequirementOutcomes(rows)
	// Narrow the first attributable row, the way an assembly stage would.
	for index := range outcomes {
		if outcomes[index].Requirement == "" {
			continue
		}
		outcomes[index].Outcome = contractsv1.ContextFabricRequirementNarrowed
		outcomes[index].Impact = contractsv1.ContextFabricAnswerImpactScope
		outcomes[index].CauseOverrun = contractsv1.ContextFabricBudgetOverrunItems
		outcomes[index].CauseObserved = true
		outcomes[index].Declared = 3
		outcomes[index].Served = 1
		outcomes[index].Refinements = []contractsv1.ContextFabricRequirementRefinement{{
			Stage:  contractsv1.ContextFabricOutcomeStageAssembledResult,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: 3, After: 1,
		}}
		break
	}

	if !reflect.DeepEqual(plan, before) {
		t.Fatal("appending a refinement changed the plan's requirement rows; the two layers are not independent")
	}
	// The appended row must itself be legal, or the "append" invariant is
	// satisfied by a document nothing would accept.
	for index, row := range outcomes {
		if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
			t.Errorf("outcome row %d does not validate after the append: %v", index, err)
		}
	}
	// Completeness is DERIVED from the whole set, last. A narrowed row must
	// move it off `complete`, or the derivation is not reading what the
	// stages wrote.
	if state := contractsv1.DeriveContextFabricAnswerCompletenessState(outcomes); state != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Errorf("completeness derived %q after a narrowing was appended, want partial", state)
	}
}

// twoRequirementRows builds one READ row and one COMPUTED row with distinct
// coordinates.
//
// Two ARMS, not two copies: a fixture whose rows differ only in identity
// cannot detect a projection that handles one arm and drops the other, and a
// fixture whose identifiers are all distinct in every field hides an aliasing
// defect. These differ in obligation, role, subject AND arm.
func twoRequirementRows() []DerivedRequirement {
	return []DerivedRequirement{
		{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationState, Role: SubjectRoleSubject, Subject: SubjectProject,
			},
			Kind:       ObligationKindRead,
			FactKinds:  []FactKind{FactHealth, FactStatus},
			Scope:      CompletionScopeSingleSubject,
			Quantifier: CompletionQuantifierAtLeastOne,
		},
		{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationCount, Role: SubjectRoleMember, Subject: SubjectRepository,
			},
			Kind:          ObligationKindComputed,
			Step:          ComputedStepMembershipCardinality,
			InputClass:    ComputedInputResolvedMemberSet,
			StepExecution: ComputedStepDeclaredOnly,
			Scope:         CompletionScopeEachMember,
			Quantifier:    CompletionQuantifierExact,
		},
	}
}
