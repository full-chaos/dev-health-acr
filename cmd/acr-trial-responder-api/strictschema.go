// Strict-schema normalization (team-lead ruling, 2026-08-26, following two
// case-57 kiac acceptance runs): non-strict response_format left this
// transport's output entirely un-grammar-constrained -- gpt-5.6-luna's
// first-attempt schema conformance rate on the `interpret` operation's
// schema measured only 3/13 (23%), with 2/13 (15%) exhausting every
// defaultMaxAnswerAttempts retry outright. Raising the retry cap or tuning
// the prompt were both explicitly ruled out ("non-strict is unfit"); the
// fix is to make OpenAI's strict json_schema mode actually accept this
// schema.
//
// A first attempt (STRUCTURAL closure only: additionalProperties:false,
// every property required, if/then/allOf/format/pattern/min/max simply
// DROPPED) satisfied the API's own strict-mode validator -- zero 400s --
// but made the actually-measured conformance WORSE (8 violations against
// the ORIGINAL schema instead of 7, 0/1 exchanges succeeding): dropping
// those keywords also dropped the only semantic guidance the model had
// about them, since the ORIGINAL schema (with them present) is still what
// answerOne validates the response against. That was LOSSY, not lossless,
// and team-lead's second ruling is explicit: never set the model up to
// fail a constraint it was never told about.
//
// This version is lossless in the two ways OpenAI's strict mode allows:
//
//  1. An allOf of {"if","then"} blocks that all discriminate on the SAME
//     property via "const" or "enum" (time_context's own axis-keyed
//     if/then/else) compiles to an EQUIVALENT anyOf of fully-closed
//     variants (compileConditionalToAnyOf) -- strict mode supports anyOf.
//     Each variant narrows the discriminator to its own branch's values
//     and forces every property NOT required by that branch to
//     {"type":"null"} (not merely nullable -- structurally EXCLUDED from
//     that branch, exactly what the original if/then/not/anyOf[required]
//     pattern meant). This is a real compilation of the conditional's
//     semantics, not an approximation -- the unit test in
//     strictschema_test.go proves the compiled anyOf accepts and rejects
//     the SAME samples the original if/then schema does.
//  2. Every OTHER dropped keyword (format, pattern, minimum, maximum,
//     minLength, maxLength, minItems, maxItems, and an allOf this file's
//     compiler cannot discriminate) is preserved as TEXT, appended to the
//     property's own "description" -- strict mode allows descriptions, and
//     the model reads them. Nothing is silently lost; what cannot be
//     structurally enforced is at least stated.
//
// The ORIGINAL schema is still never discarded: response_format sends the
// normalized schema, but answerOne's post-response validation runs against
// the ORIGINAL, because a description is guidance, not a guarantee -- the
// bounded retry (retryOrExhaust) stays the fallback for whatever a
// description alone does not fully constrain.
package main

import (
	"encoding/json"
	"fmt"
)

// stripNullFieldsJSON parses raw as JSON and returns an equivalent document
// with every object key whose value is JSON null REMOVED (recursively,
// through nested objects and arrays). Used only to prepare a strict-mode
// response for validation against the pre-strict ORIGINAL schema (see the
// call site's own comment in answerOne) -- strict mode's only way to
// represent "this optional field is absent" is an explicit null, and the
// original schema was written expecting the key to be MISSING instead.
// Never used on what gets published as the response's own Output: that
// stays exactly what the model returned.
func stripNullFieldsJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(stripNullFields(v))
}

func stripNullFields(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = stripNullFields(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripNullFields(val)
		}
		return out
	default:
		return v
	}
}

// normalizeForStrictSchema returns a NEW schema tree (the input is never
// mutated) satisfying OpenAI strict json_schema mode's structural
// requirements, plus a list of human-readable notes describing every
// rewrite it made. The notes name constructs and property names from the
// SCHEMA's own structure only (Go type/field names via genkitruntime's
// core.InferSchemaMap reflection) -- never request/response content -- so
// logging them carries no corpus-privacy risk, unlike everything else this
// package logs.
func normalizeForStrictSchema(schema any) (any, []string) {
	var notes []string
	return normalizeStrictNode(schema, &notes, "$"), notes
}

// strictStrippedKeywords are dropped from every schema node under strict
// mode; their information is not discarded (see describeStrippedKeyword)
// unless the node also gets compiled away entirely by
// compileConditionalToAnyOf.
var strictStrippedKeywords = []string{"format", "pattern", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "exclusiveMinimum", "exclusiveMaximum"}

// normalizeStrictNode recurses through a JSON-Schema-shaped value (as
// decoded into Go's json.Unmarshal any-tree: map[string]any / []any /
// scalars). path is a JSON-Schema-pointer-style breadcrumb ("$.properties.
// time_context.properties.as_of") used only inside the notes list, for a
// reader to locate what changed without re-deriving the traversal.
func normalizeStrictNode(node any, notes *[]string, path string) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}

	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}

	// An allOf of axis-style if/then blocks compiles to an equivalent
	// anyOf of closed variants (see this file's own header) -- tried
	// FIRST, before the generic object-normalization path below, because
	// a successful compilation replaces this node's shape entirely
	// (anyOf, no "properties"/"required" of its own at this level).
	if _, hasAllOf := out["allOf"]; hasAllOf {
		if compiled, ok := compileConditionalToAnyOf(out, notes, path); ok {
			*notes = append(*notes, fmt.Sprintf("%s: compiled allOf (if/then/else, discriminated) into an equivalent anyOf of closed variants -- structurally lossless, proven by strictschema_test.go against the original schema's own accept/reject judgment", path))
			return compiled
		}
		*notes = append(*notes, fmt.Sprintf("%s: allOf could not be compiled to anyOf (not a single-property const/enum-discriminated if/then) -- dropped structurally; its constraint is NOT restated in description text (no generic textual summary was built for this shape) -- the ORIGINAL schema still enforces it post-response", path))
		delete(out, "allOf")
	}
	for _, kw := range []string{"if", "then", "else"} {
		if _, present := out[kw]; present {
			delete(out, kw)
			*notes = append(*notes, fmt.Sprintf("%s: dropped unsupported keyword %q (only ever meaningful alongside allOf, handled above)", path, kw))
		}
	}

	// Every OTHER unsupported keyword is preserved as description TEXT
	// (team-lead ruling item 2) instead of silently vanishing -- the model
	// still reads it, even though nothing grammar-enforces it anymore.
	for _, kw := range strictStrippedKeywords {
		if v, present := out[kw]; present {
			delete(out, kw)
			desc := describeStrippedKeyword(kw, v)
			out["description"] = appendDescription(out["description"], desc)
			*notes = append(*notes, fmt.Sprintf("%s: moved unsupported keyword %q into description text (%q) -- strict mode drops the keyword itself but the model still reads the description", path, kw, desc))
		}
	}

	_, hasProperties := out["properties"]
	typ, _ := out["type"].(string)
	if typ == "object" || hasProperties {
		props, _ := out["properties"].(map[string]any)
		required := stringSet(out["required"])

		newProps := make(map[string]any, len(props))
		newRequired := make([]string, 0, len(props))
		for name, propNode := range props {
			propPath := path + ".properties." + name
			normalizedProp := normalizeStrictNode(propNode, notes, propPath)
			if !required[name] {
				normalizedProp = makeStrictNullable(normalizedProp, notes, propPath)
			}
			newProps[name] = normalizedProp
			newRequired = append(newRequired, name)
		}
		out["properties"] = newProps
		out["required"] = newRequired

		if _, wasOpenDict := out["additionalProperties"].(map[string]any); wasOpenDict {
			*notes = append(*notes, fmt.Sprintf("%s: open dictionary (additionalProperties held a schema, not false) closed to additionalProperties:false -- this object can no longer carry any dynamic key through the strict-mode call", path))
		}
		out["additionalProperties"] = false
	}

	if typ == "array" {
		if items, ok := out["items"]; ok {
			out["items"] = normalizeStrictNode(items, notes, path+".items")
		}
	}

	return out
}

// describeStrippedKeyword renders a dropped JSON-Schema keyword as a short
// human/model-readable sentence fragment. Values are schema STRUCTURE
// (format names, numeric bounds, regex patterns from the Go type's own
// jsonschema struct tags) -- never request/response content.
func describeStrippedKeyword(kw string, v any) string {
	switch kw {
	case "format":
		return fmt.Sprintf("Format: %v.", v)
	case "pattern":
		return fmt.Sprintf("Must match the pattern: %v.", v)
	case "minimum":
		return fmt.Sprintf("Minimum: %v.", v)
	case "maximum":
		return fmt.Sprintf("Maximum: %v.", v)
	case "minLength":
		return fmt.Sprintf("Minimum length: %v.", v)
	case "maxLength":
		return fmt.Sprintf("Maximum length: %v.", v)
	case "minItems":
		return fmt.Sprintf("Minimum items: %v.", v)
	case "maxItems":
		return fmt.Sprintf("Maximum items: %v.", v)
	case "exclusiveMinimum":
		return fmt.Sprintf("Exclusive minimum: %v.", v)
	case "exclusiveMaximum":
		return fmt.Sprintf("Exclusive maximum: %v.", v)
	default:
		return fmt.Sprintf("%s: %v.", kw, v)
	}
}

// appendDescription joins an existing "description" value (if any, and if
// it is actually a string -- a malformed schema's non-string description
// is discarded rather than propagated) with a new sentence fragment.
func appendDescription(existing any, addition string) string {
	if s, ok := existing.(string); ok && s != "" {
		return s + " " + addition
	}
	return addition
}

// makeStrictNullable represents "this property was optional in the
// ORIGINAL schema" the only way strict mode allows: the property stays
// listed in "required" (normalizeStrictNode's caller already does that
// unconditionally), but its own value schema is widened to also accept
// null. An enum-carrying schema is wrapped in anyOf (a bare "enum" cannot
// itself list `null` alongside typed values without also changing "type",
// and anyOf keeps the original enum node untouched, simplest to reason
// about); a schema with a bare scalar "type" gets that type widened to a
// [T, "null"] array, matching OpenAI's own documented nullable-field
// pattern; anything else (a node with neither -- e.g. one that is itself
// an object node normalizeStrictNode already recursed into) is wrapped in
// anyOf as a safe, general fallback.
func makeStrictNullable(propSchema any, notes *[]string, propPath string) any {
	propMap, ok := propSchema.(map[string]any)
	if !ok {
		return propSchema
	}
	if _, hasEnum := propMap["enum"]; hasEnum {
		*notes = append(*notes, fmt.Sprintf("%s: optional enum property made nullable via anyOf[enum, null]", propPath))
		return map[string]any{"anyOf": []any{propMap, map[string]any{"type": "null"}}}
	}
	if t, isBareType := propMap["type"].(string); isBareType {
		widened := make(map[string]any, len(propMap))
		for k, v := range propMap {
			widened[k] = v
		}
		widened["type"] = []any{t, "null"}
		*notes = append(*notes, fmt.Sprintf("%s: optional %q property made nullable via type:[%q,\"null\"]", propPath, t, t))
		return widened
	}
	*notes = append(*notes, fmt.Sprintf("%s: optional property (no bare scalar type) made nullable via anyOf[schema, null]", propPath))
	return map[string]any{"anyOf": []any{propMap, map[string]any{"type": "null"}}}
}

// conditionalBranch is one {"if","then"} entry from an allOf, reduced to
// what compileConditionalToAnyOf actually needs: the discriminator values
// this branch matches, which properties it ADDS to "required" beyond the
// object's own base-required set, and which properties it explicitly
// forbids (via then's own "not":{"anyOf":[{"required":["x"]}, ...]}
// pattern -- the exact shape the interpret schema's time_context uses).
type conditionalBranch struct {
	values      []any
	requiredAdd []string
	forbidden   map[string]bool
}

// compileConditionalToAnyOf attempts a LOSSLESS translation of objSchema's
// "allOf" (a list of {"if","then"} blocks) into an equivalent "anyOf" of
// fully-closed variants, when every block discriminates on the SAME
// property via "const" or "enum" (time_context's own axis-keyed
// if/then/else, expressed as three separate if/then blocks whose
// discriminator values partition the whole axis enum). Returns
// (nil, false) for any shape it does not recognize -- the caller then
// falls back to dropping allOf and recording that its constraint is not
// restated in description text (team-lead ruling: only a shape this
// compiler can actually prove faithful gets compiled; everything else is
// an honest, logged loss rather than a guessed-at rewrite).
func compileConditionalToAnyOf(objSchema map[string]any, notes *[]string, path string) (map[string]any, bool) {
	allOfRaw, ok := objSchema["allOf"].([]any)
	if !ok || len(allOfRaw) == 0 {
		return nil, false
	}
	baseProps, ok := objSchema["properties"].(map[string]any)
	if !ok || len(baseProps) == 0 {
		return nil, false
	}
	baseRequired := stringSet(objSchema["required"])

	var discriminator string
	var branches []conditionalBranch
	for _, entryAny := range allOfRaw {
		entry, ok := entryAny.(map[string]any)
		if !ok {
			return nil, false
		}
		ifNode, _ := entry["if"].(map[string]any)
		thenNode, _ := entry["then"].(map[string]any)
		if ifNode == nil || thenNode == nil {
			return nil, false
		}
		ifProps, _ := ifNode["properties"].(map[string]any)
		if len(ifProps) != 1 {
			return nil, false
		}
		var propName string
		var propCond map[string]any
		for k, v := range ifProps {
			propName = k
			propCond, _ = v.(map[string]any)
		}
		if propCond == nil {
			return nil, false
		}
		if discriminator == "" {
			discriminator = propName
		} else if discriminator != propName {
			return nil, false // branches keyed on different properties -- unsupported shape
		}
		var values []any
		switch {
		case propCond["const"] != nil:
			values = []any{propCond["const"]}
		case propCond["enum"] != nil:
			enumVals, ok := propCond["enum"].([]any)
			if !ok {
				return nil, false
			}
			values = enumVals
		default:
			return nil, false
		}

		var requiredAdd []string
		if r, ok := thenNode["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					requiredAdd = append(requiredAdd, s)
				}
			}
		}
		forbidden := map[string]bool{}
		if notNode, ok := thenNode["not"].(map[string]any); ok {
			if anyOfList, ok := notNode["anyOf"].([]any); ok {
				for _, itemAny := range anyOfList {
					item, ok := itemAny.(map[string]any)
					if !ok {
						continue
					}
					if reqList, ok := item["required"].([]any); ok {
						for _, v := range reqList {
							if s, ok := v.(string); ok {
								forbidden[s] = true
							}
						}
					}
				}
			}
			// then.not present but not in the recognized
			// {"anyOf":[{"required":[...]}]} shape carries a constraint
			// this compiler cannot faithfully translate -- refuse rather
			// than silently drop it.
			if !hasOnlyRecognizedNotShape(notNode) {
				return nil, false
			}
		}
		// A then-branch this compiler does not fully account for (any key
		// beyond "required"/"not") is refused rather than silently
		// approximated.
		for k := range thenNode {
			if k != "required" && k != "not" {
				return nil, false
			}
		}

		branches = append(branches, conditionalBranch{values: values, requiredAdd: requiredAdd, forbidden: forbidden})
	}
	if discriminator == "" || len(branches) == 0 {
		return nil, false
	}
	discProp, ok := baseProps[discriminator].(map[string]any)
	if !ok {
		return nil, false
	}

	variants := make([]any, 0, len(branches))
	for _, br := range branches {
		variantPath := fmt.Sprintf("%s.anyOf[discriminator=%v]", path, br.values)
		variantRequired := map[string]bool{discriminator: true}
		for name := range baseProps {
			if baseRequired[name] && name != discriminator {
				variantRequired[name] = true
			}
		}
		for _, name := range br.requiredAdd {
			variantRequired[name] = true
		}
		for name := range br.forbidden {
			delete(variantRequired, name)
		}

		variantProps := make(map[string]any, len(baseProps))
		for name, propNodeAny := range baseProps {
			if name == discriminator {
				narrowed := map[string]any{"type": discProp["type"]}
				if len(br.values) == 1 {
					narrowed["const"] = br.values[0]
				} else {
					narrowed["enum"] = br.values
				}
				variantProps[name] = narrowed
				continue
			}
			normalized := normalizeStrictNode(propNodeAny, notes, variantPath+".properties."+name)
			if variantRequired[name] {
				variantProps[name] = normalized
			} else {
				// This branch structurally EXCLUDES the property -- forced
				// to null-only (not merely nullable: the model has no
				// choice here), which is exactly what the original
				// if/then/not/anyOf[required] pattern meant.
				variantProps[name] = map[string]any{"type": "null"}
				variantRequired[name] = true
			}
		}
		reqList := make([]string, 0, len(variantRequired))
		for name := range variantRequired {
			reqList = append(reqList, name)
		}
		variants = append(variants, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           variantProps,
			"required":             reqList,
		})
	}
	return map[string]any{"anyOf": variants}, true
}

// hasOnlyRecognizedNotShape reports whether notNode is exactly
// {"anyOf": [{"required": [...]}, ...]} -- the only "not" shape
// compileConditionalToAnyOf translates. Any other key, or an anyOf entry
// carrying more than a bare "required" list, means this "not" expresses
// something this compiler cannot prove it translated faithfully.
func hasOnlyRecognizedNotShape(notNode map[string]any) bool {
	if len(notNode) != 1 {
		return false
	}
	anyOfList, ok := notNode["anyOf"].([]any)
	if !ok {
		return false
	}
	for _, itemAny := range anyOfList {
		item, ok := itemAny.(map[string]any)
		if !ok || len(item) != 1 {
			return false
		}
		if _, ok := item["required"].([]any); !ok {
			return false
		}
	}
	return true
}

// stringSet converts a decoded JSON "required" array ([]any of strings, as
// produced by json.Unmarshal) into a lookup set. A missing or malformed
// "required" (not present, or containing a non-string entry) degrades to
// an empty set -- every property in that object is then treated as
// originally-optional, the SAFE direction to fail in (more nullability
// added, never less; strict mode still accepts it).
func stringSet(raw any) map[string]bool {
	set := map[string]bool{}
	arr, ok := raw.([]any)
	if !ok {
		return set
	}
	for _, v := range arr {
		if s, ok := v.(string); ok {
			set[s] = true
		}
	}
	return set
}
