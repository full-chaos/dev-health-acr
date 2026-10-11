package v1

import (
	"sort"
	"testing"
)

func sourceTruncationDepthRow(code ContextFabricCoverageDetailCode) ContextFabricPlanRequirementOutcomeRow {
	return ContextFabricPlanRequirementOutcomeRow{
		Requirement: "remaining_work/member/team", Obligation: "remaining_work",
		Stage:   ContextFabricOutcomeStageAssembledResult,
		Outcome: ContextFabricRequirementNarrowed, Impact: ContextFabricAnswerImpactDepth,
		CauseCoverage: code, CauseObserved: true, Served: 1, Declared: 1,
	}
}

// The codes that may carry equal counts on a truncated source, written out so
// that adding one is a conscious change of this list and of the predicate.
var frozenSourceTruncationCauses = []ContextFabricCoverageDetailCode{
	ContextFabricCoverageDetailFactProviderReported,
	ContextFabricCoverageDetailFactScopeUnexpanded,
	ContextFabricCoverageDetailFactReadFailed,
	ContextFabricCoverageDetailFactUnconfigured,
}

func TestSourceTruncationCausesAreTheFrozenSet(t *testing.T) {
	var derived []string
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		if coverageDetailCodeQualifiesSourceTruncation(code) {
			derived = append(derived, string(code))
		}
	}
	var written []string
	for _, code := range frozenSourceTruncationCauses {
		written = append(written, string(code))
	}
	sort.Strings(derived)
	sort.Strings(written)
	if len(derived) != len(written) {
		t.Fatalf("source-truncation causes = %v, frozen list = %v: a changed set is a conscious edit of both", derived, written)
	}
	for index := range derived {
		if derived[index] != written[index] {
			t.Fatalf("source-truncation causes = %v, frozen list = %v", derived, written)
		}
	}
}

// Equal counts on a depth row are a truncated source only for a source-level
// cause; every other code in the vocabulary still reads "not a reduction".
func TestEqualCountsOnADepthRowAreAdmittedOnlyForSourceTruncationCauses(t *testing.T) {
	frozen := map[ContextFabricCoverageDetailCode]bool{}
	for _, code := range frozenSourceTruncationCauses {
		frozen[code] = true
	}
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		err := ValidateContextFabricPlanRequirementOutcomeRow(sourceTruncationDepthRow(code))
		switch {
		case frozen[code] && err != nil:
			t.Errorf("code %q is a source-truncation cause but the row is refused: %v", code, err)
		case !frozen[code] && err == nil:
			t.Errorf("code %q is not a source-truncation cause but equal counts on a depth row were admitted", code)
		}
	}
}
