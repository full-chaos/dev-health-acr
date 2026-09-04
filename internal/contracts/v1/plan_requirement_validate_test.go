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
	// The rule became an ALLOW-LIST -- only `narrowed` may carry a refinement
	// -- so the rejection now names the outcome and the one token that may.
	// Both halves are checked: the outcome, so the message is about THIS row,
	// and the permitted token, so the message is the allow-list's and not
	// some other clause that happens to mention `satisfied`.
	if !strings.Contains(err.Error(), string(ContextFabricRequirementSatisfied)) ||
		!strings.Contains(err.Error(), string(ContextFabricRequirementNarrowed)) {
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
	// THE FIXTURE MUST CARRY A CAUSE, or this case cannot reach the arm it
	// tests. The derivation refuses a causeless row LAST; a lossless fixture
	// with no cause set is refused by that later guard whether or not the
	// outcome arm exists, so deleting the outcome arm left the test green.
	// The battery caught exactly that -- the mutant survived a run in which
	// this test executed. Such a row is not a legal DOCUMENT (a lossless row
	// must name no cause), but this is a unit test of the derivation, which
	// does not validate its input, and passing one isolates the arm.
	satisfied := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStagePlanning, Requirement: "state/subject/project",
		Obligation: "state", Outcome: ContextFabricRequirementSatisfied,
		Impact: ContextFabricAnswerImpactNone, Declared: 4, Served: 2,
		CauseOverrun: ContextFabricBudgetOverrunItems,
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
		// A cause, for the same reason the satisfied fixture above carries
		// one: without it the later causeless guard refuses this row and the
		// outcome arm goes untested.
		CauseOverrun: ContextFabricBudgetOverrunItems,
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

// THE FOUR ARMS THE COVERAGE SWEEP FOUND UNASSERTED.
//
// The sweep enumerated every error message the new guards can emit — 42 arms
// across the requirement validator, the refinement validator, the chain check
// and the join — and matched each against the fragments the tests actually
// assert. Four had no assertion. They are the length and count bounds, which
// the breach table skipped because it was built around VOCABULARY membership
// and quietly stopped there.
//
// This is the same shape as the finding that produced this file: an arm nobody
// tested, in a guard everyone assumed was covered because its neighbours were.
func TestThePlanRequirementLengthAndCountBoundsAreEnforced(t *testing.T) {
	t.Parallel()
	// POSITIVE CONTROL first, so each breach below is attributable.
	if err := validReadRequirement().Validate(); err != nil {
		t.Fatalf("the baseline must be valid: %v", err)
	}

	t.Run("an identity past its length bound", func(t *testing.T) {
		t.Parallel()
		row := validReadRequirement()
		// Past ContextFabricRequirementIdentityMaxLength while still a
		// three-segment coordinate, so the LENGTH clause rejects it rather
		// than the shape clause.
		pad := strings.Repeat("x", ContextFabricRequirementIdentityMaxLength)
		row.Requirement = "state/subject/" + pad
		row.Subject = ContextFabricSubjectKind(pad)
		err := row.Validate()
		if err == nil {
			t.Fatal("an over-long identity was accepted")
		}
		if !strings.Contains(err.Error(), "identity or obligation violates v1 bounds") {
			t.Fatalf("rejected for the wrong reason: %v", err)
		}
	})

	t.Run("more serving fact kinds than the vocabulary has", func(t *testing.T) {
		t.Parallel()
		row := validReadRequirement()
		kinds := ContextFabricFactKindVocabulary()
		// One past the bound. Repeating a member is enough: the count clause
		// is what must fire, and it is checked before per-member validity.
		row.FactKinds = append(append([]ContextFabricFactKind{}, kinds[:]...), kinds[0])
		err := row.Validate()
		if err == nil {
			t.Fatal("an over-count fact kind list was accepted")
		}
		if !strings.Contains(err.Error(), "declares more fact kinds than the closed vocabulary") {
			t.Fatalf("rejected for the wrong reason: %v", err)
		}
	})

	t.Run("more input fact kinds than the vocabulary has", func(t *testing.T) {
		t.Parallel()
		row := validComputedRequirement()
		kinds := ContextFabricFactKindVocabulary()
		row.InputClass = "fact_kinds"
		row.InputFactKinds = append(append([]ContextFabricFactKind{}, kinds[:]...), kinds[0])
		err := row.Validate()
		if err == nil {
			t.Fatal("an over-count input fact kind list was accepted")
		}
		if !strings.Contains(err.Error(), "declares more input fact kinds than the closed vocabulary") {
			t.Fatalf("rejected for the wrong reason: %v", err)
		}
	})

	t.Run("the array wrapper names WHICH row failed", func(t *testing.T) {
		t.Parallel()
		// The per-row wrapper is its own arm: without the index a caller
		// holding a 200-row array learns only that something in it is
		// invalid, which is the bare-count disclosure this layer replaces.
		bad := validReadRequirement()
		bad.Kind = "inferred"
		err := ValidateContextFabricPlanRequirements([]ContextFabricPlanRequirement{validComputedRequirement(), bad})
		if err == nil {
			t.Fatal("an array containing an invalid row was accepted")
		}
		if !strings.Contains(err.Error(), "requirement 1:") {
			t.Fatalf("the error does not name which row failed: %v", err)
		}
	})
}

// rowCarryingAReduction builds a row for `outcome` that declared 4 and served
// 1, legal in every respect EXCEPT any refinement the caller adds.
//
// One shape for every outcome, so the only thing varying across the cases
// below is the outcome token itself. The impact and cause fields follow the
// pairing rule the row validator states: the lossless outcomes must carry
// impact `none` and name NO cause, every other outcome must carry a non-none
// impact and name one.
func rowCarryingAReduction(outcome ContextFabricPlanRequirementOutcome) ContextFabricPlanRequirementOutcomeRow {
	row := ContextFabricPlanRequirementOutcomeRow{
		Stage: ContextFabricOutcomeStageAssembledResult, Requirement: "state/subject/project",
		Obligation: "state", Outcome: outcome, Declared: 4, Served: 1,
	}
	if outcome == ContextFabricRequirementSatisfied || outcome == ContextFabricRequirementNotApplicable {
		row.Impact = ContextFabricAnswerImpactNone
		return row
	}
	row.Impact = ContextFabricAnswerImpactScope
	row.CauseOverrun = ContextFabricBudgetOverrunItems
	row.CauseObserved = true
	return row
}

// ONLY `narrowed` MAY CARRY A REFINEMENT — the validator half.
//
// Written over the CLOSED VOCABULARY rather than over a hand-written list of
// tokens, because the rule this pins is an allow-list and a hand-written list
// is exactly what an allow-list is meant to stop being. A sixth outcome added
// to the vocabulary is covered by this test on the day it is added.
//
// The previous rule was a deny-list naming `satisfied` and `not_applicable`,
// so `unavailable` and `not_attempted` could carry a chain reducing a
// population they never served. The fixture below is that document: declared
// 4, served 1, chain 4 -> 1, which reconciles and was accepted.
//
// EVERY case first asserts the SAME row validates WITHOUT the refinement.
// Without that control a rejection could come from any other rule -- the
// pairing rule, the counts, the cause vocabulary -- and the test would pass
// while the rule it names does nothing.
func TestOnlyNarrowedMayCarryARefinement(t *testing.T) {
	t.Parallel()
	refinement := ContextFabricRequirementRefinement{
		Stage: ContextFabricOutcomeStageAssembledResult, Overrun: ContextFabricBudgetOverrunItems,
		Before: 4, After: 1,
	}
	accepted, rejected := 0, 0
	for _, outcome := range ContextFabricPlanRequirementOutcomeVocabulary() {
		base := rowCarryingAReduction(outcome)
		if err := ValidateContextFabricPlanRequirementOutcomeRow(base); err != nil {
			t.Fatalf("the control row for outcome %q does not validate without a refinement (%v); "+
				"a rejection below could not be attributed to the refinement rule", outcome, err)
		}
		withRefinement := base
		withRefinement.Refinements = []ContextFabricRequirementRefinement{refinement}
		err := ValidateContextFabricPlanRequirementOutcomeRow(withRefinement)

		if outcome == ContextFabricRequirementNarrowed {
			accepted++
			if err != nil {
				t.Errorf("outcome %q was refused a refinement (%v); it is the one outcome that describes a reduction", outcome, err)
			}
			continue
		}
		rejected++
		if err == nil {
			t.Errorf("outcome %q carried a refinement and validated; only %q describes a population that shrank and was still served",
				outcome, ContextFabricRequirementNarrowed)
			continue
		}
		// ATTRIBUTE it. The message must name this outcome, or the rejection
		// came from some other clause and this case proved nothing.
		if !strings.Contains(err.Error(), string(outcome)) {
			t.Errorf("outcome %q was rejected by a message that does not name it: %v", outcome, err)
		}
	}
	// The loop must have exercised BOTH answers. A vocabulary that lost
	// `narrowed`, or one whose every member were refused, would otherwise
	// leave this test asserting one half of a two-sided rule.
	if accepted != 1 {
		t.Errorf("%d outcomes accepted a refinement, want exactly 1 (%q)", accepted, ContextFabricRequirementNarrowed)
	}
	if rejected == 0 {
		t.Error("no outcome was rejected; the allow-list was never exercised")
	}
}

// ONLY `narrowed` MAY CARRY A REFINEMENT — the derivation half.
//
// Stated separately from the validator because they fail in different places:
// this one keeps a step that no outcome can carry from ever being MINTED, the
// other keeps a hand-built row from carrying one anyway. Deleting either left
// the other green, which is why both are pinned.
//
// Every row here declares 4, serves 1 and names a cause, so the derivation's
// EARLIER guards -- the non-reduction refusal and the causeless refusal -- are
// both satisfied. Without that the outcome arm is unreachable and deleting it
// changes nothing, which is the shape the mutation battery caught in this
// file's older cases.
func TestTheReductionDerivationMintsAStepOnlyForNarrowed(t *testing.T) {
	t.Parallel()
	minted, refused := 0, 0
	for _, outcome := range ContextFabricPlanRequirementOutcomeVocabulary() {
		row := rowCarryingAReduction(outcome)
		// The lossless rows must name a cause HERE, even though such a row is
		// not a legal document: the derivation refuses a causeless row in a
		// LATER guard, so a causeless fixture would be refused by that guard
		// whether or not the outcome arm exists.
		if row.CauseOverrun == "" && row.CauseNarrowing == "" && row.CauseCoverage == "" {
			row.CauseOverrun = ContextFabricBudgetOverrunItems
		}
		step, ok := ContextFabricReductionRefinement(row)

		if outcome == ContextFabricRequirementNarrowed {
			minted++
			if !ok {
				t.Errorf("outcome %q produced no refinement; it is the one outcome a reduction is derivable from", outcome)
				continue
			}
			if step.Before != row.Declared || step.After != row.Served {
				t.Errorf("outcome %q minted %d -> %d, want %d -> %d", outcome, step.Before, step.After, row.Declared, row.Served)
			}
			continue
		}
		refused++
		if ok {
			t.Errorf("outcome %q minted refinement %+v; it never served a reduced population", outcome, step)
		}
	}
	if minted != 1 {
		t.Errorf("%d outcomes minted a refinement, want exactly 1 (%q)", minted, ContextFabricRequirementNarrowed)
	}
	if refused == 0 {
		t.Error("no outcome was refused; the allow-list was never exercised")
	}
}

// THE MAXIMAL FIXTURE MUST ACTUALLY BE MAXIMAL.
//
// Every row the builder emits is individually maximal in the fields it
// CHOOSES. The array as a whole was not, because the coordinate is itself a
// byte cost -- obligation, role and subject are carried in three fields and
// again concatenated into the identity -- and the builder took the first 200
// members of a 780-member product in walk order. 580 members were never
// considered, and a longer coordinate among them made the "maximal" fixture
// smaller than a legal document.
//
// That is the one error a bound fixture must not make: everything downstream
// reads it as the ceiling, so a fixture below the true maximum turns every
// byte pin built on it into a pin on a document the service can legally
// exceed.
//
// The assertion is the selection property, not a byte count: no coordinate
// left out may be longer than one taken. A byte count would pin today's
// arithmetic and would have to be re-derived on every vocabulary change; this
// stays true across them.
//
// WHAT THIS TEST DOES NOT COVER, stated because its earlier comment implied
// otherwise and a review round found the gap. It compares only DIFFERENT
// coordinates: the pool comes from the same constructor it audits, and rows
// whose coordinate is already taken are skipped before the comparison. A
// longer row at the SAME coordinate is invisible here, and one existed --
// the kind lists were bounded by count and membership and not by uniqueness,
// so repeating the longest member was legal and longer. That axis is now a
// validator rule and is asserted by
// TestTheFullVocabularyIsTheLongestLegalKindList, which builds its expected
// maximum from the vocabulary rather than from the constructor. This test is
// the COORDINATE axis only, and says so.
func TestTheMaximalRequirementFixtureTakesTheLongestCoordinates(t *testing.T) {
	t.Parallel()
	kinds := ContextFabricFactKindVocabulary()
	obligations := ContextFabricAnswerObligationVocabulary()
	roles := ContextFabricSubjectRoleVocabulary()
	subjects := ContextFabricSubjectKindVocabulary()

	pool := make([]ContextFabricPlanRequirement, 0, len(obligations)*len(roles)*len(subjects))
	for _, obligation := range obligations {
		for _, role := range roles {
			for _, subject := range subjects {
				pool = append(pool, maximalPlanRequirementAt(obligation, role, subject, kinds[:]))
			}
		}
	}

	chosen := maximalPlanRequirements()
	if len(chosen) != ContextFabricPlanRequirementsMaxCount {
		t.Fatalf("the fixture chose %d rows, want the bound of %d", len(chosen), ContextFabricPlanRequirementsMaxCount)
	}
	// THE POOL MUST BE LARGER THAN THE CHOICE. If the product exactly filled
	// the bound there would be nothing left out, every selection rule would
	// pass, and this test would assert nothing.
	if len(pool) <= len(chosen) {
		t.Fatalf("the coordinate product has %d members and the fixture takes %d; nothing is left out and the selection is untested",
			len(pool), len(chosen))
	}

	taken := make(map[string]bool, len(chosen))
	for _, row := range chosen {
		taken[row.Requirement] = true
	}
	if len(taken) != len(chosen) {
		t.Fatalf("the fixture chose %d rows carrying %d distinct identities; the join would be ambiguous", len(chosen), len(taken))
	}

	shortestTaken, shortestIdentity := -1, ""
	for _, row := range chosen {
		if length := planRequirementSerializedLength(row); shortestTaken < 0 || length < shortestTaken {
			shortestTaken, shortestIdentity = length, row.Requirement
		}
	}
	compared := 0
	for _, row := range pool {
		if taken[row.Requirement] {
			continue
		}
		compared++
		if length := planRequirementSerializedLength(row); length > shortestTaken {
			t.Errorf("coordinate %q serializes to %d bytes and was left out, while %q at %d bytes was taken; the fixture is not maximal",
				row.Requirement, length, shortestIdentity, shortestTaken)
		}
	}
	if compared == 0 {
		t.Fatal("no unchosen coordinate reached the comparison; this test proved nothing")
	}
}

// THE KIND LISTS ARE SETS, AND THE FULL VOCABULARY IS THE MAXIMUM.
//
// This is the axis the coordinate-selection test structurally cannot see. That
// test builds its candidate pool with `maximalPlanRequirementAt` and skips any
// row whose coordinate is already taken, so it only ever compares DIFFERENT
// coordinates -- a longer row at the SAME coordinate is invisible to it. A
// review round found exactly that: `input_fact_kinds` was bounded by count and
// membership and NOT by uniqueness, so a list repeating the longest vocabulary
// member was legal and 298 bytes longer per row, 59,600 across the bound. The
// "maximal" fixture was therefore smaller than a legal document.
//
// THE EXPECTED MAXIMUM IS BUILT HERE, FROM THE VOCABULARY, BY A DIRECT STRUCT
// LITERAL. It must not come from the constructor: auditing the constructor
// with the constructor is the defect that produced this test, and repeating it
// would produce a test that agrees with whatever the builder happens to do.
//
// Under uniqueness over a closed vocabulary the argument is short: every legal
// list is a set of distinct members, so none can exceed all of them. The test
// measures that rather than restating it.
func TestTheFullVocabularyIsTheLongestLegalKindList(t *testing.T) {
	t.Parallel()
	kinds := ContextFabricFactKindVocabulary()

	// Built by hand from the vocabulary, NOT via maximalPlanRequirementAt.
	full := ContextFabricPlanRequirement{
		Requirement: "state/subject/project", Obligation: "state",
		Role: "subject", Subject: ContextFabricSubjectKind("project"),
		Kind: contextFabricObligationKindComputed,
		Step: "membership_cardinality", StepExecution: "server_executed",
		InputClass:     contextFabricComputedInputFactKinds,
		InputFactKinds: append([]ContextFabricFactKind{}, kinds[:]...),
		Scope:          "single_subject", Quantifier: "corroborated",
	}
	if err := full.Validate(); err != nil {
		t.Fatalf("the full-vocabulary row does not validate: %v", err)
	}

	// A PROPER SUBSET IS STRICTLY SHORTER, so "all of them" is measured as the
	// maximum rather than asserted. Without this the test would pass on a
	// vocabulary of one.
	if len(kinds) < 2 {
		t.Fatalf("the fact-kind vocabulary has %d members; a subset comparison needs at least two", len(kinds))
	}
	subset := full
	subset.InputFactKinds = append([]ContextFabricFactKind{}, kinds[:len(kinds)-1]...)
	if err := subset.Validate(); err != nil {
		t.Fatalf("the subset row does not validate, so the comparison is not between two legal rows: %v", err)
	}
	if planRequirementSerializedLength(subset) >= planRequirementSerializedLength(full) {
		t.Errorf("a proper subset serializes to %d bytes and the full vocabulary to %d; the full list is not the longer one",
			planRequirementSerializedLength(subset), planRequirementSerializedLength(full))
	}

	// RED PROOF THAT THE RULE BITES. The variant the review found -- the same
	// LENGTH, every entry the longest member -- must now be REJECTED, and the
	// rejection must name the duplicated kind. Without this case the test
	// passes whether or not the uniqueness rule landed at all.
	longest := ContextFabricFactKind("")
	for _, kind := range kinds {
		if len(kind) > len(longest) {
			longest = kind
		}
	}
	repeated := full
	repeated.InputFactKinds = make([]ContextFabricFactKind, len(kinds))
	for index := range repeated.InputFactKinds {
		repeated.InputFactKinds[index] = longest
	}
	// It must be the LONGER shape, or it is not the thing that broke the pin.
	if planRequirementSerializedLength(repeated) <= planRequirementSerializedLength(full) {
		t.Fatalf("the repeated-kind row is %d bytes against the full vocabulary's %d; it is not the longer shape the pin needed protecting from",
			planRequirementSerializedLength(repeated), planRequirementSerializedLength(full))
	}
	err := repeated.Validate()
	if err == nil {
		t.Fatal("a row repeating one input fact kind was accepted; the list is a set and the longest legal list is the whole vocabulary")
	}
	if !strings.Contains(err.Error(), string(longest)) {
		t.Errorf("the rejection does not name the duplicated kind %q: %v", longest, err)
	}

	// AND THE SAME RULE ON THE OTHER ARRAY. `fact_kinds` had the identical
	// count-and-membership-only guard; a fix landing on one array and not the
	// other is how this branch has gone wrong before.
	read := ContextFabricPlanRequirement{
		Requirement: "state/subject/project", Obligation: "state",
		Role: "subject", Subject: ContextFabricSubjectKind("project"),
		Kind:      "read",
		FactKinds: []ContextFabricFactKind{kinds[0], kinds[0]},
		Scope:     "single_subject", Quantifier: "at_least_one",
	}
	readErr := read.Validate()
	if readErr == nil {
		t.Fatal("a row repeating one fact kind was accepted; the uniqueness rule reached input_fact_kinds only")
	}
	if !strings.Contains(readErr.Error(), string(kinds[0])) {
		t.Errorf("the fact_kinds rejection does not name the duplicated kind %q: %v", kinds[0], readErr)
	}
	// The SAME row with the duplicate removed must be valid, or the rejection
	// above cannot be attributed to uniqueness.
	read.FactKinds = []ContextFabricFactKind{kinds[0]}
	if err := read.Validate(); err != nil {
		t.Fatalf("removing only the duplicate left the row invalid (%v); the rejection was not the uniqueness rule", err)
	}
}
