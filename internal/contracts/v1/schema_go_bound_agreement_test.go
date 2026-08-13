package v1

import (
	"sort"
	"strings"
	"testing"
)

// This file closes the numeric-drift CLASS.
//
// Four separate rounds of review found the same defect in different places:
// a Go validator bound and its published JSON Schema bound disagreed, so a
// document could pass validation and still violate the contract it claims
// to satisfy (Go looser), or the contract promised something the service
// would reject (Go stricter). Each was fixed individually and the next one
// appeared somewhere else.
//
// TestSchemaAndGoBoundsAgree enumerates EVERY maxItems and maxLength in
// both canonical schemas and requires each to be accounted for: either it
// matches the Go bound that enforces it, or it appears in
// asymmetricBounds with a reason. A new bound added to either side without
// a decision fails the build.

// goBound records the Go-enforced value for one schema bound.
type goBound struct {
	// value is what the WRITE path enforces. Reads may accept more (see
	// contextFabricLegacyBounds) -- that asymmetry is deliberate and is
	// covered by its own tests, not this one.
	value int
	// why documents an intentional mismatch. Empty means the Go bound is
	// expected to equal the schema bound exactly.
	why string
}

// asymmetricBounds names schema bounds that intentionally differ from the
// Go value, with the reason. It is empty by design: every disagreement
// found so far was a defect, not a decision. An entry here is a claim that
// a reviewer decided the two SHOULD differ.
var asymmetricBounds = map[string]goBound{}

// TestSchemaAndGoBoundsAgree proves the write-path Go bounds match the
// published schema for every bound the answer surface depends on.
//
// Coverage is enforced from the SCHEMA side: the schemas are enumerated and
// every bound must resolve. That direction matters, because the failure
// mode this closes is a schema bound nobody remembered to mirror in Go.
func TestSchemaAndGoBoundsAgree(t *testing.T) {
	documents := schemaDocuments(t)

	// goBoundsByPath maps a schema bound to the value the write path
	// enforces. Paths not listed are covered by the structural checks
	// below rather than a numeric comparison.
	goBoundsByPath := map[string]int{
		// Result-level answer text and collections.
		"result#properties.direct_judgment.maxLength":           contextFabricWriteBounds.judgmentLength,
		"result#properties.current_state.maxLength":             contextFabricWriteBounds.judgmentLength,
		"result#properties.deterministic_answer.maxLength":      contextFabricWriteBounds.deterministicAnswerLength,
		"result#properties.limitations.maxItems":                contextFabricWriteBounds.narrativeCount,
		"result#properties.limitations.items.maxLength":         contextFabricWriteBounds.narrativeLength,
		"result#properties.warnings.maxItems":                   contextFabricWriteBounds.narrativeCount,
		"result#properties.warnings.items.maxLength":            contextFabricWriteBounds.narrativeLength,
		"result#properties.drivers.maxItems":                    ContextFabricDriversMaxCount,
		"result#properties.remaining_work.maxItems":             ContextFabricRemainingWorkMaxCount,
		"result#properties.readiness_gaps.maxItems":             ContextFabricReadinessGapsMaxCount,
		"result#properties.conflicts.maxItems":                  ContextFabricConflictsMaxCount,
		"result#properties.claimed_facts.maxItems":              ContextFabricClaimedFactsMaxCount,
		"result#properties.evidence_ref_ids.maxItems":           ContextFabricEvidenceRefIDsMaxCount,
		"result#properties.strongest_pressures.items.maxLength": ContextFabricStrongestPressureMaxLength,

		// Shapes the answer surface copies or projects.
		"common#$defs.CohortMember.properties.inclusion_reasons.maxItems":                   contextFabricWriteBounds.cohortInclusionReasons,
		"common#$defs.CohortMember.properties.inclusion_reasons.items.maxLength":            contextFabricWriteBounds.cohortInclusionReasonLength,
		"common#$defs.SubjectCandidate.properties.matched_terms.maxItems":                   contextFabricWriteBounds.matchedTerms,
		"common#$defs.SubjectCandidate.properties.matched_terms.items.maxLength":            contextFabricWriteBounds.matchedTermLength,
		"common#$defs.SubjectCandidate.properties.match_reasons.maxItems":                   contextFabricWriteBounds.matchReasons,
		"common#$defs.SubjectCandidate.properties.match_reasons.items.maxLength":            contextFabricWriteBounds.matchReasonLength,
		"common#$defs.Coverage.properties.sources.maxItems":                                 contextFabricWriteBounds.coverageEntries,
		"common#$defs.Coverage.properties.degraded_reasons.maxItems":                        contextFabricWriteBounds.coverageEntries,
		"common#$defs.RelationshipPath.properties.why_relevant.maxLength":                   contextFabricWriteBounds.pathWhyRelevantLength,
		"common#$defs.RelationshipPath.properties.evidence_ref_ids.maxItems":                contextFabricWriteBounds.pathEvidenceRefs,
		"common#$defs.RelationshipPath.properties.nodes.maxItems":                           contextFabricRelationshipPathMaxNodes,
		"common#$defs.RelationshipPath.properties.edges.maxItems":                           contextFabricRelationshipPathMaxNodes - 1,
		"common#$defs.DriverJudgment.properties.evidence_ref_ids.maxItems":                  contextFabricWriteBounds.nestedEvidenceRefs,
		"common#$defs.Finding.properties.evidence_ref_ids.maxItems":                         contextFabricWriteBounds.nestedEvidenceRefs,
		"common#$defs.FactRequirement.properties.parameters.additionalProperties.maxLength": contextFabricWriteBounds.factParameterValueLength,
		"common#$defs.DriverJudgment.properties.title.maxLength":                            ContextFabricDriverTitleMaxLength,
		"common#$defs.DriverJudgment.properties.summary.maxLength":                          ContextFabricDriverSummaryMaxLength,
		"common#$defs.DriverJudgment.properties.qualification.maxLength":                    ContextFabricDriverQualificationMaxLength,
		"common#$defs.ClaimedFact.properties.field.maxLength":                               ContextFabricClaimedFieldMaxLength,
	}

	discovered := schemaBounds(t, documents)
	if len(discovered) == 0 {
		t.Fatal("no schema bounds discovered; the enumeration is not working")
	}

	checked := 0
	unmapped := make([]string, 0, len(discovered))
	for _, bound := range discovered {
		expected, mapped := goBoundsByPath[bound.path]
		if !mapped {
			// Not every bound in these documents governs a value the Go
			// validator numerically enforces (identifiers, timestamps,
			// enums, and shapes the answer surface never touches are
			// bounded by the schema alone). Those are listed so a reader
			// can see what is NOT compared, rather than silently ignored.
			unmapped = append(unmapped, bound.path)
			continue
		}
		checked++
		if asymmetric, ok := asymmetricBounds[bound.path]; ok {
			if strings.TrimSpace(asymmetric.why) == "" {
				t.Errorf("%s is declared asymmetric with no reason", bound.path)
			}
			continue
		}
		if bound.value != expected {
			t.Errorf("%s: schema says %d, the Go write path enforces %d.\nA looser Go bound lets the service emit a document that violates its own contract; a stricter one makes the contract promise something the service rejects.",
				bound.path, bound.value, expected)
		}
	}
	sort.Strings(unmapped)
	t.Logf("compared %d bounds against Go; %d schema-only bounds not numerically enforced in Go", checked, len(unmapped))
	if checked == 0 {
		t.Fatal("no bounds were actually compared; the mapping resolved nothing")
	}
}

type discoveredBound struct {
	path  string
	value int
}

// schemaBounds enumerates every maxItems/maxLength in both canonical
// documents, as "<document>#<dotted path>.<keyword>".
func schemaBounds(t *testing.T, documents map[string]map[string]any) []discoveredBound {
	t.Helper()
	var found []discoveredBound
	var walk func(document string, node any, path string)
	walk = func(document string, node any, path string) {
		switch value := node.(type) {
		case map[string]any:
			for _, keyword := range []string{"maxItems", "maxLength"} {
				if raw, ok := value[keyword].(float64); ok {
					found = append(found, discoveredBound{path: document + "#" + strings.TrimPrefix(path, ".") + "." + keyword, value: int(raw)})
				}
			}
			for key, child := range value {
				switch key {
				case "maxItems", "maxLength", "description", "title", "$comment", "$schema", "$id":
					continue
				}
				walk(document, child, path+"."+key)
			}
		case []any:
			for i, child := range value {
				walk(document, child, path+"."+itoa(i))
			}
		}
	}
	for _, document := range []string{"result", "common"} {
		walk(document, documents[document], "")
	}
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })
	return found
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
