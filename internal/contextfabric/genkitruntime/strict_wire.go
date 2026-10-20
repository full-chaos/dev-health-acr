package genkitruntime

import (
	"encoding/json"
	"reflect"
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
		if isScalarValue(valueSchema) {
			out := map[string]any{"type": "array", "items": scalarEntry(valueSchema)}
			copyDescription(out, typed)
			return out
		}
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
		foldWire(typed, out)
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
	out := scalarObject()
	out["description"] = "A scalar. Set kind to the one member that carries the value (null means no value) and fill that member; fill the others with \"\", 0 or false."
	return out
}

// scalarEntry is a map entry whose value is a scalar: the key and the scalar
// members side by side, one object level less than key plus a value object.
func scalarEntry(original map[string]any) map[string]any {
	out := scalarObject()
	properties := out["properties"].(map[string]any)
	properties[mapEntryKey] = map[string]any{"type": "string"}
	out["required"] = []any{"boolean", "integer", scalarKind, mapEntryKey, "number", "string"}
	return out
}

func scalarObject() map[string]any {
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
	if object, isObject := value.(map[string]any); isObject {
		if !unfoldMembers(schema, object) {
			return value, false
		}
	}
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
			key, isText := fields[mapEntryKey].(string)
			if _, repeated := object[key]; !isText || repeated {
				return value, false
			}
			if isScalarValue(valueSchema) {
				delete(fields, mapEntryKey)
				restored, ok := restoreScalar(fields)
				if !ok {
					return value, false
				}
				object[key] = restored
				continue
			}
			if len(fields) != 2 {
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
	if !ok || len(fields) != len(scalarMembers) {
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

// foldSpec merges sibling properties of one shape into one tagged array on the
// wire: the grammar carries the item structure once, and a closed tag enum
// names the original property each item belongs to.
type foldSpec struct {
	name    string
	tag     string
	members []foldMember
	note    string
}

type foldMember struct{ property, tag string }

var foldSpecs = []foldSpec{
	{name: "findings", tag: "list", note: "Every finding of the three lists, each tagged with its list.",
		members: []foldMember{{"remaining_work", "remaining_work"}, {"readiness_gaps", "readiness_gaps"}, {"conflicts", "conflicts"}}},
	{name: "rows", tag: "series", note: "Rows of the table and of the time_series table, each tagged with its series.",
		members: []foldMember{{"rows", "table"}, {"time_series_rows", "time_series"}}},
	{name: "tables", tag: "series", note: "The table and the time_series table declarations, each tagged with its series; none when undeclared.",
		members: []foldMember{{"table", "table"}, {"time_series_table", "time_series"}}},
}

func (spec foldSpec) applies(properties map[string]any) bool {
	for _, member := range spec.members {
		if _, ok := properties[member.property]; !ok {
			return false
		}
	}
	return true
}

func foldItems(original map[string]any) any {
	if original["type"] == "array" {
		return strictNode(original["items"], false)
	}
	return strictNode(original, false)
}

func foldWire(original, out map[string]any) {
	originalProperties, _ := original["properties"].(map[string]any)
	wire, _ := out["properties"].(map[string]any)
	for _, spec := range foldSpecs {
		if !spec.applies(originalProperties) {
			continue
		}
		var item map[string]any
		same := true
		tags := make([]any, 0, len(spec.members))
		for _, member := range spec.members {
			memberSchema, _ := originalProperties[member.property].(map[string]any)
			current, _ := foldItems(memberSchema).(map[string]any)
			if item == nil {
				item = current
			} else if !reflect.DeepEqual(item, current) {
				same = false
			}
			tags = append(tags, member.tag)
		}
		itemProperties, _ := item["properties"].(map[string]any)
		if !same || item == nil || itemProperties == nil || itemProperties[spec.tag] != nil {
			continue
		}
		folded := make(map[string]any, len(item))
		for key, value := range item {
			folded[key] = value
		}
		foldedProperties := make(map[string]any, len(itemProperties)+1)
		for key, value := range itemProperties {
			foldedProperties[key] = value
		}
		foldedProperties[spec.tag] = map[string]any{"type": "string", "enum": tags}
		names := make([]any, 0, len(foldedProperties))
		for key := range foldedProperties {
			names = append(names, key)
		}
		slices.SortFunc(names, func(a, b any) int { return compareStrings(a.(string), b.(string)) })
		folded["properties"] = foldedProperties
		folded["required"] = names
		for _, member := range spec.members {
			delete(wire, member.property)
		}
		wire[spec.name] = map[string]any{"type": "array", "items": folded, "description": spec.note}
		sorted := make([]any, 0, len(wire))
		for key := range wire {
			sorted = append(sorted, key)
		}
		slices.SortFunc(sorted, func(a, b any) int { return compareStrings(a.(string), b.(string)) })
		out["required"] = sorted
	}
}

// unfoldMembers turns each folded array of a wire object back into the
// original member properties, in item order. False is a reply that does not
// follow the wire (an item without a known tag), left for the local check.
func unfoldMembers(schema map[string]any, object map[string]any) bool {
	properties, _ := schema["properties"].(map[string]any)
	for _, spec := range foldSpecs {
		if !spec.applies(properties) {
			continue
		}
		list, present := object[spec.name].([]any)
		if !present {
			continue
		}
		delete(object, spec.name)
		groups := make(map[string][]any, len(spec.members))
		for _, entry := range list {
			item, ok := entry.(map[string]any)
			tag, tagged := item[spec.tag].(string)
			if !ok || !tagged {
				return false
			}
			delete(item, spec.tag)
			known := false
			for _, member := range spec.members {
				known = known || member.tag == tag
			}
			if !known {
				return false
			}
			groups[tag] = append(groups[tag], item)
		}
		for _, member := range spec.members {
			memberSchema, _ := properties[member.property].(map[string]any)
			items := groups[member.tag]
			switch {
			case memberSchema["type"] == "array":
				if items == nil {
					items = []any{}
				}
				object[member.property] = items
			case len(items) == 0:
				object[member.property] = nil
			case len(items) == 1:
				object[member.property] = items[0]
			default:
				return false
			}
		}
	}
	return true
}
