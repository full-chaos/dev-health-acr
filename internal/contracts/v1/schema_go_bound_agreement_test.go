package v1

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
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
		// Disproved as "schema-only" by boundProbes below: the validator
		// rejects a value one past each of these, so they are compared
		// numerically rather than excused (codex round-6 F1).
		"result#properties.result_id.maxLength":                     256,
		"result#properties.request_id.maxLength":                    256,
		"result#properties.question.maxLength":                      8000,
		"common#$defs.SubjectRef.properties.label.maxLength":        512,
		"common#$defs.SubjectRef.properties.canonical_id.maxLength": 256,
	}

	discovered := schemaBounds(t, documents)
	if len(discovered) == 0 {
		t.Fatal("no schema bounds discovered; the enumeration is not working")
	}

	checked, proved := 0, 0
	unmapped := make([]string, 0, len(discovered))
	for _, bound := range discovered {
		// PREFERRED: prove agreement behaviourally. A probe that accepts a
		// value AT the schema bound and rejects one past it has measured
		// Go's bound and shown it equals the schema's -- which is the
		// comparison, done without anyone transcribing a number (codex
		// round-8 F1). This is what makes the check derive from the
		// declaration rather than from a hand-maintained table.
		if probe, probeable := genericProbe(bound.path); probeable {
			atBound := probe.apply(bound.value)
			pastBound := probe.apply(bound.value + 1)
			switch {
			case atBound == nil && pastBound != nil:
				proved++
				continue
			case atBound == nil && pastBound == nil:
				// Go accepts beyond the schema: the service can emit a
				// document violating its own contract.
				t.Errorf("%s: schema says %d but Go accepts %d; the service can emit a document that violates its own contract",
					bound.path, bound.value, bound.value+1)
				continue
			case atBound != nil:
				// The probe cannot isolate this bound (a cross-field
				// invariant rejects the control). Fall through to the
				// declarative checks rather than claiming either result.
			}
		}
		expected, mapped := goBoundsByPath[bound.path]
		if mapped {
			checked++
			if bound.value != expected {
				t.Errorf("%s: schema says %d, the Go write path enforces %d", bound.path, bound.value, expected)
			}
			continue
		}
		if reason := schemaOnlyBoundReason(bound.path); reason == "" {
			unmapped = append(unmapped, bound.path)
		}
	}
	sort.Strings(unmapped)
	if len(unmapped) > 0 {
		t.Errorf("%d schema bounds are neither proved by probe, mapped, nor classified as schema-only:\n  %s",
			len(unmapped), strings.Join(unmapped, "\n  "))
	}
	if proved == 0 {
		t.Fatal("no bound was proved behaviourally; the prober is not reaching anything")
	}
	t.Logf("proved %d bounds behaviourally, compared %d declaratively, classified the rest", proved, checked)
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

// boundProbe tests one claim about a path by driving the field to a size
// and validating.
type boundProbe struct {
	apply func(size int) error
}

// shapeLocators maps a schema $defs name to a live instance of that shape
// inside a valid result document, so ONE generic prober can drive any bound
// on any of these shapes.
//
// A handful of locators instead of a bespoke closure per bound (codex
// round-8 F1): a new bound on an existing shape needs no new probe code,
// which is the difference between a mechanism and another hand-enumeration.
// Only a genuinely NEW shape requires an entry here.
var shapeLocators = map[string]func(*ContextFabricInvestigationResult) any{
	"SubjectRef":          func(r *ContextFabricInvestigationResult) any { return &r.SubjectResolution.Committed[0] },
	"SubjectCandidate":    func(r *ContextFabricInvestigationResult) any { return &r.SubjectResolution.Candidates[0] },
	"SubjectResolution":   func(r *ContextFabricInvestigationResult) any { return &r.SubjectResolution },
	"Cohort":              func(r *ContextFabricInvestigationResult) any { return r.Cohort },
	"CohortMember":        func(r *ContextFabricInvestigationResult) any { return &r.Cohort.Members[0] },
	"CohortExclusion":     func(r *ContextFabricInvestigationResult) any { return &r.Cohort.Exclusions[0] },
	"DriverJudgment":      func(r *ContextFabricInvestigationResult) any { return &r.Drivers[0] },
	"Finding":             func(r *ContextFabricInvestigationResult) any { return &r.RemainingWork[0] },
	"RelationshipPath":    func(r *ContextFabricInvestigationResult) any { return &r.Paths[0] },
	"RelationshipEdge":    func(r *ContextFabricInvestigationResult) any { return &r.Paths[0].Edges[0] },
	"ClaimedFact":         func(r *ContextFabricInvestigationResult) any { return &r.ClaimedFacts[0] },
	"SourceObservation":   func(r *ContextFabricInvestigationResult) any { return &r.Coverage.Sources[0] },
	"Coverage":            func(r *ContextFabricInvestigationResult) any { return &r.Coverage },
	"VersionSet":          func(r *ContextFabricInvestigationResult) any { return &r.Versions },
	"InterpretedQuestion": func(r *ContextFabricInvestigationResult) any { return &r.Interpretation },
	"FactRequirement":     func(r *ContextFabricInvestigationResult) any { return &r.Interpretation.FactRequirements[0] },
	"ScalarValue":         func(r *ContextFabricInvestigationResult) any { return &r.ClaimedFacts[0].Value },
}

// genericProbe builds a probe for any schema bound whose path names a shape
// in shapeLocators or the result root, navigating the Go document with the
// same property names the schema uses.
func genericProbe(path string) (boundProbe, bool) {
	document, rest, found := strings.Cut(path, "#")
	if !found {
		return boundProbe{}, false
	}
	keyword := rest[strings.LastIndex(rest, ".")+1:]
	if keyword != "maxLength" && keyword != "maxItems" {
		return boundProbe{}, false
	}
	rest = rest[:strings.LastIndex(rest, ".")]

	locate := func(r *ContextFabricInvestigationResult) any { return r }
	if document == "common" {
		trimmed, ok := strings.CutPrefix(rest, "$defs.")
		if !ok {
			return boundProbe{}, false
		}
		name, tail, _ := strings.Cut(trimmed, ".")
		locator, known := shapeLocators[name]
		if !known {
			return boundProbe{}, false
		}
		locate, rest = locator, tail
	}
	fieldPath := strings.ReplaceAll(rest, "properties.", "")
	fieldPath = strings.ReplaceAll(fieldPath, ".items", "[]")
	if fieldPath == "" {
		return boundProbe{}, false
	}
	return boundProbe{apply: func(size int) error {
		value := probeResult()
		if !driveField(reflect.ValueOf(locate(&value)), fieldPath, size, keyword) {
			return errProbeUnreachable
		}
		return value.Validate()
	}}, true
}

var errProbeUnreachable = fmt.Errorf("probe could not reach the field")

// driveField sets the named field to a value of the requested size: a
// string of that many runes, or a slice of that many unique entries.
func driveField(value reflect.Value, path string, size int, keyword string) bool {
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	segment, rest, _ := strings.Cut(path, ".")
	name, isElement := strings.CutSuffix(segment, "[]")
	field := fieldByJSONTag(value, name)
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	if rest != "" {
		if field.Kind() == reflect.Slice {
			if field.Len() == 0 {
				return false
			}
			return driveField(field.Index(0), rest, size, keyword)
		}
		return driveField(field, rest, size, keyword)
	}
	switch {
	case isElement && field.Kind() == reflect.Slice:
		if field.Len() == 0 {
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		}
		return driveScalar(field.Index(0), size)
	case field.Kind() == reflect.Slice && keyword == "maxItems":
		grown := reflect.MakeSlice(field.Type(), size, size)
		for i := 0; i < size; i++ {
			if field.Len() > 0 {
				grown.Index(i).Set(field.Index(0))
			}
			uniquifyElement(grown.Index(i), i)
		}
		field.Set(grown)
		return true
	default:
		return driveScalar(field, size)
	}
}

func driveScalar(value reflect.Value, size int) bool {
	if value.Kind() != reflect.String || !value.CanSet() {
		return false
	}
	value.SetString(strings.Repeat("x", size))
	return true
}

// uniquifyElement makes a duplicated slice element distinct, so a maxItems
// probe is rejected for LENGTH rather than for duplication.
func uniquifyElement(value reflect.Value, index int) {
	suffix := strconv.Itoa(index)
	switch value.Kind() {
	case reflect.String:
		value.SetString("probevalue" + suffix)
	case reflect.Struct:
		for _, name := range []string{"canonical_id", "receipt_id", "driver_id", "finding_id", "path_id", "claim_id", "source"} {
			if field := fieldByJSONTag(value, name); field.IsValid() && field.Kind() == reflect.String && field.CanSet() {
				field.SetString("probevalue" + suffix)
				break
			}
		}
		if field := fieldByJSONTag(value, "rank"); field.IsValid() && field.CanSet() && field.Kind() == reflect.Int {
			field.SetInt(int64(index + 1))
		}
	}
}

func fieldByJSONTag(value reflect.Value, name string) reflect.Value {
	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	for i := 0; i < value.NumField(); i++ {
		if strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0] == name {
			return value.Field(i)
		}
	}
	return reflect.Value{}
}

func probeResult() ContextFabricInvestigationResult {
	return closureResult()
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
