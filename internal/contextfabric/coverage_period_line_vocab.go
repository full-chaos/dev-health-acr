package contextfabric

import "sort"

// The closed vocabularies of the coverage, status category composition and
// period delta lines, exported so the event specification reads them from the
// producer's own constants and tables.

// CoverageDisclosureOutcomeVocabulary is the closed set of outcomes a coverage disclosure phrasing line names.
func CoverageDisclosureOutcomeVocabulary() []string {
	return []string{
		string(CoverageDisclosurePhrased), string(CoverageDisclosurePartialAbsent), string(CoverageDisclosureRejectedByGuard),
		string(CoverageDisclosureDiscardedUndecodable), string(CoverageDisclosureAbsent),
	}
}

// CoverageDisclosureViolationVocabulary is the closed set of violations a coverage disclosure phrasing line names; the empty violation is a set the guard kept.
func CoverageDisclosureViolationVocabulary() []string {
	return []string{
		"", string(CoverageDisclosureViolationUnknownDetailID), string(CoverageDisclosureViolationUnknownToModelFacingSet),
		string(CoverageDisclosureViolationDuplicateDetailID), string(CoverageDisclosureViolationTextBound),
		string(CoverageDisclosureViolationDigitsForbidden), string(CoverageDisclosureViolationParseFailed),
	}
}

// StatusCategoryComposedSubjectKindVocabulary is the closed set of subject kinds a status category composition line names: the keys of the composition table.
func StatusCategoryComposedSubjectKindVocabulary() []string {
	kinds := make([]string, 0, len(statusCategoryFactKindComposition))
	for kind := range statusCategoryFactKindComposition {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	return kinds
}

// StatusCategoryComposedFactKindVocabulary is the closed set of fact kinds a composition line names: the values of the composition table.
func StatusCategoryComposedFactKindVocabulary() []string {
	seen := map[string]bool{}
	for _, composed := range statusCategoryFactKindComposition {
		for _, kind := range composed {
			seen[string(kind)] = true
		}
	}
	kinds := make([]string, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// StatusCategoryRequirementKindVocabulary is the one requirement kind a composition or period delta line names.
func StatusCategoryRequirementKindVocabulary() []string { return []string{string(FactStatus)} }

// PeriodDeltaSubjectKindVocabulary is the closed set of subject kinds, and grains, a period delta line names.
func PeriodDeltaSubjectKindVocabulary() []string {
	kinds := periodDeltaEligibleSubjectKinds()
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, string(kind))
	}
	return out
}

// PeriodDeltaFailureReasonVocabulary is the closed set of failure reasons a period delta line names; the empty reason is a comparison that ran.
func PeriodDeltaFailureReasonVocabulary() []string {
	return []string{
		string(PeriodDeltaFailureNone), string(PeriodDeltaFailureAnchorUnresolved), string(PeriodDeltaFailureDeadlineExceeded),
		string(PeriodDeltaFailureCanceled), string(PeriodDeltaFailureReadFailed),
	}
}

// PeriodDeltaTransitionLogVocabulary is the closed set of transitions a period delta line counts, as the keys of its transition counts.
func PeriodDeltaTransitionLogVocabulary() []string {
	out := make([]string, 0, len(periodDeltaTransitions))
	for _, transition := range periodDeltaTransitions {
		out = append(out, string(transition))
	}
	return out
}
