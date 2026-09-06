package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The 413 bound for the read-population change, MEASURED and never estimated.
//
// WHAT IS MEASURED: the byte delta of the SERVED DOCUMENT, through
// contractsv1.MeasureContextFabricResponse -- the number the route itself
// checks -- never json.Marshal of a row in isolation. A row measured alone
// omits the encoding context it actually ships in, and a bound derived from it
// is a different number from the one that refuses a request.
//
// SIGN AND BOUND, never a frozen constant. A test asserting an exact byte count
// fails on every unrelated field addition and gets "fixed" by editing the
// constant, which is how a budget assertion stops being one. These assert the
// DIRECTION of the change and a CEILING on it.
//
// ROWS ADDED: ZERO. This change CONVERTS an existing assembled-result row, it
// does not append one, so the 200-row bound is untouched -- asserted below
// rather than argued.

// smallestLegalByteBudget is the floor every bound here is stated against.
// Named once, with the reason, so a reader does not have to recognise 8192.
const smallestLegalByteBudget = 8192

// singleRowConversionCeiling and worstFrameDocumentCeiling are the
// STOP-AND-REPORT thresholds. Crossing either is not a test to update: it means
// the change costs materially more than it was costed at, and the budget is a
// live constraint on this package rather than a rounding error.
const (
	singleRowConversionCeiling = 200 // bytes, one converted row
	worstFrameDocumentCeiling  = 800 // bytes, a 4-distributive-row document
)

// measureDocument sizes a result exactly as the route would.
func measureDocument(t *testing.T, result InvestigationResult) int64 {
	t.Helper()
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse: %v", err)
	}
	return measurement.Bytes
}

// documentWithReadRows builds a served-shaped document carrying the outcome
// rows the given requirements produce under THIS change.
func documentWithReadRows(
	published []contractsv1.ContextFabricPlanRequirement,
	frame *QuestionFrame,
	committed []SubjectRef,
	coverage Coverage,
	facts CanonicalFactBundle,
) InvestigationResult {
	result := baseDocument(committed, coverage)
	result.Completeness.Outcomes = appendReadRequirementEvaluations(nil, published, coverage,
		readPopulationEvidenceFrom(frame, result, AnswerPlan{Requirements: published}, facts))
	return result
}

func baseDocument(committed []SubjectRef, coverage Coverage) InvestigationResult {
	return InvestigationResult{
		SchemaVersion:     "context_fabric_investigation_result.v1",
		ResultID:          "result_51770001",
		RequestID:         "request_51770001",
		Status:            InvestigationComplete,
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: committed},
		Coverage:          coverage,
	}
}

// documentWithParentRows is the BASELINE, and getting it right is the whole
// measurement.
//
// The parent emitted a row for these requirements too -- a `satisfied` row with
// KIND counts and no cause. So the delta this change costs is a CONVERSION of
// that row, not the arrival of a row where there was none.
//
// Measuring against "no row at all" is the mistake this helper exists to
// prevent, and the first cut of this file made it: without population evidence
// a distributive requirement now takes the caller-defect branch and emits
// nothing, so a naive before/after measures a whole row's bytes and reports a
// cost the change does not have. The non-vacuity assertion below caught it.
func documentWithParentRows(
	published []contractsv1.ContextFabricPlanRequirement,
	committed []SubjectRef,
	coverage Coverage,
	servedKinds int,
) InvestigationResult {
	result := baseDocument(committed, coverage)
	for _, requirement := range published {
		result.Completeness.Outcomes = append(result.Completeness.Outcomes, RequirementOutcomeRow{
			Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
			Requirement: requirement.Requirement,
			Obligation:  requirement.Obligation,
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
			Served:      servedKinds,
			Declared:    servedKinds,
		})
	}
	return result
}

// TestTheReadPopulationRowCostsWithinItsStatedBound is the 413 measurement.
func TestTheReadPopulationRowCostsWithinItsStatedBound(t *testing.T) {
	t.Parallel()
	alpha, beta := teamRef("team_alpha"), teamRef("team_beta")
	flow, health := contractsv1.ContextFabricFactFlow, contractsv1.ContextFabricFactHealth
	coverage := factCoverage(flow, SourceAvailable, health, SourceAvailable)
	frame := namedOperandFrame(SubjectTeam, SubjectTeam)
	facts := factsFor(alpha, kindList(flow, health))

	t.Run("one converted row", func(t *testing.T) {
		t.Parallel()
		published := []contractsv1.ContextFabricPlanRequirement{
			operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health),
		}
		before := measureDocument(t, documentWithParentRows(published, []SubjectRef{alpha, beta}, coverage, 2))
		after := measureDocument(t, documentWithReadRows(published, frame, []SubjectRef{alpha, beta}, coverage, facts))
		delta := after - before

		// SIGN: the converted row names a cause the satisfied row did not, so
		// it is LARGER. A non-positive delta would mean the fixture is not
		// exercising the conversion at all.
		if delta <= 0 {
			t.Fatalf("delta = %d bytes; a converted row carries a cause the satisfied row does not, "+
				"so a non-positive delta means this fixture measured nothing", delta)
		}
		if delta > singleRowConversionCeiling {
			t.Fatalf("STOP-AND-REPORT: one converted row costs %d bytes, above the stated %d-byte ceiling "+
				"(%.1f%% of the %d-byte floor). This is not a test to update: the change costs more than it was costed at.",
				delta, singleRowConversionCeiling,
				100*float64(delta)/float64(smallestLegalByteBudget), smallestLegalByteBudget)
		}
		t.Logf("413 MEASURED: one converted row = +%d bytes (%.2f%% of the %d-byte floor)",
			delta, 100*float64(delta)/float64(smallestLegalByteBudget), smallestLegalByteBudget)
	})

	t.Run("a worst-corpus-frame document with four distributive rows", func(t *testing.T) {
		t.Parallel()
		// Four distributive read rows, the shape the worst corpus frames carry.
		published := []contractsv1.ContextFabricPlanRequirement{
			operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health),
		}
		for _, obligation := range []AnswerObligation{ObligationPrincipalDrivers, ObligationTrendSeries, ObligationHealth} {
			requirement := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)
			requirement.Obligation = string(obligation)
			requirement.Requirement = string(obligation) + "/" + string(SubjectRoleOperand) + "/" + string(SubjectTeam)
			published = append(published, requirement)
		}
		before := measureDocument(t, documentWithParentRows(published, []SubjectRef{alpha, beta}, coverage, 2))
		after := measureDocument(t, documentWithReadRows(published, frame, []SubjectRef{alpha, beta}, coverage, facts))
		delta := after - before

		if delta <= 0 {
			t.Fatalf("delta = %d bytes over four rows; the fixture measured nothing", delta)
		}
		if delta > worstFrameDocumentCeiling {
			t.Fatalf("STOP-AND-REPORT: a four-row document costs %d bytes, above the stated %d-byte ceiling "+
				"(%.1f%% of the %d-byte floor)", delta, worstFrameDocumentCeiling,
				100*float64(delta)/float64(smallestLegalByteBudget), smallestLegalByteBudget)
		}
		t.Logf("413 MEASURED: four converted rows = +%d bytes (%.2f%% of the %d-byte floor)",
			delta, 100*float64(delta)/float64(smallestLegalByteBudget), smallestLegalByteBudget)
	})

	t.Run("rows added is ZERO and the 200-row bound is untouched", func(t *testing.T) {
		t.Parallel()
		published := []contractsv1.ContextFabricPlanRequirement{
			operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health),
		}
		before := documentWithParentRows(published, []SubjectRef{alpha, beta}, coverage, 2)
		after := documentWithReadRows(published, frame, []SubjectRef{alpha, beta}, coverage, facts)
		if len(after.Completeness.Outcomes) != len(before.Completeness.Outcomes) {
			t.Fatalf("outcome rows = %d with the population evidence and %d without; this change CONVERTS "+
				"a row and must append none", len(after.Completeness.Outcomes), len(before.Completeness.Outcomes))
		}
		// NON-VACUITY: the two documents must actually DIFFER, or "same row
		// count" is trivially true of a change that did nothing.
		if measureDocument(t, after) == measureDocument(t, before) {
			t.Fatal("the two documents are byte-identical; the conversion did not happen and the row-count " +
				"assertion above proves nothing")
		}
		if len(after.Completeness.Outcomes) > contractsv1.ContextFabricPlanRequirementOutcomeMaxCount {
			t.Fatalf("outcome rows = %d, above the %d bound",
				len(after.Completeness.Outcomes), contractsv1.ContextFabricPlanRequirementOutcomeMaxCount)
		}
	})
}
