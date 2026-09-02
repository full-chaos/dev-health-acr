package v1

import (
	"strings"
	"testing"
	"time"
)

// Y4's byte minimum is MEASURED, not chosen, and this is where it is measured.
//
// WHY IT CANNOT BE INHERITED. The minimal-answer-floor paper derives its
// FloorMinimumBytes from the worst-case FLOOR document. That form is not being
// built — the ruling deferred it — so its number is unavailable. What IS
// available, and is the term the paper proves decisive, is the ENVELOPE: the
// fields a result carries before a single driver or fact is serialized.
//
// The paper's own proof: the echoed Question is bounded at 8000 characters by
// request validation while the smallest legal byte budget is 8192, so
// "a caller can spend 97.6% of the smallest legal byte budget on the echoed
// question alone, before one driver or fact is serialized." At 8192 an answer is
// impossible for any question over ~7 KB whatever its own caps. That is the
// whole reason a minimum pair is necessary rather than tidy.
//
// So the measurement here is: the SMALLEST result that is still a valid answer,
// carrying the LARGEST legal envelope, serialized with the same encoder the
// route serves with. The constant is then pinned to that measurement and the
// pin is mutated by LOWERING it — a rounded pin can absorb a raise, which is why
// "raise one cap by one" was rejected as an oracle.
//
// Every field below is maximized to its OWN validated bound, and the result is
// run through Validate() before measurement: a measurement of a document the
// contract would reject is not a bound on anything.

// maxEnvelopeResult returns the smallest valid answer-capable result carrying
// the largest legal envelope.
func maxEnvelopeResult(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	result := validContextFabricContractResult()

	// The dominant term, and the one the paper's proof turns on: request
	// validation bounds Question at 8000 characters.
	result.Question = strings.Repeat("q", 8000)

	// Version metadata: every sibling is bounded at 256 by validVersion, and
	// BackendVersion at 256 by its own check. ContractVersion is NOT filled
	// with padding -- it must remain the real schema identifier or the
	// document stops being the thing being measured.
	pad := strings.Repeat("v", 256)
	result.Versions.ServiceVersion = pad
	result.Versions.Backend = pad
	result.Versions.ProjectionVersion = pad
	result.Versions.QueryVersion = pad
	result.Versions.InterpretationVersion = pad
	result.Versions.SynthesisVersion = pad
	result.Versions.CanonicalServiceVersion = pad
	result.Versions.BackendVersion = pad

	// The plan and every narrowing step it may honestly record. Narrowing is
	// bounded at ContextFabricPlanNarrowingMaxCount, and the vocabularies are
	// closed, so "maximal" here is exactly the declared maxima rather than an
	// invented number.
	plan := ContextFabricAnswerPlan{
		Family:       ContextFabricQuestionFamilySubjectInvestigation,
		FamilySource: ContextFabricQuestionFamilySourceStructurePrecedence,
		// FamilyVersion is bounded at 64, not 256 -- the validator caught an
		// earlier draft padding it to the version-string bound. Maximizing a
		// field past its OWN bound measures a document the contract rejects.
		FamilyVersion:  strings.Repeat("f", 64),
		RequireDrivers: true,
		RequireRanking: true,
		Budget:         ContextFabricAnswerPlanBudget{MaxItems: 30, MaxSerializedBytes: 1 << 20},
	}
	for _, kind := range ContextFabricFactKindVocabulary() {
		plan.FactKinds = append(plan.FactKinds, kind)
	}
	for i := 0; i < ContextFabricPlanNarrowingMaxCount; i++ {
		plan.Narrowing = append(plan.Narrowing, ContextFabricPlanNarrowing{
			Stage:   ContextFabricPlanNarrowingAssembledResult,
			Basis:   ContextFabricNarrowingBasisCanonicalIDLexical,
			Before:  50,
			After:   10,
			Overrun: ContextFabricBudgetOverrunItems,
		})
	}
	result.AnswerPlan = &plan

	result.GeneratedAt = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	return result
}

// withMinimalAnswerContent adds the IRREDUCIBLE answer to a maximal envelope:
// one committed subject (already present), ONE driver, and the facts that driver
// cites. That is the floor paper's own irreducible shape -- "one subject or
// member, one driver, its facts", FloorMinimumItems = 1 + 1 + 2 = 4 -- and it is
// the smallest thing that is still an ANSWER rather than an envelope.
//
// Grounding closure is respected rather than sidestepped: the driver cites the
// claim ids that are actually present, so this is a document the validator
// accepts, not a shape that merely serializes.
func withMinimalAnswerContent(t *testing.T, result ContextFabricInvestigationResult) ContextFabricInvestigationResult {
	t.Helper()
	subject := result.SubjectResolution.Committed[0]
	amber := "amber"
	second := "open"
	result.StrongestPressures = []string{"open blockers"}
	// Evidence refs must close: the driver cites them and the result-level set
	// must contain them, or the reference-integrity validator rejects the
	// document. Grounding closure is respected here rather than sidestepped.
	result.EvidenceRefIDs = []string{"evidence_status_0001"}
	result.ClaimedFacts = []ContextFabricClaimedFact{
		{ClaimID: "claim_status_00001", Kind: ContextFabricFactStatus, Subject: subject, Field: "status", Value: ContextFabricScalarValue{String: &amber}},
		{ClaimID: "claim_status_00002", Kind: ContextFabricFactStatus, Subject: subject, Field: "state", Value: ContextFabricScalarValue{String: &second}},
	}
	result.Drivers = []ContextFabricDriverJudgment{{
		DriverID: "driver_minimum_001", Standing: ContextFabricDriverPrincipal, Category: "status",
		Title: "Status is amber", Summary: "Status remains amber.",
		AffectedSubjects: []ContextFabricSubjectRef{subject},
		ClaimedFactIDs:   []string{"claim_status_00001", "claim_status_00002"},
		EvidenceRefIDs:   []string{"evidence_status_0001"},
		Derivation:       ContextFabricDerivationCanonicalStructured,
		EpistemicStatus:  ContextFabricEpistemicObserved,
		Confidence:       0.9, Current: true,
	}}
	// Coverage.Partial is ALWAYS carried on a reduced answer -- the field that
	// stops a shortened answer looking cleaner than the investigation was.
	result.Coverage.Partial = true
	// The completeness block is a census of what the document actually
	// carries, and the validator enforces the equality. Setting it by hand to
	// a stale zero would measure a document the contract rejects.
	result.Completeness.ClaimedFactsCount = len(result.ClaimedFacts)
	return result
}

// TestMeasureMinimumAnswerEnvelopeBytes measures and reports. It asserts the
// document is VALID and that the measurement is stable, but it deliberately does
// not yet assert a pinned constant -- the constant is derived FROM this number,
// so asserting one here first would be circular.
func TestMeasureMinimumAnswerEnvelopeBytes(t *testing.T) {
	t.Parallel()

	result := maxEnvelopeResult(t)
	if err := result.Validate(); err != nil {
		t.Fatalf("the maximal envelope does not VALIDATE, so it bounds nothing: %v", err)
	}

	measurement, err := MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}

	// Stability: the same document must measure the same twice, or a pin
	// taken from it is noise.
	again, err := MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("re-measure: %v", err)
	}
	if again.Bytes != measurement.Bytes {
		t.Fatalf("measurement is not stable: %d then %d", measurement.Bytes, again.Bytes)
	}

	t.Logf("MEASURED maximal envelope: %d bytes, %d budgeted items", measurement.Bytes, measurement.Items.Budgeted())

	// The envelope is the floor of the floor. The constant must clear the
	// smallest thing that is still an ANSWER.
	answer := withMinimalAnswerContent(t, result)
	if err := answer.Validate(); err != nil {
		t.Fatalf("the minimal ANSWER does not VALIDATE, so it bounds nothing: %v", err)
	}
	answerMeasurement, err := MeasureContextFabricResponse(answer)
	if err != nil {
		t.Fatalf("measure minimal answer: %v", err)
	}
	t.Logf("MEASURED minimal ANSWER (maximal envelope + 1 subject + 1 driver + 2 facts): %d bytes, %d budgeted items",
		answerMeasurement.Bytes, answerMeasurement.Items.Budgeted())
	t.Logf("  content adds %d bytes over the bare envelope", answerMeasurement.Bytes-measurement.Bytes)

	// THE ROT GUARD, two-sided. The pin is ROUNDED UP to a power of two, so a
	// one-sided "measured <= constant" assertion would let the real worst case
	// creep toward it while this kept passing -- the exact way a rounded pin
	// goes wrong. The second assertion closes that: the constant may not drift
	// far above the floor it describes either, because this is an INPUT bound
	// and every unnecessary byte is caller breakage.
	const headroomFactor = 2
	if answerMeasurement.Bytes > int64(ContextFabricMinimumAnswerBytes) {
		t.Fatalf("the minimal answer now measures %d bytes, ABOVE the pinned ContextFabricMinimumAnswerBytes of %d.\n"+
			"A budget at the documented minimum can no longer hold an answer. Re-measure and re-pin, and say in the commit what grew.",
			answerMeasurement.Bytes, ContextFabricMinimumAnswerBytes)
	}
	if int64(ContextFabricMinimumAnswerBytes) >= headroomFactor*answerMeasurement.Bytes {
		t.Fatalf("ContextFabricMinimumAnswerBytes (%d) is now %.1fx the measured floor of %d bytes.\n"+
			"The pin has drifted above what it describes, and this is an INPUT bound: the excess is caller breakage bought for nothing. Re-pin closer to the measurement.",
			ContextFabricMinimumAnswerBytes, float64(ContextFabricMinimumAnswerBytes)/float64(answerMeasurement.Bytes), answerMeasurement.Bytes)
	}
	t.Logf("  pin %d clears the %d-byte floor with %d bytes of headroom (%.2fx, bound is <%dx)",
		ContextFabricMinimumAnswerBytes, answerMeasurement.Bytes,
		int64(ContextFabricMinimumAnswerBytes)-answerMeasurement.Bytes,
		float64(ContextFabricMinimumAnswerBytes)/float64(answerMeasurement.Bytes), headroomFactor)

	// The relation that makes this constant necessary, asserted rather than
	// left in a comment: the current serving minimum cannot hold an answer.
	if int64(ContextFabricSerializedBytesMin) >= answerMeasurement.Bytes {
		t.Fatalf("ContextFabricSerializedBytesMin (%d) now clears the minimal answer (%d bytes): the premise of this whole change has changed and the minimum pair may no longer be needed",
			ContextFabricSerializedBytesMin, answerMeasurement.Bytes)
	}
	t.Logf("  ContextFabricSerializedBytesMin=%d is short of a minimal answer by %d bytes",
		ContextFabricSerializedBytesMin, answerMeasurement.Bytes-int64(ContextFabricSerializedBytesMin))
	t.Logf("  question alone: %d bytes of that", len(result.Question))
	t.Logf("  current ContextFabricSerializedBytesMin = %d -- envelope %s that floor",
		ContextFabricSerializedBytesMin,
		map[bool]string{true: "EXCEEDS", false: "fits under"}[measurement.Bytes > int64(ContextFabricSerializedBytesMin)])
}
