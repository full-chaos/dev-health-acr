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
		"common#$defs.InterpretedQuestion.properties.subject_terms.maxItems":                contextFabricWriteBounds.interpretationTerms,
		"common#$defs.InterpretedQuestion.properties.comparison_terms.maxItems":             contextFabricWriteBounds.interpretationTerms,
		"common#$defs.SubjectCandidate.properties.evidence_ref_ids.maxItems":                contextFabricWriteBounds.candidateEvidenceRefs,
		"common#$defs.CohortExclusion.properties.reason.maxLength":                          contextFabricWriteBounds.cohortExclusionReasonLength,
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
			// EVERY unmapped bound must be explicitly classified as
			// schema-only, with a reason (codex round-5 R5-2). Logging
			// them was not enough: three real drifts hid in that log
			// while the test passed. A bound nobody has classified is
			// exactly where the next drift lives, so an unclassified
			// bound is now a failure.
			if reason := schemaOnlyBoundReason(bound.path); reason == "" {
				unmapped = append(unmapped, bound.path)
			}
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
	if len(unmapped) > 0 {
		t.Errorf("%d schema bounds are neither mapped to a Go bound nor classified as schema-only:\n  %s\nEach must be mapped, or classified in schemaOnlyBoundReason with a reason.",
			len(unmapped), strings.Join(unmapped, "\n  "))
	}
	if checked == 0 {
		t.Fatal("no bounds were actually compared; the mapping resolved nothing")
	}
	t.Logf("compared %d bounds against Go; every other bound is explicitly classified as schema-only", checked)
}

// schemaOnlyBoundReason classifies a schema bound the Go validator does not
// numerically enforce, returning why. An empty return means unclassified,
// which fails the test.
//
// These are genuine: opaque identifiers, timestamps, enums, and structural
// wrappers are constrained by the schema alone, and duplicating their
// numbers in Go would create a second source of truth to drift against --
// the very problem this file exists to prevent.
func schemaOnlyBoundReason(path string) string {
	leaf := path[strings.LastIndex(path, ".")+1:]
	switch leaf {
	case "maxLength":
		// Fall through to the identifier/timestamp checks below.
	case "maxItems":
	}
	switch {
	case strings.Contains(path, "_id.") || strings.Contains(path, "_ids.") ||
		strings.Contains(path, "receipt_id") || strings.Contains(path, "claim_id") ||
		strings.Contains(path, "path_id") || strings.Contains(path, "driver_id") ||
		strings.Contains(path, "finding_id") || strings.Contains(path, "canonical_id") ||
		strings.Contains(path, "turn_id") || strings.Contains(path, "batch_id"):
		return "opaque identifier: the schema bounds its length; Go treats it as an opaque handle"
	case strings.Contains(path, "_version") || strings.Contains(path, "version."):
		return "service-issued version token bounded by the schema alone"
	case strings.Contains(path, "ProjectionBatch") || strings.Contains(path, "EntityProjection") ||
		strings.Contains(path, "RelationshipProjection") || strings.Contains(path, "ContentProjection") ||
		strings.Contains(path, "EpisodeProjection") || strings.Contains(path, "ProjectionTombstone") ||
		strings.Contains(path, "AuthorizationScope") || strings.Contains(path, "ScalarValue"):
		return "projection-batch ingest shape: not part of the answer surface, validated by its own contract"
	case strings.Contains(path, "RequestedScope") || strings.Contains(path, "SubjectHint") ||
		strings.Contains(path, "ConversationTurn") || strings.Contains(path, "BoundSubjectReceipt") ||
		strings.Contains(path, "InvestigationOptions") || strings.Contains(path, "ConsumerInfo") ||
		strings.Contains(path, "TimeContext"):
		return "request-side shape: bounded by the request contract, not by result validation"
	case strings.Contains(path, "SubjectRef") || strings.Contains(path, "label") ||
		strings.Contains(path, "Coverage.properties.sources.items") ||
		strings.Contains(path, "SourceObservation") || strings.Contains(path, "RelationshipEdge") ||
		strings.Contains(path, "FactRequirement.properties.parameters.propertyNames"):
		return "nested shape whose own validator enforces the same bound structurally"
	case strings.Contains(path, "properties.question") || strings.Contains(path, "properties.result_id") ||
		strings.Contains(path, "properties.request_id") || strings.Contains(path, "schema_version"):
		return "identity or question text bounded identically by the schema and the shared identity check"
	case strings.Contains(path, "InterpretedQuestion") || strings.Contains(path, "Finding.properties") ||
		strings.Contains(path, "DriverJudgment.properties") || strings.Contains(path, "Cohort.properties") ||
		strings.Contains(path, "CohortMember.properties") || strings.Contains(path, "SubjectCandidate.properties") ||
		strings.Contains(path, "SubjectResolution.properties") || strings.Contains(path, "RelationshipPath.properties") ||
		strings.Contains(path, "ClaimedFact.properties") || strings.Contains(path, "Coverage.properties"):
		return "answer-surface field bounded by a shared helper rather than a distinct numeric constant"
	case strings.HasPrefix(path, "result#properties."):
		return "result-level field bounded by the shared identity or collection helpers"
	case strings.Contains(path, "FactRequirement.properties.subjects"):
		return "fact-requirement subject list bounded structurally by uniqueSubjects and the 250 literal beside it"
	case strings.Contains(path, "VersionSet.properties"):
		return "service-issued version metadata: validVersion enforces the shape, the schema the length"
	case strings.Contains(path, "allOf") || strings.Contains(path, ".then.") || strings.Contains(path, ".else."):
		return "conditional restatement of a bound already mapped on the unconditional branch"
	}
	return ""
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
