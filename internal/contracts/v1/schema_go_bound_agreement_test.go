package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
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

// asymmetricBounds names schema bounds the behavioural probe cannot judge,
// with the reason. Every entry is a claim that a reviewer looked and found
// the two sides agree despite what the probe measured -- it is not a place
// to park a disagreement.
//
// It was empty until round 18, when enumerating the minimum-side keywords
// reached the first bound of this shape. That is worth keeping in mind
// before adding a second: every disagreement found before this one was a
// defect.
var asymmetricBounds = map[string]goBound{
	// An OPTIONAL string with a minLength. The Go field is
	// `model_identity,omitempty`, so an empty Go string serializes to an
	// ABSENT property -- and model_identity is not in VersionSet.required,
	// so absence is exactly what the schema permits. minLength binds only
	// a property that is present, which is precisely what Go enforces:
	// `v.ModelIdentity != "" && !validModelIdentity(...)`.
	//
	// The probe reports "Go accepts 0" because it can only drive the Go
	// value to the empty string, which it cannot distinguish from the
	// absent field that empty string actually becomes. The two sides
	// agree; the instrument cannot express the difference.
	"common#$defs.VersionSet.properties.model_identity.minLength": {
		value: 1,
		why:   "optional field: an empty Go string is an omitted JSON property, which the schema allows, and minLength binds only a present one",
	},
}

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
		"result#properties.direct_judgment.maxLength":      contextFabricWriteBounds.judgmentLength,
		"result#properties.current_state.maxLength":        contextFabricWriteBounds.judgmentLength,
		"result#properties.deterministic_answer.maxLength": contextFabricWriteBounds.deterministicAnswerLength,
		"result#properties.limitations.maxItems":           contextFabricWriteBounds.narrativeCount,
		// The displaced count can never exceed the list it counts drops
		// from, so it derives from the same write bound rather than
		// naming a number of its own.
		"result#properties.limitations_displaced.maximum": contextFabricWriteBounds.narrativeCount,
		// The floor is mapped explicitly, not left to the pattern
		// classifier. The "result#properties." catch-all would otherwise
		// swallow it as "bounded by the shared helpers", which is not true
		// of this field: Go rejects a negative count with its own clause.
		// Round-18's ruled mutation (minimum 0 -> 1) passed against that
		// catch-all before this entry existed.
		"result#properties.limitations_displaced.minimum":       0,
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
		// Not probeable, and honestly so (codex round-9 F1): no valid
		// document can carry more fact_requirements than there are distinct
		// kinds, so the probe cannot build a control past the vocabulary and
		// reports unreachable instead of a rejection. Mapped explicitly here
		// rather than left to the result-level catch-all classification,
		// which is what let the 50-vs-64 drift sit unnoticed.
		"common#$defs.InterpretedQuestion.properties.fact_requirements.maxItems": ContextFabricFactRequirementsMaxCount,
		"common#$defs.FactRequirement.properties.parameters.maxProperties":       ContextFabricFactRequirementParametersMaxCount,
		// Also unprobeable, for the same honest reason (codex round-12):
		// Finding.kind is a closed vocabulary, so no filler string of any
		// length is a valid value and the probe cannot isolate the length
		// bound. Mapped rather than left to the Finding.properties
		// classification, which would have absorbed it silently -- the
		// measurement showed it sliding from proved into the residual
		// bucket the moment the vocabulary closed.
		"common#$defs.Finding.properties.kind.maxLength":                     ContextFabricFindingKindMaxLength,
		"common#$defs.SubjectCandidate.properties.evidence_ref_ids.maxItems": contextFabricWriteBounds.candidateEvidenceRefs,
		"common#$defs.CohortExclusion.properties.reason.maxLength":           contextFabricWriteBounds.cohortExclusionReasonLength,
		// Disproved as "schema-only" by boundProbes below: the validator
		// rejects a value one past each of these, so they are compared
		// numerically rather than excused (codex round-6 F1).
		"result#properties.result_id.maxLength":                     256,
		"result#properties.request_id.maxLength":                    256,
		"result#properties.question.maxLength":                      8000,
		"common#$defs.SubjectRef.properties.label.maxLength":        512,
		"common#$defs.SubjectRef.properties.canonical_id.maxLength": 256,
		// CHAOS-3900 W1: ContextFabricWindowClarification.Validate's own
		// bounds (validate_context_fabric_window.go) -- options is
		// required non-empty (minItems 1, "window clarification options
		// violate v1 bounds" on len==0) and capped at
		// contextFabricWindowClarificationMaxOptions.
		"common#$defs.WindowClarification.properties.options.maxItems": contextFabricWindowClarificationMaxOptions,
		"common#$defs.WindowClarification.properties.options.minItems": 1,
	}

	discovered := schemaBounds(t, documents)
	if len(discovered) == 0 {
		t.Fatal("no schema bounds discovered; the enumeration is not working")
	}

	checked, proved := 0, 0
	exempted := make(map[string]bool, len(asymmetricBounds))
	unmapped := make([]string, 0, len(discovered))
	for _, bound := range discovered {
		// PREFERRED: prove agreement behaviourally. A probe that accepts a
		// value AT the schema bound and rejects one past it has measured
		// Go's bound and shown it equals the schema's -- which is the
		// comparison, done without anyone transcribing a number (codex
		// round-8 F1). This is what makes the check derive from the
		// declaration rather than from a hand-maintained table.
		// A reviewed decision short-circuits the instrument. Consulted
		// BEFORE the probe, because the probe is exactly what an entry
		// here exists to overrule -- and because asymmetricBounds was
		// declared and never read until round 18, which nothing noticed
		// only because it was empty.
		if entry, exempt := asymmetricBounds[bound.path]; exempt {
			exempted[bound.path] = true
			if entry.why == "" {
				t.Errorf("%s is listed in asymmetricBounds with no reason; an unexplained exemption is indistinguishable from the defect", bound.path)
			}
			if entry.value != bound.value {
				t.Errorf("%s: asymmetricBounds records %d, the schema now says %d; the exemption was written against a different bound", bound.path, entry.value, bound.value)
			}
			continue
		}
		// A MINIMUM inverts the probe. "At the bound is accepted, one past
		// it is rejected" is the whole comparison, and for a minimum "past
		// it" means one BELOW, not one above (round-18 fix B). Probing a
		// minimum in the maximum direction measures nothing: a minLength
		// of 1 obviously accepts length 2, which reads as "Go accepts
		// beyond the schema" and is simply the wrong question.
		//
		// A minimum of 0 constrains nothing and has no value below it to
		// reject, so it is not probeable at all and falls through to the
		// declarative checks rather than manufacturing a proof.
		leaf := bound.path[strings.LastIndex(bound.path, ".")+1:]
		minimumSide := strings.HasPrefix(leaf, "min")
		probedValue := bound.value + 1
		if minimumSide {
			probedValue = bound.value - 1
		}
		if probe, probeable := genericProbe(bound.path); probeable && !(minimumSide && bound.value == 0) {
			atBound := probe.apply(bound.value)
			pastBound := probe.apply(probedValue)
			switch {
			case errors.Is(atBound, errProbeUnreachable) || errors.Is(pastBound, errProbeUnreachable):
				// UNREACHABLE IS NOT A REJECTION. A probe that cannot build
				// the control document returns an error indistinguishable
				// from a validator rejection, so treating it as one would
				// score "I could not test this" as "Go rejects N+1" -- the
				// same false green this file exists to prevent, one level up.
				//
				// It is a real case: fact_requirements cannot be driven past
				// the fact-kind vocabulary, because every entry needs a
				// distinct kind. Fall through to the declarative comparison
				// rather than claiming a proof the probe did not make.
			case atBound == nil && pastBound != nil:
				proved++
				continue
			case atBound == nil && pastBound == nil:
				// Go accepts beyond the schema: the service can emit a
				// document violating its own contract.
				direction := "but Go accepts"
				if minimumSide {
					direction = "as a minimum but Go accepts"
				}
				t.Errorf("%s: schema says %d %s %d; the service can emit a document that violates its own contract",
					bound.path, bound.value, direction, probedValue)
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
	// The denominator is reported explicitly: "classified the rest" hid how
	// large the residual bucket actually is, and that bucket is where the
	// round-9 fact_requirements drift was sitting.
	// The other side of the exemption list: an entry matching no
	// discovered bound describes nothing, and would quietly keep excusing
	// a path that no longer exists.
	for path := range asymmetricBounds {
		if !exempted[path] {
			t.Errorf("asymmetricBounds lists %q, which matches no schema bound; remove it rather than leaving an exemption that describes nothing", path)
		}
	}
	t.Logf("%d schema bounds: %d proved behaviourally, %d compared declaratively, %d classified by pattern",
		len(discovered), proved, checked, len(discovered)-proved-checked)
}

// TestFactRequirementsBoundDerivesFromTheVocabulary pins the derivation
// itself (codex round-9 F1).
//
// fact_requirements is capped by the fact-kind vocabulary, because
// ContextFabricInterpretedQuestion.validate rejects a duplicate kind. That
// makes the count bound a CONSEQUENCE of the vocabulary, not an independent
// policy number -- so the published schema, the Go constant, and the enum
// must all move together. They did not: the schema said 50, Go said 64, and
// the vocabulary permitted 20.
//
// This is a declarative check by necessity. The behavioural prober cannot
// reach past the vocabulary to reject at N+1, and a probe that "proved" the
// bound by hitting the uniqueness rule instead would be measuring the wrong
// invariant -- which is exactly how the drift stayed green for nine rounds.
func TestFactRequirementsBoundDerivesFromTheVocabulary(t *testing.T) {
	documents := schemaDocuments(t)

	// The schema's own enum must be the vocabulary, in order.
	node := schemaNodeAt(t, documents, "common#$defs.FactRequirement.properties.kind")
	raw, ok := node["enum"].([]any)
	if !ok {
		t.Fatal("common#$defs.FactRequirement.properties.kind declares no enum")
	}
	vocabulary := ContextFabricFactKindVocabulary()
	published := make([]ContextFabricFactKind, 0, len(raw))
	for _, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("fact kind enum holds a non-string member %v", value)
		}
		published = append(published, ContextFabricFactKind(text))
	}
	if !slices.Equal(published, vocabulary[:]) {
		t.Errorf("the published fact-kind enum and the Go vocabulary disagree:\n  schema: %v\n  go:     %v", published, vocabulary)
	}

	// Every published kind must actually validate, and nothing else may.
	for _, kind := range published {
		if !validFactKind(kind) {
			t.Errorf("the schema publishes fact kind %q, which the validator rejects", kind)
		}
	}
	if validFactKind(ContextFabricFactKind("not_a_fact_kind")) {
		t.Error("validFactKind accepts a kind outside the closed vocabulary")
	}

	// And the count bound must be the vocabulary's size on both sides.
	if ContextFabricFactRequirementsMaxCount != ContextFabricFactKindCount {
		t.Errorf("ContextFabricFactRequirementsMaxCount is %d but the vocabulary holds %d kinds; a cap above the vocabulary can never be reached, and one below it silently forbids a legal interpretation",
			ContextFabricFactRequirementsMaxCount, ContextFabricFactKindCount)
	}
	bound := schemaNodeAt(t, documents, "common#$defs.InterpretedQuestion.properties.fact_requirements")
	value, ok := bound["maxItems"].(float64)
	if !ok {
		t.Fatal("fact_requirements declares no maxItems")
	}
	if int(value) != ContextFabricFactKindCount {
		t.Errorf("the schema caps fact_requirements at %d but only %d distinct kinds exist, so the contract promises a document the service always rejects",
			int(value), ContextFabricFactKindCount)
	}
}

type discoveredBound struct {
	path  string
	value int
}

// boundKeywords are the schema keywords that state a size the Go write path
// must agree with.
// "maximum" joins them for CHAOS-3746 round-17 finding 2. Enumerating
// only the collection and string keywords left every integer bound
// invisible: limitations_displaced shipped with a schema maximum of 250
// while the Go write path enforced 100, past a guard written precisely to
// catch that. A keyword this file does not know about is a bound nothing
// checks.
var boundKeywords = []string{"maxItems", "maxLength", "maxProperties", "maximum", "minItems", "minLength", "minimum"}

// schemaBounds enumerates every maxItems/maxLength in both canonical
// documents, as "<document>#<dotted path>.<keyword>".
func schemaBounds(t *testing.T, documents map[string]map[string]any) []discoveredBound {
	t.Helper()
	var found []discoveredBound
	var walk func(document string, node any, path string)
	walk = func(document string, node any, path string) {
		switch value := node.(type) {
		case map[string]any:
			// maxProperties counts too (codex round-9 F2). Enumerating only
			// maxItems/maxLength left every object-size bound invisible --
			// FactRequirement.parameters caps at 32 and nothing checked that
			// Go agreed, which is precisely the drift this file exists for.
			for _, keyword := range boundKeywords {
				if raw, ok := value[keyword].(float64); ok {
					found = append(found, discoveredBound{path: document + "#" + strings.TrimPrefix(path, ".") + "." + keyword, value: int(raw)})
				}
			}
			for key, child := range value {
				switch key {
				case "maxItems", "maxLength", "maxProperties", "maximum",
					"minItems", "minLength", "minimum",
					"description", "title", "$comment", "$schema", "$id":
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
	if !slices.Contains(boundKeywords, keyword) {
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
	// A field whose value is a CLOSED VOCABULARY cannot be driven to a
	// length at all: filler text of any size is rejected for not being a
	// member, so the probe would "reject" at both N and N+1 and measure
	// nothing (codex round-12). Type-checked, exactly like the fact
	// requirement case in uniquifyElement, so it cannot silently capture a
	// different shape's field that happens to be called "kind" -- Finding's
	// kind is the driver-category vocabulary, ClaimedFact's and
	// FactRequirement's are the fact-kind one.
	if rest == "" && value.Type() == reflect.TypeOf(ContextFabricFinding{}) && name == "kind" {
		return false
	}
	switch {
	case isElement && field.Kind() == reflect.Slice:
		if field.Len() == 0 {
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		}
		return driveScalar(field.Index(0), size)
	case field.Kind() == reflect.Map && keyword == "maxProperties":
		// Object-size bounds are driven by filling the map with distinct
		// keys (codex round-9 F2). Keys and values stay short so the probe
		// measures the PROPERTY COUNT and not a key/value length bound that
		// happens to sit on the same object.
		grown := reflect.MakeMapWithSize(field.Type(), size)
		for i := 0; i < size; i++ {
			key := reflect.ValueOf("p" + strconv.Itoa(i))
			value := reflect.ValueOf("probevalue")
			if !key.Type().AssignableTo(field.Type().Key()) || !value.Type().AssignableTo(field.Type().Elem()) {
				return false
			}
			grown.SetMapIndex(key, value)
		}
		field.Set(grown)
		return true
	case field.Kind() == reflect.Slice && keyword == "maxItems":
		grown := reflect.MakeSlice(field.Type(), size, size)
		for i := 0; i < size; i++ {
			if field.Len() > 0 {
				grown.Index(i).Set(field.Index(0))
			}
			if !uniquifyElement(grown.Index(i), i) {
				// The probe cannot build a valid element at this index, so
				// it cannot make a control document at this size. Reported
				// as unreachable, never as a rejection.
				return false
			}
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
// probe is rejected for LENGTH rather than for duplication. It reports
// whether a distinct element could be built at this index.
func uniquifyElement(value reflect.Value, index int) bool {
	suffix := strconv.Itoa(index)
	switch value.Kind() {
	case reflect.String:
		value.SetString("probevalue" + suffix)
	case reflect.Struct:
		// A fact requirement is made distinct by its KIND, not by an
		// identifier -- it has none (codex round-9 F1). Duplicating the same
		// requirement made the document fail the kind-uniqueness invariant at
		// BOTH N and N+1, so the length bound was never exercised and the
		// 50-vs-64 schema/Go drift was concealed behind a green probe.
		if value.Type() == reflect.TypeOf(ContextFabricFactRequirement{}) {
			if index >= ContextFabricFactKindCount {
				// Past the vocabulary there is no distinct kind left, so no
				// valid document of this size exists at all.
				return false
			}
			if field := fieldByJSONTag(value, "kind"); field.IsValid() && field.CanSet() {
				field.Set(reflect.ValueOf(ContextFabricFactKindVocabulary()[index]))
				return true
			}
			return false
		}
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
	return true
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

// TestFactKindVocabularyCannotBeMutatedByCallers closes codex round-10 F2.
//
// The vocabulary was an exported array VAR. An array var's elements are
// assignable, so any importing package could write one -- and the two
// consumers read it differently: validFactKind consults it live on every
// call, while the interpretation prompt renders it once at init. A single
// in-process write therefore desynchronized them, leaving the validator
// accepting a kind the prompt never advertised and the published schema does
// not contain. Demonstrated before the fix: assigning to element 0 made
// ContextFabricInterpretedQuestion.Validate accept "forged_kind" while the
// rendered prompt still listed "identity".
//
// The backing array is now unexported and reached only through
// ContextFabricFactKindVocabulary, which returns an ARRAY -- copied on
// return. The absence of a writable path is a COMPILE-TIME property, not a
// runtime one, so it cannot be red-tested from outside this package: the
// pre-fix expression `contractsv1.ContextFabricFactKinds[0] = x` no longer
// compiles because the identifier does not exist, and no exported symbol
// yields an alias to the backing array. What this test can and does check is
// the other half of that guarantee -- that the accessor hands back a copy
// rather than a window onto the declaration.
func TestFactKindVocabularyCannotBeMutatedByCallers(t *testing.T) {
	const forged = ContextFabricFactKind("forged_kind")

	vocabulary := ContextFabricFactKindVocabulary()
	if len(vocabulary) == 0 {
		t.Fatal("the vocabulary is empty")
	}
	original := vocabulary[0]

	// Write to the returned value the way a caller ranging over it might.
	vocabulary[0] = forged

	if validFactKind(forged) {
		t.Error("mutating the value returned by ContextFabricFactKindVocabulary changed what the validator accepts; the accessor is handing out an alias, not a copy")
	}
	if !validFactKind(original) {
		t.Errorf("mutating the returned value removed %q from the accepted set; the accessor is handing out an alias, not a copy", original)
	}
	if fresh := ContextFabricFactKindVocabulary(); fresh[0] != original {
		t.Errorf("a second call returned the mutated vocabulary (%q); the copy is not fresh per call", fresh[0])
	}

	// And the derived count stays tied to the declaration.
	if ContextFabricFactKindCount != len(ContextFabricFactKindVocabulary()) {
		t.Errorf("ContextFabricFactKindCount is %d but the vocabulary holds %d", ContextFabricFactKindCount, len(ContextFabricFactKindVocabulary()))
	}
}

// TestWindowShapeSchemaAndGoValidateAgree closes the CHAOS-3900 W1
// window-SHAPE class TestSchemaAndGoBoundsAgree above does not reach: that
// test enumerates numeric maxItems/maxLength/etc keywords, but the window
// defect class codex round 6 opened and round 7 reopened twice more is
// STRUCTURAL -- an anyOf/if/then combination that is laxer than
// validate_context_fabric_window.go's own Validate()/validate() methods.
// Two hand-audit passes (round 6's own fix, then round 7's re-review of
// that fix) each missed at least one instance of the same shape, which is
// exactly the pattern the house rule ("after the second boundary defect you
// enforce the invariant, not the instance") exists for: this table is that
// invariant. A future schema edit that reopens ANY of these gaps fails this
// test instead of waiting for a third codex round to notice.
//
// Every case drives BOTH sides independently -- the Go method the write
// path actually calls, and contractcheck.ValidateSerialized against the
// canonical schema wrapping the shape in an otherwise-valid document -- and
// asserts each against the test's own stated expectation, not merely
// against each other. Asserting only mutual agreement would pass if both
// sides regressed together in the same direction; asserting each against a
// known-correct expectation is what actually catches that.
func TestWindowShapeSchemaAndGoValidateAgree(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	assertParity := func(t *testing.T, wantValid bool, goErr, schemaErr error) {
		t.Helper()
		if goValid := goErr == nil; goValid != wantValid {
			t.Errorf("Go verdict: valid=%v (err=%v), want valid=%v", goValid, goErr, wantValid)
		}
		if schemaValid := schemaErr == nil; schemaValid != wantValid {
			t.Errorf("schema verdict: valid=%v (err=%v), want valid=%v", schemaValid, schemaErr, wantValid)
		}
	}

	// --- RequestedEvidenceWindow, via request.time_context.evidence_window ---
	requestedCases := []struct {
		name      string
		window    ContextFabricRequestedEvidenceWindow
		wantValid bool
	}{
		{"relative_id + start, no end (codex round 7 F1)", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D, Start: &start}, false},
		{"relative_id + end, no start (codex round 7 F1)", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D, End: &end}, false},
		{"relative_id alone", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowTrailing90D}, true},
		{"explicit bounds alone, no relative_id", ContextFabricRequestedEvidenceWindow{Start: &start, End: &end}, true},
		{"all_time alone", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime}, true},
		{"all_time with bounds", ContextFabricRequestedEvidenceWindow{RelativeID: ContextFabricRelativeWindowAllTime, Start: &start, End: &end}, false},
	}
	for _, tc := range requestedCases {
		t.Run("RequestedEvidenceWindow/"+tc.name, func(t *testing.T) {
			goErr := tc.window.validate()
			request := validContextFabricContractRequest()
			request.TimeContext.EvidenceWindow = &tc.window
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_request.v1.schema.json", encoded)
			assertParity(t, tc.wantValid, goErr, schemaErr)
		})
	}

	// --- EffectiveEvidenceWindow, via result.effective_evidence_window ---
	effectiveCases := []struct {
		name      string
		window    ContextFabricEffectiveEvidenceWindow
		wantValid bool
	}{
		{"relative_id + start, no end (codex round 7 F1)", ContextFabricEffectiveEvidenceWindow{Provenance: ContextFabricWindowInferredDefault, RelativeID: ContextFabricRelativeWindowTrailing30D, Start: &start}, false},
		{"relative_id + end, no start (codex round 7 F1)", ContextFabricEffectiveEvidenceWindow{Provenance: ContextFabricWindowInferredDefault, RelativeID: ContextFabricRelativeWindowTrailing30D, End: &end}, false},
		{"relative_id alone", ContextFabricEffectiveEvidenceWindow{Provenance: ContextFabricWindowInferredDefault, RelativeID: ContextFabricRelativeWindowTrailing30D}, true},
	}
	for _, tc := range effectiveCases {
		t.Run("EffectiveEvidenceWindow/"+tc.name, func(t *testing.T) {
			goErr := tc.window.validate()
			result := validContextFabricContractResult()
			result.EffectiveEvidenceWindow = &tc.window
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			assertParity(t, tc.wantValid, goErr, schemaErr)
		})
	}

	// --- WindowOption, via result.window_clarification.options[0] ---
	baseOption := ContextFabricWindowOption{ReceiptID: "winr_confirm00001", OptionID: "opt_1", Label: "a window option"}
	optionCases := []struct {
		name      string
		option    ContextFabricWindowOption
		wantValid bool
	}{
		{"neither relative_id nor bounds (codex round 7 F2)", baseOption, false},
		{"all_time with bounds (codex round 7 F2)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowAllTime
			o.Start, o.End = &start, &end
		}), false},
		{"relative_id + start, no end (codex round 7 F1/F2)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowTrailing30D
			o.Start = &start
		}), false},
		{"relative_id + end, no start (codex round 7 F1/F2)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowTrailing30D
			o.End = &end
		}), false},
		{"relative_id + both bounds (valid control)", withOption(baseOption, func(o *ContextFabricWindowOption) {
			o.RelativeID = ContextFabricRelativeWindowTrailing30D
			o.Start, o.End = &start, &end
		}), true},
		{"all_time alone (valid control)", withOption(baseOption, func(o *ContextFabricWindowOption) { o.RelativeID = ContextFabricRelativeWindowAllTime }), true},
		{"no relative_id, both bounds (valid control)", withOption(baseOption, func(o *ContextFabricWindowOption) { o.Start, o.End = &start, &end }), true},
	}
	for _, tc := range optionCases {
		t.Run("WindowOption/"+tc.name, func(t *testing.T) {
			goErr := tc.option.Validate()
			result := validContextFabricContractResult()
			result.WindowClarification = &ContextFabricWindowClarification{Options: []ContextFabricWindowOption{tc.option}}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			assertParity(t, tc.wantValid, goErr, schemaErr)
		})
	}

	// --- WindowClarification per-field uniqueness: the DOCUMENTED RESIDUAL
	// asymmetry (codex round 7 F3/F5), not a closed gap. Standard JSON
	// Schema (draft 2020-12) has no keyword for uniqueness on a derived
	// subset of fields, so uniqueItems (whole-object equality) cannot
	// reject two options that differ only in label -- see WindowClarification's
	// own schema description for the full explanation. This is pinned as an
	// EXPECTED disagreement, not silently left untested: if a future
	// schema-validator upgrade ever makes this expressible, tightening the
	// schema and flipping wantSchemaValid to false here (matching every
	// other case's shared expectation) is the signal to do it.
	uniquenessCases := []struct {
		name            string
		options         []ContextFabricWindowOption
		wantGoValid     bool
		wantSchemaValid bool
	}{
		{
			name: "duplicate receipt_id, differing label",
			options: []ContextFabricWindowOption{
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.RelativeID = ContextFabricRelativeWindowTrailing30D
					o.Start, o.End = &start, &end
				}),
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.OptionID, o.Label = "opt_2", "a differently-labeled window option"
					o.RelativeID = ContextFabricRelativeWindowTrailing90D
					o.Start, o.End = &start, &end
				}),
			},
			wantGoValid:     false,
			wantSchemaValid: true,
		},
		{
			name: "duplicate option_id, differing label",
			options: []ContextFabricWindowOption{
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.RelativeID = ContextFabricRelativeWindowTrailing30D
					o.Start, o.End = &start, &end
				}),
				withOption(baseOption, func(o *ContextFabricWindowOption) {
					o.ReceiptID, o.Label = "winr_confirm00002", "a differently-labeled window option"
					o.RelativeID = ContextFabricRelativeWindowTrailing90D
					o.Start, o.End = &start, &end
				}),
			},
			wantGoValid:     false,
			wantSchemaValid: true,
		},
	}
	for _, tc := range uniquenessCases {
		t.Run("WindowClarification/"+tc.name, func(t *testing.T) {
			clarification := ContextFabricWindowClarification{Options: tc.options}
			goErr := clarification.Validate()
			if goValid := goErr == nil; goValid != tc.wantGoValid {
				t.Errorf("Go verdict: valid=%v (err=%v), want valid=%v", goValid, goErr, tc.wantGoValid)
			}
			result := validContextFabricContractResult()
			result.WindowClarification = &clarification
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			if schemaValid := schemaErr == nil; schemaValid != tc.wantSchemaValid {
				t.Errorf("schema verdict: valid=%v (err=%v), want valid=%v -- if this now differs, standard JSON Schema may have gained a way to express per-field uniqueness; see this test's own doc comment", schemaValid, schemaErr, tc.wantSchemaValid)
			}
		})
	}
}

// withOption returns a copy of base with mutate applied, so table entries
// above can start from one shared, already-valid receipt_id/option_id/label
// triple and vary only the window shape under test.
func withOption(base ContextFabricWindowOption, mutate func(*ContextFabricWindowOption)) ContextFabricWindowOption {
	mutate(&base)
	return base
}
