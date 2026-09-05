package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Test-only exports, the standard Go pattern: this file is compiled into
// the package for the test binary and nothing else, so the external
// contextfabric_test package can reach package internals WITHOUT widening
// the production surface.
//
// It exists because the N2 parity property has to compare a GENERATED seed
// against the CHAOS-4347 status composition, and that composition is
// unexported package state. Exporting it for real would invite a
// production reader, and the whole point of the parity property is that
// the composition is the thing being RETIRED -- it is a reference for the
// proof, not an API.

// StatusCategoryCompositionForTest returns a copy of the CHAOS-4347
// status-category composition: the ruled subject-kind -> fact-kind set
// that a bare `status` requirement expands into today.
//
// A COPY, not the map: a test that sorted or appended in place would
// corrupt the composition for the engine's own tests in the same binary,
// and the resulting failure would point anywhere but here.
func StatusCategoryCompositionForTest() map[SubjectKind][]FactKind {
	out := make(map[SubjectKind][]FactKind, len(statusCategoryFactKindComposition))
	for subject, kinds := range statusCategoryFactKindComposition {
		out[subject] = append([]FactKind(nil), kinds...)
	}
	return out
}

// ---------------------------------------------------------------------------
// The SIX PLANNING AUTHORITIES (design 13.8a), reached for the parity proof.
//
// Every shim below CALLS the production authority. None of them
// re-implements one, and that is the whole discipline: an expectation
// computed by a transcription of the thing it is checking is decided by
// the transcription, and this package's own review history carries three
// instances of exactly that defect.
//
// They live here rather than on the production surface for the reason the
// file header already gives for the status composition: these functions
// are REFERENCES FOR A RETIREMENT PROOF, not an API. A production reader
// of any of them would be a seventh planning authority.
// ---------------------------------------------------------------------------

// ComposeStatusCategoryRequirementsForTest runs AUTHORITY 1 --
// composeStatusCategoryRequirements (chaos4347_status_category_composition.go)
// -- against a requirement list and a subject set.
//
// The engine value carries no telemetry sink, which the production method
// nil-guards (recordCategoryFactComposition), so the composition runs and
// records nothing. The composition itself reads no engine state at all.
func ComposeStatusCategoryRequirementsForTest(requirements []FactRequirement, subjects []SubjectRef) []FactRequirement {
	engine := &Engine{}
	return engine.composeStatusCategoryRequirements(context.Background(), storage.Principal{OrgID: "org_parity"}, requirements, subjects)
}

// PlanFactKindsForTest runs AUTHORITY 3 -- planFactKinds
// (chaos4636_answer_plan.go) -- for a family definition and an
// interpretation.
//
// Passing a ZERO InterpretedQuestion isolates authority 3 from authority 2:
// planFactKinds unions the model's requirement kinds with the family's own
// contribution, so an empty model side leaves exactly what the FAMILY
// contributes. That separation is the only way the two can carry different
// verdicts, and they do.
//
// NIL REQUIREMENTS, FOR THE SAME REASON AND IT MATTERS MORE. planFactKinds
// now also unions the declared inputs of the turn's computed requirement
// rows, and that third source is the DERIVATION's, not this authority's.
// Handing the rows in here would make authority 3's measured contribution
// include the very kinds the derived rows supply, so the parity cell would
// compare the derivation against itself and report `subsumed` no matter what
// the authority actually contributes -- the proof clearing its own blocking
// cell by construction. The authority is what remains when the model side and
// the row side are both empty.
func PlanFactKindsForTest(definition QuestionFamilyDefinition, interpretation InterpretedQuestion) []FactKind {
	return planFactKinds(definition, interpretation, nil)
}

// CohortRankingFormulaKindsForTest returns a copy of AUTHORITY 4 --
// cohortRankingFormulaKinds (chaos4636_answer_plan.go), the five-kind set
// the engine appends FIRST and unconditionally whenever the resolved
// graph context carries a cohort (engine.go, inside `if
// graphContext.Cohort != nil`).
//
// A COPY, for the reason StatusCategoryCompositionForTest gives: the
// variable is package state shared with the engine's own tests in this
// binary.
func CohortRankingFormulaKindsForTest() []FactKind {
	return append([]FactKind(nil), cohortRankingFormulaKinds...)
}

// IsCohortSubjectAxisForTest exposes the predicate authority 3 uses to
// decide whether the ranking kinds are part of the plan, so the harness
// asks the PRODUCTION rule rather than re-listing the cohort axes.
func IsCohortSubjectAxisForTest(axis SubjectAxisKind) bool {
	return isCohortSubjectAxis(axis)
}

// ApplyCarriedPlanForTest runs AUTHORITY 6 -- applyCarriedPlan
// (chaos4636_plan_carry.go) -- and reports whether the carry applied.
//
// It is reached through a shim because the carry contributes no fact kinds
// at all: what it overlays is the FAMILY and the group axis, which is a
// planning authority precisely because the family is authority 3's input.
// The harness needs to demonstrate that, not assert it.
func ApplyCarriedPlanForTest(outcome QuestionFamilyOutcome, family QuestionFamily, groupKind SubjectKind) (QuestionFamilyOutcome, bool) {
	return applyCarriedPlan(outcome, planCarryResult{Outcome: PlanCarryHit, Family: family, GroupKind: groupKind})
}
