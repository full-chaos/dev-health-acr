package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// TestNormalizeForStrictSchema_InterpretSchemaIsFullyClosed is the
// team-lead-directed unit test: normalizeForStrictSchema, applied to the
// REAL `interpret` operation schema (genkitruntime.InterpretationOutputSchema,
// the exact schema that measured only 3/13 first-try conformance under
// non-strict mode on the case-57 kiac run), must produce a tree satisfying
// every one of OpenAI's strict json_schema structural requirements. Walks
// the WHOLE normalized tree recursively rather than asserting a few
// hand-picked paths, so a future schema change (a new field, a new nested
// object) is covered automatically instead of silently unchecked.
func TestNormalizeForStrictSchema_InterpretSchemaIsFullyClosed(t *testing.T) {
	raw, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		t.Fatalf("InterpretationOutputSchema: %v", err)
	}
	var schemaAny any
	if err := json.Unmarshal(raw, &schemaAny); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	normalized, notes := normalizeForStrictSchema(schemaAny)
	if len(notes) == 0 {
		t.Fatal("normalizeForStrictSchema reported zero rewrites for a schema known to need them (interpret has optional fields, an if/then/else time_context, and format:date-time) -- the transform silently did nothing")
	}
	t.Logf("%d rewrite note(s):", len(notes))
	for _, n := range notes {
		t.Log(" ", n)
	}

	assertFullyClosedStrictSchema(t, normalized, "$")

	// The ORIGINAL, un-normalized schema must be untouched -- answerOne's
	// own post-response validation depends on it still carrying the real
	// semantic constraints (the if/then conditional, the optional-vs-
	// required distinction) that normalization deliberately drops for the
	// API call itself.
	if _, hasAllOf := findKeyAnywhere(schemaAny, "allOf"); !hasAllOf {
		t.Fatal("test setup invariant broken: the original schema no longer contains allOf -- normalizeForStrictSchema must not mutate its input")
	}
}

// TestNormalizeForStrictSchema_OpenDictionaryIsClosed proves the specific
// "dictionary object" case (additionalProperties holding a SCHEMA, as
// genkitruntime's fact_requirements[].parameters field does -- a
// map[string]string in the underlying Go type) closes to
// additionalProperties:false with a note, rather than being left open
// (which strict mode would reject) or silently dropped without record.
func TestNormalizeForStrictSchema_OpenDictionaryIsClosed(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
		"required": []any{},
	}
	normalized, notes := normalizeForStrictSchema(schema)
	assertFullyClosedStrictSchema(t, normalized, "$")

	found := false
	for _, n := range notes {
		if strings.Contains(n, "open dictionary") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no note recorded the open-dictionary->closed rewrite; notes: %v", notes)
	}

	m := normalized.(map[string]any)
	props := m["properties"].(map[string]any)
	tags := props["tags"]
	tagsMap, ok := tags.(map[string]any)
	if ok {
		if ap, _ := tagsMap["additionalProperties"].(bool); ap {
			t.Fatalf("tags.additionalProperties = true, want false (was made nullable, so this is inside an anyOf -- check the wrapped variant instead)")
		}
	}
}

// TestNormalizeForStrictSchema_OptionalEnumIsNullableViaAnyOf proves an
// optional (not in "required") enum-carrying property is represented as
// anyOf[original-enum-schema, {"type":"null"}] -- not silently made
// required-non-null (which would force the model to always populate it)
// nor left out of "required" (which strict mode rejects outright).
func TestNormalizeForStrictSchema_OptionalEnumIsNullableViaAnyOf(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"window_confidence": map[string]any{
				"type": "string",
				"enum": []any{"high", "low"},
			},
		},
		"required": []any{},
	}
	normalized, _ := normalizeForStrictSchema(schema)
	assertFullyClosedStrictSchema(t, normalized, "$")

	m := normalized.(map[string]any)
	required, _ := m["required"].([]string)
	if len(required) != 1 || required[0] != "window_confidence" {
		t.Fatalf("required = %v, want exactly [\"window_confidence\"] (every property must be required under strict mode)", required)
	}
	props := m["properties"].(map[string]any)
	prop, ok := props["window_confidence"].(map[string]any)
	if !ok {
		t.Fatalf("properties.window_confidence is not an object: %#v", props["window_confidence"])
	}
	anyOf, ok := prop["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("window_confidence = %#v, want anyOf[enum-schema, null-schema]", prop)
	}
}

// assertFullyClosedStrictSchema recursively walks node and fails the test
// at the FIRST OpenAI-strict-mode structural violation found, naming the
// exact json-pointer-style path (schema structure only, never request/
// response content -- safe to include in a test failure message).
func assertFullyClosedStrictSchema(t *testing.T, node any, path string) {
	t.Helper()
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	for _, banned := range []string{"if", "then", "else", "allOf", "format", "pattern", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"} {
		if _, present := m[banned]; present {
			t.Fatalf("%s: contains unsupported strict-mode keyword %q", path, banned)
		}
	}
	_, hasProperties := m["properties"]
	typ, _ := m["type"].(string)
	if typ == "object" || hasProperties {
		props, _ := m["properties"].(map[string]any)
		ap, hasAP := m["additionalProperties"]
		if !hasAP {
			t.Fatalf("%s: object has no additionalProperties key, want false", path)
		}
		if apBool, ok := ap.(bool); !ok || apBool {
			t.Fatalf("%s: additionalProperties = %#v, want false", path, ap)
		}
		required, _ := m["required"].([]string)
		requiredSet := map[string]bool{}
		for _, r := range required {
			requiredSet[r] = true
		}
		if len(required) != len(props) {
			t.Fatalf("%s: required has %d entries but properties has %d -- every property must be required under strict mode", path, len(required), len(props))
		}
		for name := range props {
			if !requiredSet[name] {
				t.Fatalf("%s: property %q is not in required", path, name)
			}
		}
		for name, propNode := range props {
			assertFullyClosedStrictSchema(t, propNode, path+".properties."+name)
		}
	}
	if typ == "array" {
		if items, ok := m["items"]; ok {
			assertFullyClosedStrictSchema(t, items, path+".items")
		}
	}
	if anyOf, ok := m["anyOf"].([]any); ok {
		for i, v := range anyOf {
			assertFullyClosedStrictSchema(t, v, path+".anyOf["+strconv.Itoa(i)+"]")
		}
	}
}

// findKeyAnywhere reports whether key appears anywhere in node's tree, and
// returns the first value found at it.
func findKeyAnywhere(node any, key string) (any, bool) {
	m, ok := node.(map[string]any)
	if !ok {
		if arr, ok := node.([]any); ok {
			for _, v := range arr {
				if found, ok := findKeyAnywhere(v, key); ok {
					return found, true
				}
			}
		}
		return nil, false
	}
	if v, ok := m[key]; ok {
		return v, true
	}
	for _, v := range m {
		if found, ok := findKeyAnywhere(v, key); ok {
			return found, true
		}
	}
	return nil, false
}

// TestNormalizeForStrictSchema_TimeContextConditionalIsLossless is the
// team-lead-directed equivalence test: "the normalized interpret schema
// accepts exactly the samples the original accepts (both branches of
// time_context) and rejects the cross-branch mix." Extracts time_context's
// own schema node (both original and compiled-anyOf-normalized forms) from
// the REAL interpret schema and, for every case, proves THREE things agree
// with the case's own "want" verdict:
//
//  1. omittedForm (fields the original schema treats as inapplicable are
//     OMITTED, the shape the original if/then was actually written
//     against) validated against the ORIGINAL schema.
//  2. nullForm (the SAME logical case, but inapplicable fields are
//     explicit JSON null -- the ONLY way strict mode's compiled anyOf can
//     express "this branch excludes this field") validated against the
//     NORMALIZED schema -- proves compileConditionalToAnyOf is a faithful
//     structural translation, not an approximation.
//  3. nullForm with null fields stripped (stripNullFieldsJSON, the exact
//     transform answerOne applies before its own post-response validation)
//     validated against the ORIGINAL schema -- proves the full pipeline a
//     real strict-mode response actually goes through reconstructs the
//     SAME verdict the original schema would have given the omitted form.
func TestNormalizeForStrictSchema_TimeContextConditionalIsLossless(t *testing.T) {
	raw, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		t.Fatalf("InterpretationOutputSchema: %v", err)
	}
	var schemaAny any
	if err := json.Unmarshal(raw, &schemaAny); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	fullSchema := schemaAny.(map[string]any)
	originalTimeContext := fullSchema["properties"].(map[string]any)["time_context"]

	normalizedFull, _ := normalizeForStrictSchema(schemaAny)
	normalizedTimeContext := normalizedFull.(map[string]any)["properties"].(map[string]any)["time_context"]
	if _, hasAnyOf := normalizedTimeContext.(map[string]any)["anyOf"]; !hasAnyOf {
		t.Fatalf("test setup invariant broken: normalized time_context has no anyOf -- compileConditionalToAnyOf did not fire (check the schema still matches the discriminated-if/then shape this compiler recognizes)")
	}

	originalLoader := gojsonschema.NewGoLoader(originalTimeContext)
	normalizedLoader := gojsonschema.NewGoLoader(normalizedTimeContext)

	cases := []struct {
		name        string
		omittedForm map[string]any // what the ORIGINAL schema was written against
		nullForm    map[string]any // what strict mode's compiled anyOf actually produces
		want        bool
	}{
		{
			name:        "current: no as_of/start/end",
			omittedForm: map[string]any{"axis": "current"},
			nullForm:    map[string]any{"axis": "current", "as_of": nil, "start": nil, "end": nil},
			want:        true,
		},
		{
			name:        "valid_time: as_of present",
			omittedForm: map[string]any{"axis": "valid_time", "as_of": "2024-01-01T00:00:00Z"},
			nullForm:    map[string]any{"axis": "valid_time", "as_of": "2024-01-01T00:00:00Z", "start": nil, "end": nil},
			want:        true,
		},
		{
			name:        "observed_time: as_of present (shares valid_time's branch)",
			omittedForm: map[string]any{"axis": "observed_time", "as_of": "2024-01-01T00:00:00Z"},
			nullForm:    map[string]any{"axis": "observed_time", "as_of": "2024-01-01T00:00:00Z", "start": nil, "end": nil},
			want:        true,
		},
		{
			name:        "range: start and end present",
			omittedForm: map[string]any{"axis": "range", "start": "2024-01-01T00:00:00Z", "end": "2024-02-01T00:00:00Z"},
			nullForm:    map[string]any{"axis": "range", "as_of": nil, "start": "2024-01-01T00:00:00Z", "end": "2024-02-01T00:00:00Z"},
			want:        true,
		},
		{
			name:        "cross-branch mix: current with as_of set",
			omittedForm: map[string]any{"axis": "current", "as_of": "2024-01-01T00:00:00Z"},
			nullForm:    map[string]any{"axis": "current", "as_of": "2024-01-01T00:00:00Z", "start": nil, "end": nil},
			want:        false,
		},
		{
			name:        "cross-branch mix: range missing end",
			omittedForm: map[string]any{"axis": "range", "start": "2024-01-01T00:00:00Z"},
			nullForm:    map[string]any{"axis": "range", "as_of": nil, "start": "2024-01-01T00:00:00Z", "end": nil},
			want:        false,
		},
		{
			name:        "cross-branch mix: valid_time with start also set",
			omittedForm: map[string]any{"axis": "valid_time", "as_of": "2024-01-01T00:00:00Z", "start": "2024-01-01T00:00:00Z"},
			nullForm:    map[string]any{"axis": "valid_time", "as_of": "2024-01-01T00:00:00Z", "start": "2024-01-01T00:00:00Z", "end": nil},
			want:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			omittedResult, err := gojsonschema.Validate(originalLoader, gojsonschema.NewGoLoader(tc.omittedForm))
			if err != nil {
				t.Fatalf("validate omittedForm against original: %v", err)
			}
			if omittedResult.Valid() != tc.want {
				t.Fatalf("test setup invariant broken: omittedForm valid=%v against the ORIGINAL schema, want %v -- the case itself does not match the original schema's own judgment", omittedResult.Valid(), tc.want)
			}

			normalizedResult, err := gojsonschema.Validate(normalizedLoader, gojsonschema.NewGoLoader(tc.nullForm))
			if err != nil {
				t.Fatalf("validate nullForm against normalized: %v", err)
			}
			if normalizedResult.Valid() != tc.want {
				t.Errorf("nullForm valid=%v against the COMPILED anyOf schema, want %v (original's own verdict) -- compileConditionalToAnyOf is not a faithful translation for this case", normalizedResult.Valid(), tc.want)
			}

			nullFormJSON, err := json.Marshal(tc.nullForm)
			if err != nil {
				t.Fatalf("marshal nullForm: %v", err)
			}
			stripped, err := stripNullFieldsJSON(nullFormJSON)
			if err != nil {
				t.Fatalf("stripNullFieldsJSON: %v", err)
			}
			roundTripResult, err := gojsonschema.Validate(originalLoader, gojsonschema.NewBytesLoader(stripped))
			if err != nil {
				t.Fatalf("validate stripped nullForm against original: %v", err)
			}
			if roundTripResult.Valid() != tc.want {
				t.Errorf("null-stripped nullForm valid=%v against the ORIGINAL schema, want %v -- the answerOne post-validation pipeline (strip nulls, validate against original) does not reconstruct the original verdict for this case", roundTripResult.Valid(), tc.want)
			}
		})
	}
}
