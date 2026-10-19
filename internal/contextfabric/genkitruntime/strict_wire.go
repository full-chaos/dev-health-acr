package genkitruntime

import (
	"encoding/json"
	"slices"
)

// The strict wire is the closed form of an output schema that a provider's
// structured outputs accept: no open maps, no conditional keywords, no
// untyped property, and every property required (an optional property is
// counted against a provider limit). The local check and the served shape
// keep the original schema; restoreStrictReply turns a wire reply back into
// the original shape before that check runs.
const (
	mapEntryKey   = "key"
	mapEntryValue = "value"

	scalarKind = "kind"
)

var scalarMembers = []string{"string", "integer", "number", "boolean", "null"}

func strictWire(schema map[string]any) map[string]any {
	out, _ := strictNode(schema, false).(map[string]any)
	return out
}

func isOpenMap(node map[string]any) (map[string]any, bool) {
	if node["properties"] != nil {
		return nil, false
	}
	value, ok := node["additionalProperties"].(map[string]any)
	return value, ok
}

// isScalarValue recognises ContextFabricScalarValue: a closed object whose
// members are exactly the five scalar kinds, all optional.
func isScalarValue(node map[string]any) bool {
	properties, _ := node["properties"].(map[string]any)
	if len(properties) != len(scalarMembers) || node["additionalProperties"] != false {
		return false
	}
	for _, member := range scalarMembers {
		if _, ok := properties[member]; !ok {
			return false
		}
	}
	return true
}

func requiredSet(node map[string]any) map[string]bool {
	set := make(map[string]bool)
	list, _ := node["required"].([]any)
	for _, name := range list {
		if text, ok := name.(string); ok {
			set[text] = true
		}
	}
	return set
}

// emptyDefault reports whether an absent optional property of this schema is
// sent as its empty value: a plain string, an enum string, or an array. Any
// other optional property (object, date-time, untyped) is sent as null.
func emptyDefault(node map[string]any) bool {
	switch node["type"] {
	case "array":
		return true
	case "string":
		_, formatted := node["format"]
		return !formatted
	}
	return false
}

func strictNode(node any, optional bool) any {
	typed, ok := node.(map[string]any)
	if !ok {
		return node
	}
	if valueSchema, open := isOpenMap(typed); open {
		entry := map[string]any{
			"type": "object",
			"properties": map[string]any{
				mapEntryKey:   map[string]any{"type": "string"},
				mapEntryValue: strictNode(valueSchema, false),
			},
			"required":             []any{mapEntryKey, mapEntryValue},
			"additionalProperties": false,
		}
		out := map[string]any{"type": "array", "items": entry}
		copyDescription(out, typed)
		return out
	}
	if isScalarValue(typed) {
		return strictScalar(typed)
	}
	if _, untyped := typed["type"]; !untyped && typed["properties"] == nil && typed["anyOf"] == nil && typed["enum"] == nil {
		return untypedStrict(typed)
	}
	out := make(map[string]any, len(typed))
	for key, value := range typed {
		switch key {
		case "if", "then", "else", "not", "allOf", "required", "properties":
			continue
		case "items":
			out[key] = strictNode(value, false)
		default:
			out[key] = value
		}
	}
	if properties, ok := typed["properties"].(map[string]any); ok {
		required := requiredSet(typed)
		wire := make(map[string]any, len(properties))
		names := make([]any, 0, len(properties))
		for name, child := range properties {
			names = append(names, name)
			wire[name] = strictNode(child, !required[name])
		}
		slices.SortFunc(names, func(a, b any) int { return compareStrings(a.(string), b.(string)) })
		out["properties"] = wire
		out["required"] = names
		if _, conditional := typed["allOf"]; conditional {
			out["description"] = joinDescription(out["description"], timeContextRules)
		}
	}
	if optional {
		return optionalWire(typed, out)
	}
	return out
}

const timeContextRules = "Set exactly the fields the axis allows and null for the others: current takes none of as_of, start, end; valid_time and observed_time take as_of only; range takes start and end only."

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func copyDescription(out, from map[string]any) {
	if description, ok := from["description"]; ok {
		out["description"] = description
	}
}

func joinDescription(existing any, extra string) string {
	if text, ok := existing.(string); ok && text != "" {
		return text + " " + extra
	}
	return extra
}

// optionalWire is the wire form of a property the original schema leaves
// optional: an empty string or array where empty means absent for the served
// shape, an enum widened by the empty member, and null for everything else.
func optionalWire(original, wire map[string]any) any {
	if emptyDefault(original) {
		if values, ok := wire["enum"].([]any); ok {
			wire["enum"] = append(slices.Clone(values), "")
		}
		return wire
	}
	if kind, ok := wire["type"].(string); ok {
		nullable := make(map[string]any, len(wire))
		for key, value := range wire {
			nullable[key] = value
		}
		nullable["type"] = []any{kind, "null"}
		return nullable
	}
	return map[string]any{"anyOf": []any{wire, map[string]any{"type": "null"}}}
}

// strictScalar is ContextFabricScalarValue as a tagged record: "kind" names
// the one member that counts, so an empty string and an absent string stay
// different answers (kind=string with "" versus another kind).
func strictScalar(original map[string]any) map[string]any {
	properties := map[string]any{
		scalarKind: map[string]any{"type": "string", "enum": []any{"string", "integer", "number", "boolean", "null"}},
		"string":   map[string]any{"type": "string"},
		"integer":  map[string]any{"type": "integer"},
		"number":   map[string]any{"type": "number"},
		"boolean":  map[string]any{"type": "boolean"},
	}
	out := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             []any{"boolean", "integer", scalarKind, "number", "string"},
		"additionalProperties": false,
		"description":          "A scalar. Set kind to the one member that carries the value (null means no value) and fill that member; fill the others with \"\", 0 or false.",
	}
	return out
}

// untypedStrict gives the one deliberately unconstrained property
// (synthesisOutput.CoverageDisclosures) the type a strict schema requires:
// an array of {detail_id, text}. The reply is still decoded leniently.
func untypedStrict(original map[string]any) map[string]any {
	node := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"detail_id": map[string]any{"type": "string"},
				"text":      map[string]any{"type": "string"},
			},
			"required":             []any{"detail_id", "text"},
			"additionalProperties": false,
		},
	}
	copyDescription(node, original)
	return node
}

// restoreStrictReply rewrites a reply that follows the strict wire back to
// the shape of the original schema. Text that is not JSON, or a structure
// that does not follow the wire (a repeated map key, a missing kind member),
// is returned unchanged so the local check refuses it by name.
func restoreStrictReply(original map[string]any, text string) string {
	var reply any
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return text
	}
	restored, ok := restoreNode(original, reply)
	if !ok {
		return text
	}
	raw, err := json.Marshal(restored)
	if err != nil {
		return text
	}
	return string(raw)
}

func restoreNode(schema map[string]any, value any) (any, bool) {
	if valueSchema, open := isOpenMap(schema); open {
		entries, ok := value.([]any)
		if !ok {
			return value, false
		}
		object := make(map[string]any, len(entries))
		for _, entry := range entries {
			fields, ok := entry.(map[string]any)
			if !ok {
				return value, false
			}
			key, ok := fields[mapEntryKey].(string)
			if _, repeated := object[key]; !ok || repeated {
				return value, false
			}
			item, present := fields[mapEntryValue]
			if !present {
				return value, false
			}
			restored, ok := restoreNode(valueSchema, item)
			if !ok {
				return value, false
			}
			object[key] = restored
		}
		return object, true
	}
	if isScalarValue(schema) {
		return restoreScalar(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		required := requiredSet(schema)
		for name, child := range typed {
			childSchema, known := properties[name].(map[string]any)
			if !known {
				continue
			}
			restored, ok := restoreNode(childSchema, child)
			if !ok {
				return value, false
			}
			if !required[name] && isEmptyDefault(childSchema, restored) {
				delete(typed, name)
				continue
			}
			typed[name] = restored
		}
	case []any:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return value, true
		}
		for i, child := range typed {
			restored, ok := restoreNode(items, child)
			if !ok {
				return value, false
			}
			typed[i] = restored
		}
	}
	return value, true
}

// isEmptyDefault reports whether an optional property's restored value is the
// wire's stand-in for absent: null, "" for a string, [] for an array. The
// untyped coverage_disclosures property is sent as an array.
func isEmptyDefault(schema map[string]any, value any) bool {
	if value == nil {
		return !emptyDefault(schema) && schema["type"] != nil
	}
	switch typed := value.(type) {
	case string:
		return typed == "" && schema["type"] == "string"
	case []any:
		return len(typed) == 0 && (schema["type"] == "array" || schema["type"] == nil)
	case map[string]any:
		_, open := isOpenMap(schema)
		return len(typed) == 0 && open
	}
	return false
}

func restoreScalar(value any) (any, bool) {
	fields, ok := value.(map[string]any)
	if !ok {
		return value, false
	}
	kind, ok := fields[scalarKind].(string)
	if !ok {
		return value, false
	}
	if kind == "null" {
		return map[string]any{"null": true}, true
	}
	member, present := fields[kind]
	if !present || !slices.Contains(scalarMembers, kind) {
		return value, false
	}
	return map[string]any{kind: member}, true
}
