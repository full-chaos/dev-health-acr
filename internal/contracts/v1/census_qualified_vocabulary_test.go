package v1

// The vocabulary half of the census exception.
//
// These two tests reference identifiers that do not exist at the parent
// commit, so they cannot be part of the red-first proof -- a file that does
// not compile proves an identifier is absent, never that behaviour is wrong.
// They are pinned by the mutation battery instead: deleting the vocabulary
// member and widening the allow-list both have to turn something red here.

import "testing"

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
