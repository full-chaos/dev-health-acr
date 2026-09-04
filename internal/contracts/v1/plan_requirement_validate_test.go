package v1

import (
	"strings"
	"testing"
)

// The validator's own breach tests.
//
// THE MUTATION BATTERY IS WHY THIS FILE EXISTS. Every guard below was written
// with the layer and then found UNPINNED: deleting the exactly-one-server
// invariant, the identity agreement check, the uniqueness check, the
// refinement chain's endpoints, its continuity and its non-reduction refusal
// each left the whole suite green. The rows were exercised only by fixtures
// that satisfy them, which proves the happy path and nothing else.
//
// Each case asserts the rejection names THAT bound and then ATTRIBUTES it:
// restoring only the breached field must make the document valid again. Either
// check alone has a hole. A message check alone passes when an unrelated
// clause rejects; an attribution check alone passes when the right field was
// breached and the wrong rule caught it. Both together are what make a breach
// proof mean what it says.

// validReadRequirement is a well-formed READ row: fact kinds, no step, no
// unavailable reason.
func validReadRequirement() ContextFabricPlanRequirement {
	return ContextFabricPlanRequirement{
		Requirement: "state/subject/project",
		Obligation:  "state",
		Role:        "subject",
		Subject:     ContextFabricSubjectProject,
		Kind:        "read",
		FactKinds:   []ContextFabricFactKind{ContextFabricFactHealth},
		Scope:       "single_subject",
		Quantifier:  "at_least_one",
	}
}

// validComputedRequirement is a well-formed COMPUTED row: a step, an input
// class, an execution declaration, and no serving fact kinds of its own.
func validComputedRequirement() ContextFabricPlanRequirement {
	return ContextFabricPlanRequirement{
		Requirement:   "count/member/repository",
		Obligation:    "count",
		Role:          "member",
		Subject:       ContextFabricSubjectRepository,
		Kind:          "computed",
		Step:          "membership_cardinality",
		InputClass:    "resolved_member_set",
		StepExecution: "server_executed",
		Scope:         "each_member",
		Quantifier:    "exact",
	}
}

// validUnavailableRequirement is a well-formed UNAVAILABLE row.
func validUnavailableRequirement() ContextFabricPlanRequirement {
	return ContextFabricPlanRequirement{
		Requirement: "ranking/subject/organization",
		Obligation:  "ranking",
		Role:        "subject",
		Subject:     ContextFabricSubjectOrganization,
		Kind:        "computed",
		Scope:       "single_subject",
		Quantifier:  "none",
		Unavailable: "computed_population_absent",
	}
}

// TestTheValidBaselineRequirementsValidate is the POSITIVE CONTROL for every
// breach below.
//
// Without it a breach test proves only that some document is rejected: if the
// baseline were itself invalid, every mutation of it would reject too and the
// whole file would pass while testing nothing.
func TestTheValidBaselineRequirementsValidate(t *testing.T) {
	t.Parallel()
	for name, row := range map[string]ContextFabricPlanRequirement{
		"read":        validReadRequirement(),
		"computed":    validComputedRequirement(),
		"unavailable": validUnavailableRequirement(),
	} {
		if err := row.Validate(); err != nil {
			t.Errorf("the %s baseline must be valid, or every breach below proves nothing: %v", name, err)
		}
	}
	// The three baselines must DIFFER in which server arm they use, or the
	// arm-specific breaches below are all testing one shape.
	if validReadRequirement().Kind == validComputedRequirement().Kind {
		t.Fatal("the read and computed baselines carry the same kind; the arm distinction is untested")
	}
}

func TestPlanRequirementBreachesAreRejectedByTheirOwnBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// breach mutates a valid row into an invalid one.
		breach func() ContextFabricPlanRequirement
		// want is a fragment of the message THAT bound's clause emits.
		want string
	}{
		{
			name: "a read row that also names a step has two servers",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Step = "rank_cohort"
				return row
			},
			want: "must have exactly one of fact_kinds, step or unavailable",
		},
		{
			name: "a row with no server at all",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.FactKinds = nil
				return row
			},
			want: "must have exactly one of fact_kinds, step or unavailable",
		},
		{
			name: "the identity disagrees with the row's own role",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Role = "operand"
				return row
			},
			want: "disagrees with its own obligation/role/subject",
		},
		{
			name: "the identity disagrees with the row's own subject kind",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Subject = ContextFabricSubjectTeam
				return row
			},
			want: "disagrees with its own obligation/role/subject",
		},
		{
			name: "the identity is not a three-part coordinate",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Requirement = "state/subject"
				return row
			},
			want: "is not an obligation/role/subject coordinate",
		},
		{
			name: "a role from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Role = "custodian"
				row.Requirement = "state/custodian/project"
				return row
			},
			want: "role \"custodian\" is not a vocabulary member",
		},
		{
			name: "a kind from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Kind = "inferred"
				return row
			},
			want: "kind \"inferred\" is not a vocabulary member",
		},
		{
			name: "a scope from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Scope = "each_universe"
				return row
			},
			want: "scope \"each_universe\" is not a vocabulary member",
		},
		{
			name: "a quantifier from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.Quantifier = "most"
				return row
			},
			want: "quantifier \"most\" is not a vocabulary member",
		},
		{
			name: "a step from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.Step = "guess_the_answer"
				return row
			},
			want: "step \"guess_the_answer\" is not a vocabulary member",
		},
		{
			name: "an execution declaration from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.StepExecution = "probably"
				return row
			},
			want: "step_execution \"probably\" is not a vocabulary member",
		},
		{
			name: "an input class from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.InputClass = "vibes"
				return row
			},
			want: "input_class \"vibes\" is not a vocabulary member",
		},
		{
			name: "an unavailable reason from no vocabulary",
			breach: func() ContextFabricPlanRequirement {
				row := validUnavailableRequirement()
				row.Unavailable = "because"
				return row
			},
			want: "unavailable reason \"because\" is not a vocabulary member",
		},
		{
			name: "a computed row declaring no input class",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.InputClass = ""
				return row
			},
			want: "must declare both an input class and an execution",
		},
		{
			name: "a computed row declaring no execution",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.StepExecution = ""
				return row
			},
			want: "must declare both an input class and an execution",
		},
		{
			name: "input class fact_kinds naming no kinds",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.InputClass = "fact_kinds"
				return row
			},
			want: "disagrees with its 0 declared input fact kinds",
		},
		{
			name: "input class resolved_member_set naming kinds anyway",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.InputFactKinds = []ContextFabricFactKind{ContextFabricFactHealth}
				return row
			},
			want: "disagrees with its 1 declared input fact kinds",
		},
		{
			name: "a step on a row whose kind is not computed",
			breach: func() ContextFabricPlanRequirement {
				row := validComputedRequirement()
				row.Kind = "read"
				return row
			},
			want: "names a server step but its kind is",
		},
		{
			name: "an unavailable row claiming a completion",
			breach: func() ContextFabricPlanRequirement {
				row := validUnavailableRequirement()
				row.Quantifier = "all"
				return row
			},
			want: "is unavailable and must carry quantifier",
		},
		{
			name: "an unavailable row declaring computation inputs",
			breach: func() ContextFabricPlanRequirement {
				row := validUnavailableRequirement()
				row.InputClass = "resolved_member_set"
				return row
			},
			want: "is unavailable and must declare no computation inputs or execution",
		},
		{
			name: "a read row declaring computation inputs",
			breach: func() ContextFabricPlanRequirement {
				row := validReadRequirement()
				row.StepExecution = "server_executed"
				return row
			},
			want: "is a read and must declare no computation inputs or execution",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.breach().Validate()
			if err == nil {
				t.Fatalf("the breach was accepted; the bound is not enforced")
			}
			// THE REASON. A rejection by some other clause would satisfy a
			// bare err != nil and prove nothing about this bound.
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("rejected for the wrong reason:\n got:  %v\n want a message containing: %q", err, testCase.want)
			}
		})
	}
}

// ATTRIBUTION, the check the message test cannot make.
//
// Several clauses in this validator emit ONE message for a whole family of
// fields, so a breach of the wrong field in the same family produces an
// identical message and passes the reason check above. Restoring only the
// breached field and asserting the row becomes valid again is what closes
// that hole.
func TestRestoringOnlyTheBreachedFieldMakesTheRequirementValidAgain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		breach  func(*ContextFabricPlanRequirement)
		restore func(*ContextFabricPlanRequirement)
		base    func() ContextFabricPlanRequirement
	}{
		{
			name:    "two servers",
			base:    validReadRequirement,
			breach:  func(r *ContextFabricPlanRequirement) { r.Step = "rank_cohort" },
			restore: func(r *ContextFabricPlanRequirement) { r.Step = "" },
		},
		{
			name:    "identity role disagreement",
			base:    validReadRequirement,
			breach:  func(r *ContextFabricPlanRequirement) { r.Role = "operand" },
			restore: func(r *ContextFabricPlanRequirement) { r.Role = "subject" },
		},
		{
			name:    "unavailable row claiming a completion",
			base:    validUnavailableRequirement,
			breach:  func(r *ContextFabricPlanRequirement) { r.Quantifier = "all" },
			restore: func(r *ContextFabricPlanRequirement) { r.Quantifier = "none" },
		},
		{
			name:    "input class disagreeing with the declared kinds",
			base:    validComputedRequirement,
			breach:  func(r *ContextFabricPlanRequirement) { r.InputClass = "fact_kinds" },
			restore: func(r *ContextFabricPlanRequirement) { r.InputClass = "resolved_member_set" },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			row := testCase.base()
			if err := row.Validate(); err != nil {
				t.Fatalf("the baseline is invalid; attribution proves nothing: %v", err)
			}
			testCase.breach(&row)
			if err := row.Validate(); err == nil {
				t.Fatal("the breach was accepted")
			}
			testCase.restore(&row)
			if err := row.Validate(); err != nil {
				t.Fatalf("restoring only the breached field must make the row valid again; "+
					"it did not, so the rejection was attributable to something else: %v", err)
			}
		})
	}
}

// The array-level rules: the count bound and identity UNIQUENESS.
//
// Uniqueness cannot live on the row -- two rows can each be well formed and
// still make the outcome join ambiguous -- so it needs its own case here.
func TestPlanRequirementArrayRulesAreEnforced(t *testing.T) {
	t.Parallel()
	// Positive control first: a two-row array with DISTINCT identities is
	// accepted, so the rejection below is caused by the duplication and not
	// by the array shape.
	distinct := []ContextFabricPlanRequirement{validReadRequirement(), validComputedRequirement()}
	if distinct[0].Requirement == distinct[1].Requirement {
		t.Fatal("the two baseline rows share an identity; the duplicate case below cannot discriminate")
	}
	if err := ValidateContextFabricPlanRequirements(distinct); err != nil {
		t.Fatalf("a distinct two-row array must validate: %v", err)
	}

	duplicated := []ContextFabricPlanRequirement{validReadRequirement(), validReadRequirement()}
	err := ValidateContextFabricPlanRequirements(duplicated)
	if err == nil {
		t.Fatal("a repeated identity was accepted; the outcome join would be ambiguous")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}

	overCount := make([]ContextFabricPlanRequirement, 0, ContextFabricPlanRequirementsMaxCount+1)
	for i := 0; i <= ContextFabricPlanRequirementsMaxCount; i++ {
		row := validReadRequirement()
		// Distinct identities, so the COUNT bound is what rejects rather
		// than the uniqueness rule reached first.
		row.Obligation = "state"
		row.Requirement = "state/subject/project"
		overCount = append(overCount, row)
	}
	if err := ValidateContextFabricPlanRequirements(overCount); err == nil {
		t.Fatal("an over-count array was accepted")
	} else if !strings.Contains(err.Error(), "more than the") {
		t.Fatalf("the over-count array was rejected for the wrong reason: %v", err)
	}
}

// The refinement chain: the vocabulary, the arithmetic, and the reconciliation
// with the row's own two numbers.
func TestRequirementRefinementChainIsEnforced(t *testing.T) {
	t.Parallel()
	// A row that lost something, with a chain that reconciles: 5 declared,
	// 2 served, two steps accounting for the whole reduction.
	base := func() ContextFabricPlanRequirementOutcomeRow {
		return ContextFabricPlanRequirementOutcomeRow{
			Stage:        ContextFabricOutcomeStageAssembledResult,
			Requirement:  "state/subject/project",
			Obligation:   "state",
			Outcome:      ContextFabricRequirementNarrowed,
			Impact:       ContextFabricAnswerImpactScope,
			CauseOverrun: ContextFabricBudgetOverrunItems,
			// Observed, because the overrun above is a mechanism-reported
			// cause and a defaulted one must not read as an observed one.
			CauseObserved: true,
			Declared:      5,
			Served:        2,
			Refinements: []ContextFabricRequirementRefinement{
				{Stage: ContextFabricOutcomeStageAssembledResult, Basis: ContextFabricNarrowingBasisCanonicalIDLexical, Before: 5, After: 3},
				{Stage: ContextFabricOutcomeStageProjection, Basis: ContextFabricNarrowingBasisCanonicalIDLexical, Before: 3, After: 2},
			},
		}
	}
	// POSITIVE CONTROL.
	if err := ValidateContextFabricPlanRequirementOutcomeRow(base()); err != nil {
		t.Fatalf("the reconciling baseline must be valid, or every breach below proves nothing: %v", err)
	}

	cases := []struct {
		name   string
		breach func(*ContextFabricPlanRequirementOutcomeRow)
		want   string
	}{
		{
			name: "the chain does not start where the row declared",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[0].Before = 4
			},
			want: "refinement chain begins at 4 but the row declared 5",
		},
		{
			name: "the chain does not end where the row served",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[1].After = 1
			},
			want: "refinement chain ends at 1 but the row served 2",
		},
		{
			name: "a gap between two steps",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[1].Before = 4
				r.Refinements[1].After = 2
			},
			want: "the chain is broken",
		},
		{
			name: "a step that reduced nothing",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[0].After = 5
				r.Refinements[1].Before = 5
			},
			want: "which is not a reduction",
		},
		{
			name: "a step that grew the count",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[0].Before = 2
				r.Refinements[0].After = 3
			},
			want: "must reduce a non-negative count",
		},
		{
			name: "a stage from no vocabulary",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[0].Stage = "later"
			},
			want: "refinement stage \"later\" is not a vocabulary member",
		},
		{
			name: "a refinement naming no cause at all",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				// Basis is OPTIONAL, so an empty one is legal on its own --
				// but a reduction must still say what forced it, or the row
				// is the generic truncation this layer replaces.
				r.Refinements[0].Basis = ""
				r.Refinements[0].Overrun = ""
			},
			want: "names no cause; a reduction must say what forced it",
		},
		{
			name: "an overrun from no vocabulary",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[0].Overrun = "disk_space"
			},
			want: "refinement overrun \"disk_space\" is not a vocabulary member",
		},
		{
			name: "a basis from no vocabulary",
			breach: func(r *ContextFabricPlanRequirementOutcomeRow) {
				r.Refinements[0].Basis = "whatever_fit"
			},
			want: "refinement basis \"whatever_fit\" is not a vocabulary member",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			row := base()
			testCase.breach(&row)
			err := ValidateContextFabricPlanRequirementOutcomeRow(row)
			if err == nil {
				t.Fatal("the breach was accepted; the rule is not enforced")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("rejected for the wrong reason:\n got:  %v\n want a message containing: %q", err, testCase.want)
			}
		})
	}
}

// A row that lost NOTHING must record no refinement: allowing one would let
// `satisfied` carry a reduction.
func TestALosslessOutcomeRowMayNotRecordARefinement(t *testing.T) {
	t.Parallel()
	row := ContextFabricPlanRequirementOutcomeRow{
		Stage:       ContextFabricOutcomeStagePlanning,
		Requirement: "state/subject/project",
		Obligation:  "state",
		Outcome:     ContextFabricRequirementSatisfied,
		Impact:      ContextFabricAnswerImpactNone,
	}
	// POSITIVE CONTROL: valid before the refinement is attached.
	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the satisfied baseline must be valid: %v", err)
	}
	row.Refinements = []ContextFabricRequirementRefinement{
		{Stage: ContextFabricOutcomeStagePlanning, Basis: ContextFabricNarrowingBasisCanonicalIDLexical, Before: 2, After: 1},
	}
	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a satisfied row carrying a refinement was accepted")
	}
	if !strings.Contains(err.Error(), "lost nothing and must record no refinement") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}
}

// The refinement count bound, which is the outcome-stage vocabulary's size
// rather than a number chosen for it.
func TestTooManyRefinementsAreRejected(t *testing.T) {
	t.Parallel()
	declared := ContextFabricRequirementRefinementMaxCount + 2
	row := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStageAssembledResult, Requirement: "state/subject/project",
		Obligation: "state", Outcome: ContextFabricRequirementNarrowed,
		Impact: ContextFabricAnswerImpactScope, CauseOverrun: ContextFabricBudgetOverrunItems,
		CauseObserved: true, Declared: declared, Served: 1,
	}
	before := declared
	stages := ContextFabricOutcomeStageVocabulary()
	for i := 0; i <= ContextFabricRequirementRefinementMaxCount; i++ {
		after := before - 1
		row.Refinements = append(row.Refinements, ContextFabricRequirementRefinement{
			Stage: stages[i%len(stages)], Basis: ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: before, After: after,
		})
		before = after
	}
	row.Served = before
	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a chain longer than the bound was accepted")
	}
	if !strings.Contains(err.Error(), "more than the") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}
}

// THE JOIN, enforced at the DOCUMENT level.
//
// Round 1 finding: the two arrays were each validated alone, so a document
// whose plan described one requirement while its outcome row named a
// different one passed. Both halves were individually well formed; nothing
// compared them. Publishing two joinable arrays and never checking the join
// is publishing a relationship that holds only by construction.
func TestTheRequirementJoinIsEnforcedOnTheWholeDocument(t *testing.T) {
	t.Parallel()
	planned := validReadRequirement()

	// POSITIVE CONTROL: the matching pair validates. Without it the breach
	// below could be rejected by any unrelated rule and read as success.
	matched := resultWithRequirementJoin(t, planned, planned.Requirement)
	if err := matched.Validate(); err != nil {
		t.Fatalf("a document whose plan and outcome name the SAME requirement must be valid: %v", err)
	}

	// The breach: same obligation, DIFFERENT subject kind, so the identity
	// differs in exactly one segment and every per-array rule still holds.
	mismatched := resultWithRequirementJoin(t, planned, "state/subject/repository")
	err := mismatched.Validate()
	if err == nil {
		t.Fatal("a document naming an outcome for a requirement its own plan does not describe was accepted")
	}
	if !strings.Contains(err.Error(), "which the answer plan does not describe") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}

	// ATTRIBUTION: restore only the identity and the document is valid again,
	// so the rejection was caused by the join and not by something else the
	// mutation disturbed.
	restored := resultWithRequirementJoin(t, planned, planned.Requirement)
	if err := restored.Validate(); err != nil {
		t.Fatalf("restoring the identity must make the document valid again: %v", err)
	}
}

// The OTHER direction: a planned requirement no outcome accounts for.
func TestAPlannedRequirementWithNoOutcomeIsRejected(t *testing.T) {
	t.Parallel()
	planned := validReadRequirement()
	other := validComputedRequirement()

	doc := resultWithRequirementJoin(t, planned, planned.Requirement)
	// Add a SECOND plan requirement that no outcome row mentions. The two
	// must differ, or this is the matched case again.
	if planned.Requirement == other.Requirement {
		t.Fatal("the two plan rows share an identity; this direction cannot be tested")
	}
	doc.AnswerPlan.Requirements = append(doc.AnswerPlan.Requirements, other)

	err := doc.Validate()
	if err == nil {
		t.Fatal("a plan requirement that no outcome row accounts for was accepted")
	}
	if !strings.Contains(err.Error(), "which no outcome row accounts for") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}
}

// The join must be VACUOUS, not violated, where either array is absent: a turn
// that derived nothing has neither, and a result written before this layer
// existed has neither.
func TestTheJoinIsVacuousWhenEitherArrayIsAbsent(t *testing.T) {
	t.Parallel()
	planned := validReadRequirement()

	noPlanRows := resultWithRequirementJoin(t, planned, planned.Requirement)
	noPlanRows.AnswerPlan.Requirements = nil
	if err := noPlanRows.Validate(); err != nil {
		t.Errorf("a document with outcome rows and no plan requirements must stay valid: %v", err)
	}

	// An unattributed row beside a plan that describes NOTHING is legal --
	// there is no requirement for it to have failed to account for.
	unattributedNoPlan := resultWithRequirementJoin(t, planned, "")
	unattributedNoPlan.AnswerPlan.Requirements = nil
	if err := unattributedNoPlan.Validate(); err != nil {
		t.Errorf("an unattributed row beside an empty plan must stay valid: %v", err)
	}
}

// ALL-UNATTRIBUTED BESIDE A POPULATED PLAN IS NOT AN HONEST ABSENCE.
//
// This case was BLESSED by an earlier revision of the test above, which is
// worse than it being merely unchecked: a test that asserts an invalid shape
// is valid actively defends the gap. The review round found it by reading the
// escape in the validator and the test that protected it together.
//
// The seed writes one attributed row per derived requirement, so a document
// whose plan describes requirements while NO outcome row names any of them
// has lost the seed. Reporting that as a legal unattributed narrowing would
// let exactly the "planned then dropped" case the join exists to catch pass.
func TestAllUnattributedOutcomesBesideAPopulatedPlanIsRejected(t *testing.T) {
	t.Parallel()
	planned := validReadRequirement()

	// POSITIVE CONTROL: the attributed pair is valid, so the rejection below
	// is caused by the attribution and not by the fixture's shape.
	attributed := resultWithRequirementJoin(t, planned, planned.Requirement)
	if err := attributed.Validate(); err != nil {
		t.Fatalf("the attributed baseline must be valid: %v", err)
	}

	unattributed := resultWithRequirementJoin(t, planned, "")
	if len(unattributed.AnswerPlan.Requirements) == 0 {
		t.Fatal("the fixture's plan carries no requirements; this case cannot discriminate")
	}
	err := unattributed.Validate()
	if err == nil {
		t.Fatal("a document whose plan describes a requirement no outcome row accounts for was accepted")
	}
	if !strings.Contains(err.Error(), "which no outcome row accounts for") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}
}

// resultWithRequirementJoin builds a VALID minimal document carrying one plan
// requirement and one outcome row, with the outcome's identity supplied by the
// caller so a test can make the two agree or disagree.
//
// It starts from the irreducible fixture rather than a hand-built result: that
// fixture is derived from the bound table and is known valid, so a failure
// here is attributable to the join rather than to a field this helper forgot.
// A hand-built document would fail for reasons unrelated to what is under test
// and read as a passing breach.
func resultWithRequirementJoin(t *testing.T, planned ContextFabricPlanRequirement, outcomeIdentity string) ContextFabricInvestigationResult {
	t.Helper()
	r := buildFromTable(t, func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Min })

	plan := ContextFabricAnswerPlan{
		Family:        ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource:  ContextFabricQuestionFamilySourceStructurePrecedence,
		FamilyVersion: "join-v1",
		Requirements:  []ContextFabricPlanRequirement{planned},
	}
	r.AnswerPlan = &plan

	row := ContextFabricPlanRequirementOutcomeRow{
		Stage:   ContextFabricOutcomeStagePlanning,
		Outcome: ContextFabricRequirementSatisfied,
		Impact:  ContextFabricAnswerImpactNone,
	}
	if outcomeIdentity != "" {
		row.Requirement = outcomeIdentity
		// The row's own rule requires obligation and identity to be present
		// together and to agree on the first segment, so derive the
		// obligation FROM the identity rather than hardcoding it -- a
		// hardcoded one would make this helper reject for its own reason.
		row.Obligation = strings.SplitN(outcomeIdentity, "/", 2)[0]
	}
	r.Completeness.Outcomes = []ContextFabricPlanRequirementOutcomeRow{row}
	r.Completeness.State = DeriveContextFabricAnswerCompletenessState(r.Completeness.Outcomes)
	return r
}

// THE DERIVATION ITSELF, which the battery found unpinned twice.
//
// ContextFabricReductionRefinement is now the single authority for "what step
// does this row imply", and two of its properties had no test: that it refuses
// a row describing no reduction, and that it carries a COVERAGE cause. Both
// mutants survived a battery run, which is the same evidence as a defect --
// the property a green suite does not pin is the one the next edit breaks.
func TestTheReductionDerivationRefusesRowsThatReducedNothing(t *testing.T) {
	t.Parallel()
	// A row whose counts are EQUAL described no reduction. Deriving a step
	// from it would emit before == after, which the refinement validator
	// rejects -- so the guard is what keeps a non-reduction from becoming an
	// invalid document rather than merely an untidy one.
	equal := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStageAssembledResult, Requirement: "state/subject/project",
		Obligation: "state", Outcome: ContextFabricRequirementNarrowed,
		Impact: ContextFabricAnswerImpactScope, CauseOverrun: ContextFabricBudgetOverrunItems,
		CauseObserved: true, Declared: 4, Served: 4,
	}
	if _, ok := ContextFabricReductionRefinement(equal); ok {
		t.Error("a row that served everything it declared produced a refinement; before == after is not a reduction")
	}

	// A SATISFIED row, likewise: it lost nothing, and the outcome row's own
	// validator refuses a refinement on it.
	satisfied := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStagePlanning, Requirement: "state/subject/project",
		Obligation: "state", Outcome: ContextFabricRequirementSatisfied,
		Impact: ContextFabricAnswerImpactNone, Declared: 4, Served: 2,
	}
	if _, ok := ContextFabricReductionRefinement(satisfied); ok {
		t.Error("a satisfied row produced a refinement; it lost nothing")
	}

	// NOT_APPLICABLE, which is a DISTINCT arm of the same guard and was
	// unpinned: the test covered `satisfied` and stopped there, so deleting
	// the not_applicable arm let a caused reduction on such a row produce a
	// step. Two tokens, two arms, two assertions.
	notApplicable := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStagePlanning, Requirement: "state/subject/project",
		Obligation: "state", Outcome: ContextFabricRequirementNotApplicable,
		Impact: ContextFabricAnswerImpactNone, Declared: 4, Served: 2,
	}
	if _, ok := ContextFabricReductionRefinement(notApplicable); ok {
		t.Error("a not_applicable row produced a refinement; the question did not ask for it, so nothing was lost")
	}

	// A row naming NO cause cannot produce a step that names one. Unreachable
	// on a valid row, handled rather than assumed.
	causeless := equal
	causeless.Declared, causeless.Served = 4, 1
	causeless.CauseOverrun = ""
	causeless.CauseObserved = false
	if _, ok := ContextFabricReductionRefinement(causeless); ok {
		t.Error("a row naming no cause produced a refinement; the step would name none either")
	}

	// POSITIVE CONTROL: a real reduction DOES produce one, so the three
	// refusals above are the guard acting and not the function being inert.
	reducing := equal
	reducing.Declared, reducing.Served = 4, 1
	step, ok := ContextFabricReductionRefinement(reducing)
	if !ok {
		t.Fatal("a genuine reduction produced no refinement; the guard rejects everything and proves nothing")
	}
	if step.Before != 4 || step.After != 1 {
		t.Errorf("refinement runs %d->%d, want 4->1", step.Before, step.After)
	}
}

// EVERY ONE of the row's three causes must reach the derived step, and the
// coverage arm is the one a sweep found a real site depends on: the reuse
// degrade names a coverage code and NO ceiling and NO ordering, so a
// derivation that dropped coverage could not represent that site at all.
func TestTheReductionDerivationCarriesEachOfTheRowsCauses(t *testing.T) {
	t.Parallel()
	base := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStageReuse, Requirement: "state/subject/project",
		Obligation: "state", Outcome: ContextFabricRequirementNarrowed,
		Impact: ContextFabricAnswerImpactDepth, CauseObserved: true,
		Declared: 5, Served: 2,
	}

	coverageOnly := base
	coverageOnly.CauseCoverage = ContextFabricCoverageDetailReuseAuxiliaryRefsStripped
	step, ok := ContextFabricReductionRefinement(coverageOnly)
	if !ok {
		t.Fatal("a coverage-caused reduction produced no refinement; the reuse degrade could not be represented")
	}
	if step.Coverage != coverageOnly.CauseCoverage {
		t.Errorf("refinement coverage = %q, the row named %q", step.Coverage, coverageOnly.CauseCoverage)
	}
	// It must carry ONLY that cause -- inventing a ceiling or an ordering
	// would state a mechanism that did not run.
	if step.Overrun != "" || step.Basis != "" {
		t.Errorf("refinement invented causes the row did not name: overrun=%q basis=%q", step.Overrun, step.Basis)
	}
	if err := step.Validate(); err != nil {
		t.Errorf("a coverage-only refinement must be valid: %v", err)
	}

	// The other two arms, so this test covers the whole cause model rather
	// than the one arm that happened to be broken.
	overrunOnly := base
	overrunOnly.CauseOverrun = ContextFabricBudgetOverrunItems
	if step, ok := ContextFabricReductionRefinement(overrunOnly); !ok || step.Overrun != overrunOnly.CauseOverrun {
		t.Errorf("overrun cause did not reach the step: ok=%v step=%+v", ok, step)
	}
	basisOnly := base
	basisOnly.CauseNarrowing = ContextFabricNarrowingBasisCanonicalIDLexical
	if step, ok := ContextFabricReductionRefinement(basisOnly); !ok || step.Basis != basisOnly.CauseNarrowing {
		t.Errorf("basis cause did not reach the step: ok=%v step=%+v", ok, step)
	}

	// The three fixtures must DIFFER in which cause they set, or the three
	// assertions above are all reading the same field.
	if coverageOnly.CauseCoverage == "" || overrunOnly.CauseOverrun == "" || basisOnly.CauseNarrowing == "" {
		t.Fatal("the three cause fixtures are not distinct; this test cannot discriminate")
	}
}
