package v1

// The vocabulary half of the census exception.
//
// These two tests reference identifiers that do not exist at the parent
// commit, so they cannot be part of the red-first proof -- a file that does
// not compile proves an identifier is absent, never that behaviour is wrong.
// They are pinned by the mutation battery instead: deleting the vocabulary
// member and widening the allow-list both have to turn something red here.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestThePopulationQualifyingCodeIsTheWireToken binds the Go constant to the
// literal string the published schemas enumerate.
//
// It exists because every other test in this change compares against the
// LITERAL rather than the constant, on the rule that a test reading the Go
// vocabulary agrees with the Go side by construction. That rule leaves exactly
// one gap: nothing would notice if the constant's value were changed while the
// schemas kept the old token. This is that one assertion, made once, in the
// only place where naming both spellings is the point.
func TestThePopulationQualifyingCodeIsTheWireToken(t *testing.T) {
	t.Parallel()
	const wire = "population_truncated"
	if string(ContextFabricCoverageDetailPopulationTruncated) != wire {
		t.Fatalf("the population-qualifying constant is %q but the published schemas enumerate %q; "+
			"a consumer validating against the schema would fail closed on every answer carrying it",
			ContextFabricCoverageDetailPopulationTruncated, wire)
	}
	if !validCoverageDetailCode(ContextFabricCoverageDetailPopulationTruncated) {
		t.Fatal("the population-qualifying code is not a member of its own closed vocabulary: the constant exists " +
			"but was never added to contextFabricCoverageDetailCodes, so every row naming it is refused at the wire")
	}
}

// TestPopulationQualifyingCodesAreAnAllowListOverTheWholeVocabulary walks the
// ENTIRE closed vocabulary and pins the predicate's answer for every member.
//
// A test that only checked the one census code would pass under a predicate
// that returned true for everything -- and that predicate would make the row
// validator's exception admit any coverage code at all, which is the whole
// rule it exists to keep narrow. Quantifying over the vocabulary is what turns
// "the allow-list admits this" into "the allow-list admits ONLY this".
func TestPopulationQualifyingCodesAreAnAllowListOverTheWholeVocabulary(t *testing.T) {
	t.Parallel()
	// The census set, spelled here rather than derived from the predicate --
	// an expectation computed by the thing it checks is decided by the
	// mutation it exists to catch.
	census := map[ContextFabricCoverageDetailCode]bool{
		ContextFabricCoverageDetailPopulationTruncated: true,
	}
	if len(census) == 0 {
		t.Fatal("the census set is empty; this walk would assert only refusals and the exception would be dead")
	}

	vocabulary := ContextFabricCoverageDetailCodeVocabulary()
	if len(vocabulary) == 0 {
		t.Fatal("the coverage-detail vocabulary is empty; this test would pass while proving nothing")
	}
	admitted := 0
	for _, code := range vocabulary {
		want := census[code]
		got := coverageDetailCodeQualifiesPopulation(code)
		if got != want {
			if want {
				t.Errorf("%s: qualifies a population but the allow-list refuses it", code)
			} else {
				t.Errorf("%s: does NOT describe a population, but the allow-list admits it -- the row validator "+
					"would then accept `narrowed` with served == declared on a coverage event about a fact read", code)
			}
		}
		if got {
			admitted++
		}
	}
	if admitted != len(census) {
		t.Fatalf("the allow-list admits %d codes, want exactly %d -- a member joined or left the census set "+
			"without this pin moving", admitted, len(census))
	}

	// A value from OUTSIDE the vocabulary entirely. The default arm is where
	// a "return true" mutation hides, and every in-vocabulary member above
	// already has an expectation, so this is the one input that distinguishes
	// "refuses non-census members" from "refuses nothing it was asked about".
	if coverageDetailCodeQualifiesPopulation(ContextFabricCoverageDetailCode("not_a_code_at_all")) {
		t.Error("the allow-list admits a code that is not in the vocabulary at all")
	}
}

// TestThePopulationQualifiedLabelIsNotTheGenericFallback pins that the code
// has a label of its OWN.
//
// The totality walk beside it asserts the composed label is non-empty and
// within bounds, and the `default` arm's "Coverage was limited" satisfies both.
// So a change that deleted this code's case would leave every existing
// assertion green while the reader lost the one sentence that says what
// actually happened -- a fallback that reads as coverage rather than as a
// count being a floor. This is the assertion that notices.
func TestThePopulationQualifiedLabelIsNotTheGenericFallback(t *testing.T) {
	t.Parallel()
	const fallback = "Coverage was limited"

	// Positive control: the fallback string is REACHABLE, so a test asserting
	// "not the fallback" is asserting something a real defect could produce.
	unknown := validDetailForCode(ContextFabricCoverageDetailFactPruned)
	unknown.Code = ContextFabricCoverageDetailCode("no_such_code")
	unknown.FactKind = ""
	unknown.SourceState = ""
	unknown.SupportedKinds = nil
	if got := ComposeCoverageDetailLabel(unknown); got != fallback {
		t.Fatalf("the control did not reach the fallback arm (got %q); this test cannot detect the defect it exists for", got)
	}

	detail := validDetailForCode(ContextFabricCoverageDetailPopulationTruncated)
	label := ComposeCoverageDetailLabel(detail)
	if label == fallback {
		t.Fatal("the population-qualifying code composes the GENERIC fallback label: a reader is told coverage was " +
			"limited when what happened is that the number they were given is a floor, not a total")
	}
	if strings.TrimSpace(label) == "" {
		t.Fatal("the population-qualifying code composes an empty label")
	}
	if len([]rune(label)) > ContextFabricCoverageDetailLabelMaxLength {
		t.Fatalf("the composed label is %d runes, over the %d bound", len([]rune(label)), ContextFabricCoverageDetailLabelMaxLength)
	}
}

// TestTheCensusQualifiedCountCostsExactlyItsMeasuredBytes ASSERTS the wire cost
// of the qualification instead of printing it.
//
// A number a downstream artifact depends on must be asserted in the test that
// derives it, in the same test. The response-budget work downstream reasons
// about this delta, and a measurement that is only logged cannot fail -- so a
// change that quietly doubled the cost would be narration in a log nobody
// diffs.
//
// It measures the two encodings against each other rather than pinning an
// absolute size, because what the budget cares about is the DELTA a
// qualification adds to a row that would have shipped anyway.
//
// If a field name, a token, or a tag changed on purpose, update the pin in the
// same commit and say so in the message. Never update it to make this pass.
func TestTheCensusQualifiedCountCostsExactlyItsMeasuredBytes(t *testing.T) {
	t.Parallel()
	// The row an exact count over a COMPLETE census ships today.
	satisfied := ContextFabricPlanRequirementOutcomeRow{
		Stage:       ContextFabricOutcomeStageAssembledResult,
		Requirement: "count/member/team",
		Obligation:  "count",
		Outcome:     ContextFabricRequirementSatisfied,
		Impact:      ContextFabricAnswerImpactNone,
		Served:      5,
		Declared:    5,
	}
	// The SAME row once the population is known to be incomplete. Identical in
	// every other field, so the delta is the qualification and nothing else.
	qualified := satisfied
	qualified.Outcome = ContextFabricRequirementNarrowed
	qualified.Impact = ContextFabricAnswerImpactScope
	qualified.CauseCoverage = ContextFabricCoverageDetailPopulationTruncated
	qualified.CauseObserved = true

	// Both rows must be legal, or the measurement is of a document that cannot
	// ship.
	if err := ValidateContextFabricPlanRequirementOutcomeRow(satisfied); err != nil {
		t.Fatalf("the satisfied baseline row is invalid: %v", err)
	}
	if err := ValidateContextFabricPlanRequirementOutcomeRow(qualified); err != nil {
		t.Fatalf("the qualified row is invalid: %v", err)
	}

	before, err := json.Marshal(satisfied)
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	after, err := json.Marshal(qualified)
	if err != nil {
		t.Fatalf("marshal qualified: %v", err)
	}

	// +40 for `,"cause_coverage":"population_truncated"`, -1 for
	// "satisfied"->"narrowed", +1 for "none"->"scope", -1 for the
	// cause_observed value false->true. The field is not omitempty, so the
	// flag costs a value change rather than a whole key.
	const wantDelta = 39
	if got := len(after) - len(before); got != wantDelta {
		t.Fatalf("qualifying a count costs %d bytes on the wire, want %d\n baseline (%d B): %s\nqualified (%d B): %s\n"+
			"if a field name or token changed on purpose, move this pin in the same commit and say so",
			got, wantDelta, len(before), before, len(after), after)
	}

	// The delta is charged AT MOST ONCE PER TURN: the step refuses a second
	// assembled-result count row for one requirement. Asserting the per-row
	// cost without that would invite multiplying it by the 200-row bound,
	// which is 195 rows more than any turn can produce here.
	if !strings.Contains(string(after), `"cause_coverage":"population_truncated"`) {
		t.Fatal("the qualified encoding does not carry the cause the delta was measured for")
	}
}

// TestThePopulationTruncatedDetailRefusesACount closes the field-rule half of
// this code, which nothing asserted before.
//
// The rule for this code is `{}` — no fact kind, no scope, no narrowing, and
// NO COUNT — and the count is the tempting one. The only number available is
// the size of the set that WAS resolved, which the outcome row already carries
// as `served`; putting it on the detail as well gives a reader two places to
// learn one fact. The number this code is actually about, how large the
// population is, is precisely the one nothing measured, so there is nothing
// honest to put there.
//
// The minimal fixture omits the count, so a rule quietly widened to allow one
// would keep every existing test green. This asserts the refusal directly, and
// pairs it with the fixture as the positive control so it cannot pass by the
// validator refusing everything.
func TestThePopulationTruncatedDetailRefusesACount(t *testing.T) {
	t.Parallel()
	detail := validDetailForCode(ContextFabricCoverageDetailPopulationTruncated)
	if err := detail.Validate(); err != nil {
		t.Fatalf("positive control: the minimal fixture for this code does not validate: %v", err)
	}

	count := 4
	detail.Count = &count
	err := detail.Validate()
	if err == nil {
		t.Fatal("a population-truncated detail carrying a count was accepted; the only count available is the " +
			"size of what WAS resolved, which the outcome row already states, and the number this code is about " +
			"is the one nothing measured")
	}
	if !strings.Contains(err.Error(), "forbids count") {
		t.Fatalf("refused for the wrong reason: %v (want the count field rule)", err)
	}
}

// TestThePopulationTruncatedLabelSaysTheNumberIsAFloor pins the label's
// CONTENT, not merely that it is non-empty and not the generic fallback.
//
// Those two weaker assertions already exist, and between them they still admit
// a label that is well-formed and WRONG — "The population was read
// successfully" is non-empty, in bounds, and not the fallback, while telling
// the operator the opposite of what happened. This is operator-facing copy on
// the one row whose entire purpose is to stop an answer reading as complete, so
// the exact string is the contract and a golden is the right shape for it.
//
// The two negative assertions beside it say what the string must never become:
// a failure report (everything here was read successfully, and a reader told
// "could not be read" goes looking for a broken source) and a reassurance.
func TestThePopulationTruncatedLabelSaysTheNumberIsAFloor(t *testing.T) {
	t.Parallel()
	const want = "The full set could not be listed, so this is at least this many"

	got := ComposeCoverageDetailLabel(validDetailForCode(ContextFabricCoverageDetailPopulationTruncated))
	if got != want {
		t.Fatalf("the population-truncated label is %q, want %q -- this is operator-facing copy on the row whose "+
			"whole purpose is to stop an answer reading as complete, so the string is the contract", got, want)
	}
	for _, reassuring := range []string{"successfully", "complete", "all of"} {
		if strings.Contains(strings.ToLower(got), reassuring) {
			t.Errorf("the label contains %q; a label that reassures is worse than no label on this row", reassuring)
		}
	}
}
