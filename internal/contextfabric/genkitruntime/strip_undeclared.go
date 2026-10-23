package genkitruntime

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
)

const (
	llmUndeclaredDroppedMessage = "context fabric model reply had undeclared properties dropped"
	maxLoggedDroppedPaths       = 5
)

var loggedKeyName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)

// stripUndeclared drops, from a decoded model reply, every property an object
// node of a CLOSED schema (additionalProperties false) does not declare. It
// never adds, renames or changes a value and never touches a declared
// property, so required fields, types, enums and grounding are judged on
// exactly what the model declared. Open maps, untyped nodes and combinators
// are left as written. It returns the paths of the dropped keys.
func stripUndeclared(value any, schema map[string]any, path string) []string {
	if len(schema) == 0 || hasCombinator(schema) {
		return nil
	}
	var dropped []string
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if properties != nil {
			if closedProps, ok := schema["additionalProperties"].(bool); ok && !closedProps {
				for key := range typed {
					if _, declared := properties[key]; !declared {
						dropped = append(dropped, path+"."+loggedKey(key))
						delete(typed, key)
					}
				}
			}
			for key, child := range typed {
				if childSchema, ok := properties[key].(map[string]any); ok {
					dropped = append(dropped, stripUndeclared(child, childSchema, path+"."+loggedKey(key))...)
				}
			}
			return dropped
		}
		if mapSchema, ok := schema["additionalProperties"].(map[string]any); ok {
			for key, child := range typed {
				dropped = append(dropped, stripUndeclared(child, mapSchema, path+"."+loggedKey(key))...)
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for _, element := range typed {
				dropped = append(dropped, stripUndeclared(element, items, path+"[]")...)
			}
		}
	}
	return dropped
}

func hasCombinator(schema map[string]any) bool {
	for _, key := range []string{"anyOf", "oneOf", "allOf", "not", "$ref", "if", "then", "else"} {
		if _, present := schema[key]; present {
			return true
		}
	}
	return false
}

// loggedKey keeps a model-chosen key name loggable: a short identifier as
// written, anything else as "?".
func loggedKey(key string) string {
	if loggedKeyName.MatchString(key) {
		return key
	}
	return "?"
}

// dropUndeclaredProperties returns the candidate with undeclared properties
// removed, and the sorted distinct paths dropped. A candidate with nothing to
// drop is returned byte-identical. A candidate that does not decode is left
// for the schema check to refuse.
func dropUndeclaredProperties(candidate string, schema map[string]any) (string, []string) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(candidate)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return candidate, nil
	}
	dropped := stripUndeclared(value, schema, "$")
	if len(dropped) == 0 {
		return candidate, nil
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return candidate, nil
	}
	sort.Strings(dropped)
	distinct := dropped[:0]
	for _, path := range dropped {
		if len(distinct) == 0 || distinct[len(distinct)-1] != path {
			distinct = append(distinct, path)
		}
	}
	return string(bytes.TrimSpace(out.Bytes())), distinct
}
