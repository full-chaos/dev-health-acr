package contractcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const draft202012 = "https://json-schema.org/draft/2020-12/schema"

type schemaRegistry struct {
	byName     map[string]map[string]any
	byID       map[string]string
	regexCache map[string]*regexp.Regexp
}

func newSchemaRegistry() *schemaRegistry {
	return &schemaRegistry{
		byName:     map[string]map[string]any{},
		byID:       map[string]string{},
		regexCache: map[string]*regexp.Regexp{},
	}
}

func (r *schemaRegistry) add(name string, schema map[string]any) error {
	if got, _ := schema["$schema"].(string); got != draft202012 {
		return fmt.Errorf("$schema must be %q, got %q", draft202012, got)
	}
	id, ok := schema["$id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return errors.New("non-empty $id is required")
	}
	if previous, exists := r.byID[id]; exists {
		return fmt.Errorf("duplicate $id %q already used by %s", id, previous)
	}
	if err := r.checkSchema(schema, "$"); err != nil {
		return err
	}
	r.byName[name] = schema
	r.byID[id] = name
	return nil
}

func (r *schemaRegistry) checkReferences() error {
	for name, schema := range r.byName {
		if err := r.walkRefs(name, schema); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (r *schemaRegistry) walkRefs(currentName string, value any) error {
	switch node := value.(type) {
	case map[string]any:
		if raw, exists := node["$ref"]; exists {
			ref, ok := raw.(string)
			if !ok {
				return errors.New("$ref must be a string")
			}
			name, fragment := splitSchemaRef(currentName, ref)
			target, ok := r.byName[name]
			if !ok {
				return fmt.Errorf("unresolved $ref %q", ref)
			}
			if fragment != "" {
				if _, err := resolveJSONPointer(target, fragment); err != nil {
					return fmt.Errorf("unresolved $ref %q: %w", ref, err)
				}
			}
		}
		for _, child := range node {
			if err := r.walkRefs(currentName, child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := r.walkRefs(currentName, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *schemaRegistry) validate(schemaName string, instance any) error {
	schema, ok := r.byName[schemaName]
	if !ok {
		return fmt.Errorf("unknown schema %s", schemaName)
	}
	return r.validateAt(schemaName, schema, instance, "$", map[string]bool{})
}

func (r *schemaRegistry) validateAt(schemaName string, schema map[string]any, instance any, path string, refStack map[string]bool) error {
	if raw, ok := schema["$ref"]; ok {
		ref, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s: $ref must be a string", path)
		}
		name, fragment := splitSchemaRef(schemaName, ref)
		key := name + "#" + fragment
		if refStack[key] {
			return fmt.Errorf("%s: cyclic $ref %q", path, ref)
		}
		target, ok := r.byName[name]
		if !ok {
			return fmt.Errorf("%s: unresolved $ref %q", path, ref)
		}
		var targetValue any = target
		if fragment != "" {
			var err error
			targetValue, err = resolveJSONPointer(target, fragment)
			if err != nil {
				return fmt.Errorf("%s: unresolved $ref %q: %w", path, ref, err)
			}
		}
		targetSchema, ok := targetValue.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: $ref %q does not resolve to a schema object", path, ref)
		}
		refStack[key] = true
		err := r.validateAt(name, targetSchema, instance, path, refStack)
		delete(refStack, key)
		if err != nil {
			return err
		}
	}

	if children, ok := schema["allOf"].([]any); ok {
		for i, raw := range children {
			child, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: allOf[%d] is not a schema", path, i)
			}
			if err := r.validateAt(schemaName, child, instance, path, refStack); err != nil {
				return err
			}
		}
	}
	if children, ok := schema["anyOf"].([]any); ok {
		matched := false
		for _, raw := range children {
			child, ok := raw.(map[string]any)
			if ok && r.validateAt(schemaName, child, instance, path, cloneStack(refStack)) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: does not match anyOf", path)
		}
	}
	if children, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, raw := range children {
			child, ok := raw.(map[string]any)
			if ok && r.validateAt(schemaName, child, instance, path, cloneStack(refStack)) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: matches %d oneOf branches, expected exactly one", path, matches)
		}
	}
	if conditional, ok := schema["if"].(map[string]any); ok {
		if r.validateAt(schemaName, conditional, instance, path, cloneStack(refStack)) == nil {
			if thenSchema, ok := schema["then"].(map[string]any); ok {
				if err := r.validateAt(schemaName, thenSchema, instance, path, refStack); err != nil {
					return err
				}
			}
		} else if elseSchema, ok := schema["else"].(map[string]any); ok {
			if err := r.validateAt(schemaName, elseSchema, instance, path, refStack); err != nil {
				return err
			}
		}
	}
	if notSchema, ok := schema["not"].(map[string]any); ok {
		if r.validateAt(schemaName, notSchema, instance, path, cloneStack(refStack)) == nil {
			return fmt.Errorf("%s: matches disallowed not schema", path)
		}
	}

	if typeSpec, exists := schema["type"]; exists && !matchesType(typeSpec, instance) {
		return fmt.Errorf("%s: expected type %s, got %s", path, compactJSON(typeSpec), jsonType(instance))
	}
	if expected, exists := schema["const"]; exists && !jsonEqual(expected, instance) {
		return fmt.Errorf("%s: expected const %s", path, compactJSON(expected))
	}
	if choices, ok := schema["enum"].([]any); ok {
		matched := false
		for _, choice := range choices {
			if jsonEqual(choice, instance) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: %s is not in enum", path, compactJSON(instance))
		}
	}

	switch value := instance.(type) {
	case map[string]any:
		return r.validateObject(schemaName, schema, value, path, refStack)
	case []any:
		return r.validateArray(schemaName, schema, value, path, refStack)
	case string:
		return r.validateString(schema, value, path)
	case json.Number:
		return validateNumber(schema, value, path)
	default:
		return nil
	}
}

func (r *schemaRegistry) validateObject(schemaName string, schema map[string]any, value map[string]any, path string, refStack map[string]bool) error {
	if min, ok := integerKeyword(schema, "minProperties"); ok && len(value) < min {
		return fmt.Errorf("%s: has %d properties, minimum is %d", path, len(value), min)
	}
	if max, ok := integerKeyword(schema, "maxProperties"); ok && len(value) > max {
		return fmt.Errorf("%s: has %d properties, maximum is %d", path, len(value), max)
	}
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			name, ok := raw.(string)
			if ok {
				if _, exists := value[name]; !exists {
					return fmt.Errorf("%s: missing required property %q", path, name)
				}
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		instanceValue, exists := value[name]
		if !exists {
			continue
		}
		propertySchema, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: invalid property schema %q", path, name)
		}
		if err := r.validateAt(schemaName, propertySchema, instanceValue, childPath(path, name), refStack); err != nil {
			return err
		}
	}
	if additional, exists := schema["additionalProperties"]; exists {
		for name, instanceValue := range value {
			if _, known := properties[name]; known {
				continue
			}
			switch rule := additional.(type) {
			case bool:
				if !rule {
					return fmt.Errorf("%s: additional property %q is not allowed", path, name)
				}
			case map[string]any:
				if err := r.validateAt(schemaName, rule, instanceValue, childPath(path, name), refStack); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *schemaRegistry) validateArray(schemaName string, schema map[string]any, value []any, path string, refStack map[string]bool) error {
	if min, ok := integerKeyword(schema, "minItems"); ok && len(value) < min {
		return fmt.Errorf("%s: has %d items, minimum is %d", path, len(value), min)
	}
	if max, ok := integerKeyword(schema, "maxItems"); ok && len(value) > max {
		return fmt.Errorf("%s: has %d items, maximum is %d", path, len(value), max)
	}
	if unique, _ := schema["uniqueItems"].(bool); unique {
		seen := map[string]int{}
		for i, item := range value {
			key := compactJSON(item)
			if previous, exists := seen[key]; exists {
				return fmt.Errorf("%s: items %d and %d are duplicates", path, previous, i)
			}
			seen[key] = i
		}
	}
	if itemSchema, ok := schema["items"].(map[string]any); ok {
		for i, item := range value {
			if err := r.validateAt(schemaName, itemSchema, item, fmt.Sprintf("%s[%d]", path, i), refStack); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *schemaRegistry) validateString(schema map[string]any, value, path string) error {
	length := utf8.RuneCountInString(value)
	if min, ok := integerKeyword(schema, "minLength"); ok && length < min {
		return fmt.Errorf("%s: string length %d is below minimum %d", path, length, min)
	}
	if max, ok := integerKeyword(schema, "maxLength"); ok && length > max {
		return fmt.Errorf("%s: string length %d exceeds maximum %d", path, length, max)
	}
	if pattern, ok := schema["pattern"].(string); ok {
		rx := r.regexCache[pattern]
		if rx == nil {
			var err error
			rx, err = regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("%s: invalid schema pattern %q: %w", path, pattern, err)
			}
			r.regexCache[pattern] = rx
		}
		if !rx.MatchString(value) {
			return fmt.Errorf("%s: value does not match pattern %q", path, pattern)
		}
	}
	if format, ok := schema["format"].(string); ok {
		switch format {
		case "date-time":
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return fmt.Errorf("%s: invalid date-time: %w", path, err)
			}
		case "uri":
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" {
				return fmt.Errorf("%s: invalid absolute URI %q", path, value)
			}
		case "uuid":
			if !uuidPattern.MatchString(value) {
				return fmt.Errorf("%s: invalid UUID %q", path, value)
			}
		default:
			return fmt.Errorf("%s: unsupported format %q", path, format)
		}
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func validateNumber(schema map[string]any, value json.Number, path string) error {
	number, err := strconv.ParseFloat(value.String(), 64)
	if err != nil {
		return fmt.Errorf("%s: invalid number %q", path, value.String())
	}
	if minimum, ok := numberKeyword(schema, "minimum"); ok && number < minimum {
		return fmt.Errorf("%s: number %v is below minimum %v", path, number, minimum)
	}
	if maximum, ok := numberKeyword(schema, "maximum"); ok && number > maximum {
		return fmt.Errorf("%s: number %v exceeds maximum %v", path, number, maximum)
	}
	if schema["type"] == "integer" {
		if _, err := value.Int64(); err != nil {
			return fmt.Errorf("%s: expected integer, got %s", path, value.String())
		}
	}
	return nil
}

var annotations = map[string]bool{
	"$schema": true, "$id": true, "$comment": true,
	"title": true, "description": true, "default": true,
	"examples": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

var supportedAssertions = map[string]bool{
	"$ref": true, "$defs": true,
	"type": true, "enum": true, "const": true,
	"properties": true, "required": true, "additionalProperties": true,
	"minProperties": true, "maxProperties": true,
	"items": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"minLength": true, "maxLength": true, "pattern": true, "format": true,
	"minimum": true, "maximum": true,
	"allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"if": true, "then": true, "else": true,
}

func (r *schemaRegistry) checkSchema(schema map[string]any, path string) error {
	for key, value := range schema {
		if !annotations[key] && !supportedAssertions[key] {
			return fmt.Errorf("%s: unsupported JSON Schema keyword %q", path, key)
		}
		switch key {
		case "$schema", "$id", "$comment", "title", "description", "pattern", "format", "$ref":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s.%s must be a string", path, key)
			}
		case "type":
			if !validTypeSpec(value) {
				return fmt.Errorf("%s.type must be a valid type string or array", path)
			}
		case "properties", "$defs":
			children, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s must be an object", path, key)
			}
			for name, raw := range children {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s.%s must be a schema object", path, key, name)
				}
				if err := r.checkSchema(child, path+"."+key+"."+name); err != nil {
					return err
				}
			}
		case "items", "not", "if", "then", "else":
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s must be a schema object", path, key)
			}
			if err := r.checkSchema(child, path+"."+key); err != nil {
				return err
			}
		case "additionalProperties":
			if child, ok := value.(map[string]any); ok {
				if err := r.checkSchema(child, path+".additionalProperties"); err != nil {
					return err
				}
			} else if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s.additionalProperties must be boolean or schema", path)
			}
		case "allOf", "anyOf", "oneOf":
			children, ok := value.([]any)
			if !ok || len(children) == 0 {
				return fmt.Errorf("%s.%s must be a non-empty array", path, key)
			}
			for i, raw := range children {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s[%d] must be a schema object", path, key, i)
				}
				if err := r.checkSchema(child, fmt.Sprintf("%s.%s[%d]", path, key, i)); err != nil {
					return err
				}
			}
		case "required":
			if !isUniqueStringArray(value) {
				return fmt.Errorf("%s.required must be a unique array of strings", path)
			}
		case "enum":
			if values, ok := value.([]any); !ok || len(values) == 0 {
				return fmt.Errorf("%s.enum must be a non-empty array", path)
			}
		case "minProperties", "maxProperties", "minItems", "maxItems", "minLength", "maxLength":
			if n, ok := jsonInteger(value); !ok || n < 0 {
				return fmt.Errorf("%s.%s must be a non-negative integer", path, key)
			}
		case "minimum", "maximum":
			if _, ok := jsonFloat(value); !ok {
				return fmt.Errorf("%s.%s must be a number", path, key)
			}
		case "uniqueItems", "deprecated", "readOnly", "writeOnly":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s.%s must be boolean", path, key)
			}
		}
	}
	return nil
}

func splitSchemaRef(currentName, ref string) (string, string) {
	parts := strings.SplitN(ref, "#", 2)
	name := parts[0]
	if name == "" {
		name = currentName
	} else {
		name = filepath.Base(filepath.FromSlash(name))
	}
	fragment := ""
	if len(parts) == 2 {
		fragment = parts[1]
	}
	return name, fragment
}

func resolveJSONPointer(root any, fragment string) (any, error) {
	if fragment == "" {
		return root, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil, fmt.Errorf("unsupported fragment %q", fragment)
	}
	current := root
	for _, raw := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[token]
			if !ok {
				return nil, fmt.Errorf("pointer token %q not found", token)
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil, fmt.Errorf("invalid array index %q", token)
			}
			current = node[index]
		default:
			return nil, fmt.Errorf("cannot traverse pointer token %q", token)
		}
	}
	return current, nil
}

func matchesType(spec, value any) bool {
	switch expected := spec.(type) {
	case string:
		return matchesSingleType(expected, value)
	case []any:
		for _, raw := range expected {
			if name, ok := raw.(string); ok && matchesSingleType(name, value) {
				return true
			}
		}
	}
	return false
}

func matchesSingleType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	default:
		return false
	}
}

func validTypeSpec(value any) bool {
	valid := map[string]bool{"object": true, "array": true, "string": true, "boolean": true, "null": true, "number": true, "integer": true}
	switch typed := value.(type) {
	case string:
		return valid[typed]
	case []any:
		if len(typed) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, raw := range typed {
			name, ok := raw.(string)
			if !ok || !valid[name] || seen[name] {
				return false
			}
			seen[name] = true
		}
		return true
	default:
		return false
	}
}

func jsonType(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func jsonEqual(left, right any) bool {
	if leftNumber, ok := left.(json.Number); ok {
		if rightNumber, ok := right.(json.Number); ok {
			leftRat, leftOK := new(big.Rat).SetString(leftNumber.String())
			rightRat, rightOK := new(big.Rat).SetString(rightNumber.String())
			return leftOK && rightOK && leftRat.Cmp(rightRat) == 0
		}
	}
	return reflect.DeepEqual(left, right) || compactJSON(left) == compactJSON(right)
}

func compactJSON(value any) string {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprintf("%v", value)
	}
	return strings.TrimSpace(output.String())
}

func integerKeyword(schema map[string]any, key string) (int, bool) {
	value, ok := schema[key]
	if !ok {
		return 0, false
	}
	return jsonInteger(value)
}

func numberKeyword(schema map[string]any, key string) (float64, bool) {
	value, ok := schema[key]
	if !ok {
		return 0, false
	}
	return jsonFloat(value)
}

func jsonInteger(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := strconv.Atoi(number.String())
	return integer, err == nil
}

func jsonFloat(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	float, err := strconv.ParseFloat(number.String(), 64)
	return float, err == nil
}

func isUniqueStringArray(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, raw := range items {
		item, ok := raw.(string)
		if !ok || seen[item] {
			return false
		}
		seen[item] = true
	}
	return true
}

func cloneStack(source map[string]bool) map[string]bool {
	copy := make(map[string]bool, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func childPath(parent, property string) string {
	if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(property) {
		return parent + "." + property
	}
	return parent + "[" + strconv.Quote(property) + "]"
}
