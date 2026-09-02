package v1

import (
	"encoding/json"
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

// worstCaseRune is the single character that costs the most SERIALIZED BYTES
// per rune of a length-bounded field.
//
// It is not the widest UTF-8 encoding. Go's JSON encoder HTML-escapes "<", ">"
// and "&", and escapes the line separators, to a six-byte \uXXXX form -- more
// than the four bytes a non-BMP emoji costs raw. Bounds in this contract are
// counted in RUNES, so the byte cost of a maximal field is the rune bound times
// this factor.
const worstCaseRune = "<"

// worstCaseBytesPerRune is what the encoder actually charges for it, proven by
// TestWorstCaseRuneIsTheOneTheEncoderEscapes rather than assumed here.
const worstCaseBytesPerRune = 6

// TestWorstCaseRuneIsTheOneTheEncoderEscapes proves the padding choice instead
// of asserting it. If a future encoder change makes some other character more
// expensive, the measurement above stops being a worst case -- and this fails
// rather than letting the minimum quietly become too small again, which is
// exactly what happened when the fixture assumed ASCII.
func TestWorstCaseRuneIsTheOneTheEncoderEscapes(t *testing.T) {
	t.Parallel()
	const runes = 1000
	candidates := []string{"q", "<", "&", ">", "\"", "\\", "\u4e16", "\U0001F600", "\u2028", "\u2029", "\t"}
	worst, worstCost := "", 0
	for _, candidate := range candidates {
		encoded, err := json.Marshal(strings.Repeat(candidate, runes))
		if err != nil {
			t.Fatalf("marshal %q: %v", candidate, err)
		}
		// Subtract the two surrounding quotes.
		cost := (len(encoded) - 2) / runes
		if cost > worstCost {
			worst, worstCost = candidate, cost
		}
	}
	if worstCost != worstCaseBytesPerRune {
		t.Fatalf("the worst candidate costs %d bytes/rune but worstCaseBytesPerRune says %d: the envelope measurement is no longer a worst case", worstCost, worstCaseBytesPerRune)
	}
	encoded, err := json.Marshal(strings.Repeat(worstCaseRune, runes))
	if err != nil {
		t.Fatalf("marshal padding rune: %v", err)
	}
	if got := (len(encoded) - 2) / runes; got != worstCost {
		t.Fatalf("the padding rune %q costs %d bytes/rune but the worst candidate %q costs %d: the fixture is not padding with the worst case",
			worstCaseRune, got, worst, worstCost)
	}
}

// maxEnvelopeResult returns the smallest valid answer-capable result carrying
// the largest legal envelope.
func maxEnvelopeResult(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	result := validContextFabricContractResult()

	// The dominant term, and the one the paper's proof turns on: request
	// validation bounds Question at 8000 CHARACTERS -- and characters are
	// RUNES, not bytes (stringLengthBetween uses utf8.RuneCountInString).
	//
	// An earlier version of this fixture used 8000 ASCII bytes and measured a
	// minimum FOUR TIMES too small. A review found it, and the correction is
	// the whole reason this fixture now derives its padding rune by
	// measurement instead of assuming one: the worst case is not the widest
	// UTF-8 encoding but the one Go's JSON encoder ESCAPES, which costs six
	// bytes per rune against a 4-byte emoji's four. See
	// TestWorstCaseRuneIsTheOneTheEncoderEscapes, which proves the choice
	// rather than asserting it.
	result.Question = strings.Repeat(worstCaseRune, 8000)

	// Version metadata: every sibling is bounded at 256 by validVersion, and
	// BackendVersion at 256 by its own check. ContractVersion is NOT filled
	// with padding -- it must remain the real schema identifier or the
	// document stops being the thing being measured.
	pad := strings.Repeat(worstCaseRune, 256)
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
		FamilyVersion:  strings.Repeat(worstCaseRune, 64),
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

	// THE IRREDUCIBLE ANSWER -- the request-INDEPENDENT floor, and what the
	// static constant is pinned to.
	//
	// The maximal measurement above is NOT the pin, and the difference is the
	// whole shape of this row. Question echo and interpretation terms are
	// request-shaped and FIXED -- no narrowing or degradation path reduces them
	// (narrowing reaches only cohort members and facts) -- so the smallest answer
	// a given request can have is a function of THAT request. A static constant
	// cannot express that: pinned to the maximum it exceeds every shipped
	// consumer's budget, and pinned lower it is not a floor for every request.
	//
	// So the static constant is the floor that holds for ANY request -- the
	// irreducible envelope plus minimal content -- and the request-dependent part
	// is a runtime check. This measures the static half.
	irreducible := validContextFabricContractResult()
	irreducible.Question = "q"
	irreducible.Interpretation.SubjectTerms = []string{"s"}
	irreducible.Interpretation.ComparisonTerms = nil
	irreducibleAnswer := withMinimalAnswerContent(t, irreducible)
	if err := irreducibleAnswer.Validate(); err != nil {
		t.Fatalf("the irreducible answer does not VALIDATE, so it bounds nothing: %v", err)
	}
	floor, err := MeasureContextFabricResponse(irreducibleAnswer)
	if err != nil {
		t.Fatalf("measure irreducible answer: %v", err)
	}
	t.Logf("MEASURED IRREDUCIBLE answer (question 1 rune, 1 term, + minimal content): %d bytes, %d budgeted items",
		floor.Bytes, floor.Items.Budgeted())
	t.Logf("  the request-shaped spread between irreducible and maximal is %d bytes -- this is what the runtime check covers",
		answerMeasurement.Bytes-floor.Bytes)

	// THE PIN, exact, on the request-INDEPENDENT floor. Not rounded: slack
	// absorbs growth, and pinned exactly the assertion kills in both directions.
	if int64(ContextFabricMinimumAnswerBytes) != floor.Bytes {
		t.Fatalf("ContextFabricMinimumAnswerBytes = %d but the measured irreducible answer is %d bytes.\n"+
			"Re-pin to %d and say in the commit what grew. This bound is published in the request schema, so the\n"+
			"re-pin is a wire-visible change and needs the consumer pin bump with it.",
			ContextFabricMinimumAnswerBytes, floor.Bytes, floor.Bytes)
	}

	// The maximal answer must stay FAR above the static floor, or the premise
	// that a runtime check is needed at all has changed and this row should be
	// reconsidered rather than quietly simplified.
	if answerMeasurement.Bytes <= floor.Bytes*2 {
		t.Fatalf("the maximal answer (%d) is no longer far above the irreducible floor (%d): the request-shaped spread this row exists for has collapsed, and the two-property split may no longer be justified",
			answerMeasurement.Bytes, floor.Bytes)
	}

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

// RED C: the MCP surface must refuse a sub-minimum budget AT ITS OWN BOUNDARY.
//
// This is the surface the governing paper does not name, and leaving it behind
// is a specific, diagnosable harm rather than an inconsistency. MCP forwards its
// budget to the hosted investigation options; if the MCP validator kept the old
// floor, a caller passing a sub-minimum budget would clear MCP validation and be
// refused DOWNSTREAM by the hosted validator. The MCP tool would surface that as
// a generic upstream failure, so the caller would never see the closed reason at
// the surface they are actually using -- the diagnosis would exist and be
// unreachable.
//
// Tracking the hosted constant, rather than restating a number, is what makes
// the two surfaces incapable of disagreeing.
func TestMCPInvestigationBudgetRefusesBelowTheMinimumAnswerSize(t *testing.T) {
	t.Parallel()

	// The probe value is one byte below the static floor, not the old 8192.
	// 8192 is now LEGAL against this constant, and that is not an oversight:
	// the static floor is the request-INDEPENDENT one, and a service at 8192
	// can serve a small question perfectly well. What 8192 cannot serve is a
	// LARGE question -- and catching that is the per-request runtime check's
	// job, not this one's. Testing the boundary directly is also the stronger
	// assertion: an off-by-one in the guard fails here and would not fail
	// against a value four times away from it.
	const belowFloor = ContextFabricMinimumAnswerBytes - 1
	if MCPInvestigationBudgetMinBytes != ContextFabricMinimumAnswerBytes {
		t.Fatalf("MCPInvestigationBudgetMinBytes = %d but the hosted minimum is %d: the two surfaces can now disagree, which is the whole defect this pins",
			MCPInvestigationBudgetMinBytes, ContextFabricMinimumAnswerBytes)
	}

	if err := (MCPInvestigationBudget{MaxSerializedBytes: belowFloor}).Validate(); err == nil {
		t.Fatalf("the MCP surface accepted a %d-byte budget, one below the floor: it clears here and is then refused by the hosted validator, so the caller sees a generic upstream failure instead of the reason at the surface they are using", belowFloor)
	}

	// The boundary is accepted, or the documented minimum is a lie.
	if err := (MCPInvestigationBudget{MaxSerializedBytes: ContextFabricMinimumAnswerBytes}).Validate(); err != nil {
		t.Fatalf("the MCP surface refused exactly the documented minimum (%d): %v", ContextFabricMinimumAnswerBytes, err)
	}

	// Zero still means "unset, use the default" and must remain accepted --
	// this change tightens a floor, it does not make the field required.
	if err := (MCPInvestigationBudget{}).Validate(); err != nil {
		t.Fatalf("an unset budget was refused, so this change made an optional field required: %v", err)
	}
}

// RED A's request half, on the surface the contract owns. The route's closed
// reason code is a separate wire concern; what the CONTRACT must guarantee is
// that a sub-minimum request is invalid at all.
func TestInvestigationOptionsRefuseBelowTheMinimumAnswerSize(t *testing.T) {
	t.Parallel()

	// The probe value is one byte below the static floor, not the old 8192.
	// 8192 is now LEGAL against this constant, and that is not an oversight:
	// the static floor is the request-INDEPENDENT one, and a service at 8192
	// can serve a small question perfectly well. What 8192 cannot serve is a
	// LARGE question -- and catching that is the per-request runtime check's
	// job, not this one's. Testing the boundary directly is also the stronger
	// assertion: an off-by-one in the guard fails here and would not fail
	// against a value four times away from it.
	const belowFloor = ContextFabricMinimumAnswerBytes - 1
	options := ContextFabricInvestigationOptions{
		MaxSubjectCandidates: 10, MaxCohortMembers: 20, MaxRelationshipPaths: 25,
		MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: belowFloor,
	}
	if err := options.Validate(); err == nil {
		t.Fatalf("a request asking for %d bytes validated, but not even the irreducible answer fits in %d bytes (floor %d)",
			belowFloor, belowFloor, ContextFabricMinimumAnswerBytes)
	}

	options.MaxSerializedBytes = ContextFabricMinimumAnswerBytes
	if err := options.Validate(); err != nil {
		t.Fatalf("a request at exactly the documented minimum (%d) was refused: %v", ContextFabricMinimumAnswerBytes, err)
	}
}
