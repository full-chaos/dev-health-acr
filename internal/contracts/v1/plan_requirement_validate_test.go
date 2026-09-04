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
