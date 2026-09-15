package v1

// The SOURCE-TRUNCATION twin of the census exception (CHAOS-5742).
//
// A read requirement whose only fact-bearing kind was a TRUNCATED canonical
// source has no population axis to report a shortfall on: the cover of what
// was observed is exact (one source), so Served == Declared is the truthful
// pair, while the source itself said it did not return everything asked of
// it. `truncationQualified` (context_fabric_requirement_outcome.go) admits
// exactly that shape, discriminated from the census exception by
// `Impact: depth` (never `scope`) so the two can never both admit the same
// row.
//
// This file also pins the OTHER direction: a `narrowed` row that served NONE
// of a truncated, fact-bearing source is a contradiction and is refused --
// but only that exact shape. A population-scope zero (read_population.go's
// zero-of-N-committed arms) and a planner-narrowing zero (a kind the plan
// itself narrowed to nothing) are different mechanisms, and both stay legal.

import (
	"strings"
	"testing"
)

// truncationQualifiedRow is the row a single, fully-truncated canonical-fact
// read requirement emits: one declared kind, one observation, all of it
// truncated.
func truncationQualifiedRow() ContextFabricPlanRequirementOutcomeRow {
	return ContextFabricPlanRequirementOutcomeRow{
		Stage:       ContextFabricOutcomeStageAssembledResult,
		Requirement: "state/subject/team",
		Obligation:  "state",
		Outcome:     ContextFabricRequirementNarrowed,
		// Depth, not scope: the same subject, with less behind it.
		Impact:        ContextFabricAnswerImpactDepth,
		CauseCoverage: ContextFabricCoverageDetailFactProviderReported,
		// Observed: the provider reported the truncation.
		CauseObserved: true,
		Served:        1,
		Declared:      1,
	}
}

// TestATruncationQualifiedRowIsAdmittedWithEqualCounts is the admission.
// Without it, the only legal row shapes for a fully-truncated single-kind
// read are `narrowed 0/1` (a false zero -- the source did return facts) or
// `satisfied` (a truncation reported as a clean serve) -- neither honest.
func TestATruncationQualifiedRowIsAdmittedWithEqualCounts(t *testing.T) {
	t.Parallel()
	if err := ValidateContextFabricPlanRequirementOutcomeRow(truncationQualifiedRow()); err != nil {
		t.Fatalf("a narrowed row over an observed, fact-bearing truncation with equal counts is refused: %v", err)
	}
}

// TestATruncationQualifiedRowCannotCarryARefinement mirrors the census
// exception's own pin: a refinement is a Before larger than an After, and
// ContextFabricReductionRefinement already declines to mint one when
// Declared <= Served, so a hand-built refinement on this shape must still be
// refused by the refinement chain check.
func TestATruncationQualifiedRowCannotCarryARefinement(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Refinements = []ContextFabricRequirementRefinement{{
		Stage: row.Stage, Coverage: row.CauseCoverage, Before: 1, After: 1,
	}}
	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err == nil {
		t.Fatal("a refinement claiming a 1 -> 1 step (nothing reduced) was accepted")
	}
}

// TestTheTruncationExceptionRequiresDepthNotScope is the discriminator pin: a
// scope-impact row with otherwise-identical counts and cause is the CENSUS
// exception's own shape (a population axis), not this one, and the census
// exception refuses it because `fact_provider_reported` is not a
// population-qualifying code. The two exceptions must never both reach for
// the same row.
func TestTheTruncationExceptionRequiresDepthNotScope(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Impact = ContextFabricAnswerImpactScope

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a scope-impact row with a non-census cause and equal counts was accepted: " +
			"truncationQualified must not admit on impact alone")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheTruncationExceptionRequiresAnObservedCause is the CauseObserved
// conjunct's pin, for the same reason the census exception has one: a
// DEFAULTED cause would let any producer opt into equal-count narrowing by
// naming a code it never measured.
func TestTheTruncationExceptionRequiresAnObservedCause(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.CauseObserved = false

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a narrowed row with equal counts and a DEFAULTED cause was accepted")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheTruncationExceptionRequiresTheProviderReportedCode is the
// allow-list conjunct: a coverage code outside the one this exception was
// written for must not admit equal counts, or the exception becomes a
// property of any narrowed row that can arrange one.
func TestTheTruncationExceptionRequiresTheProviderReportedCode(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.CauseCoverage = ContextFabricCoverageDetailFactNarrowed

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a narrowed row with equal counts and a non-truncation cause was accepted")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheTruncationExceptionRejectsAMixedNarrowingCause mirrors the census
// exception's own rejection of a row that names a basis or an overrun beside
// the qualifying cause: two reduction mechanisms on one row tell the reader
// two incompatible stories about the same shrink.
func TestTheTruncationExceptionRejectsAMixedNarrowingCause(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.CauseNarrowing = ContextFabricNarrowingBasisCanonicalIDLexical

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a truncation-qualified row also naming a narrowing basis was accepted")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheTruncationExceptionRejectsAMixedOverrunCause is the CauseOverrun
// half of the pair above: a budget overrun asserts a byte/item cut, which is
// contradicted by the equal counts this exception exists to permit exactly
// as a narrowing basis is.
func TestTheTruncationExceptionRejectsAMixedOverrunCause(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.CauseOverrun = ContextFabricBudgetOverrunItems

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a truncation-qualified row also naming a budget overrun was accepted")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheTruncationExceptionIsScopedToTheAssembledResultStage: a planning
// seed cannot take this exception -- it never carries an observed source
// truncation, and admitting one there would widen the exception past the one
// producer it exists for.
func TestTheTruncationExceptionIsScopedToTheAssembledResultStage(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Stage = ContextFabricOutcomeStagePlanning

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a planning-stage row with equal counts and a truncation cause was accepted")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestANarrowedRowServingNoneOfAnObservedTruncationIsRefused is the mirror
// rule: an observed, fact-bearing truncation reported as
// `narrowed 0/N` is a contradiction, and the wire refuses it.
func TestANarrowedRowServingNoneOfAnObservedTruncationIsRefused(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Served = 0

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a narrowed row over an observed, fact-bearing truncation with Served == 0 was accepted")
	}
	if !strings.Contains(err.Error(), "unavailable, not narrowed") {
		t.Fatalf("rejected for the wrong reason: %v (want the zero-served rule)", err)
	}
}

// TestTheZeroServedRefusalRequiresNoNarrowingBasisBesideTheTruncationCause and
// TestTheZeroServedRefusalRequiresNoOverrunBesideTheTruncationCause pin the
// refusal rule's own "OTHER two cause fields, empty" conjuncts: a row naming
// a second reduction mechanism beside the truncation cause is a different,
// malformed shape from the one this rule refuses, and this rule must not
// reach for it -- exactly the same discipline truncationQualified's own
// admission applies to the same two fields.
func TestTheZeroServedRefusalRequiresNoNarrowingBasisBesideTheTruncationCause(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Served = 0
	row.CauseNarrowing = ContextFabricNarrowingBasisCanonicalIDLexical

	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("a zero-served row also naming a narrowing basis was refused by the truncation rule: %v", err)
	}
}

func TestTheZeroServedRefusalRequiresNoOverrunBesideTheTruncationCause(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Served = 0
	row.CauseOverrun = ContextFabricBudgetOverrunItems

	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("a zero-served row also naming a budget overrun was refused by the truncation rule: %v", err)
	}
}

// TestTheZeroServedRefusalRequiresDepthNotScope: a population-scope zero
// (Impact: scope) is the CENSUS shape's territory, not this rule's, even
// with an otherwise-matching cause -- see
// TestAZeroServedNarrowedRowIsStillLegalOutsideTheTruncationShape for why a
// scope row needs no truncation cause at all to stay legal; this pins the
// conjunct directly, isolated from every other difference.
func TestTheZeroServedRefusalRequiresDepthNotScope(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Served = 0
	row.Impact = ContextFabricAnswerImpactScope

	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("a zero-served scope-impact row was refused by the depth-only truncation rule: %v", err)
	}
}

// TestTheZeroServedRefusalRequiresAnObservedCause: a DEFAULTED
// `fact_provider_reported` code -- nothing reported it, the evaluator merely
// carries the shipped default -- must not be read as the observed
// contradiction this rule refuses.
func TestTheZeroServedRefusalRequiresAnObservedCause(t *testing.T) {
	t.Parallel()
	row := truncationQualifiedRow()
	row.Served = 0
	row.CauseObserved = false

	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("a zero-served row with a DEFAULTED cause was refused by the observed-cause-only truncation rule: %v", err)
	}
}

// TestAZeroServedNarrowedRowIsStillLegalOutsideTheTruncationShape is the
// NEGATIVE CONTROL for the rule above: the refusal names ONE producer shape
// as the contradiction, not every zero-served narrowed row. A
// population-scope zero (the shape read_population.go's own
// zero-of-N-committed arms emit) is a different mechanism -- no source was
// truncated, nothing here claims a fact-bearing read -- and stays legal.
// Without this control, the rule above could have been written as a blanket
// "narrowed forbids Served == 0" and it would still pass its own positive
// test while silently refusing shapes it was never meant to reach.
func TestAZeroServedNarrowedRowIsStillLegalOutsideTheTruncationShape(t *testing.T) {
	t.Parallel()
	row := ContextFabricPlanRequirementOutcomeRow{
		Stage:         ContextFabricOutcomeStageAssembledResult,
		Requirement:   "state/operand/project",
		Obligation:    "state",
		Outcome:       ContextFabricRequirementNarrowed,
		Impact:        ContextFabricAnswerImpactScope,
		CauseCoverage: ContextFabricCoverageDetailFactNarrowed,
		CauseObserved: false,
		Served:        0,
		Declared:      1,
	}
	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("a population-scope zero-served narrowed row (a different mechanism, no truncation) was refused: %v", err)
	}

	// The same numbers, DEPTH impact and a defaulted `fact_narrowed` cause
	// (a planner narrowing, not an observed provider truncation) -- the
	// shape appendReadRequirementEvaluations itself still emits for a kind
	// the PLANNER narrowed to nothing (read_requirement_evaluation_test.go's
	// "a narrowing on the only served kind is still narrowed, never
	// unavailable"). CauseObserved is true there (the planner DID report the
	// narrowing) but the cause is `fact_narrowed`, never
	// `fact_provider_reported`, so the refusal rule must not reach it either.
	row.Impact = ContextFabricAnswerImpactDepth
	row.CauseObserved = true
	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("a planner-narrowing zero-served row (fact_narrowed, not an observed truncation) was refused: %v", err)
	}
}
