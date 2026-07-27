package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// requiredFieldsPresent closes the parity gap decodeExact and canonical
// Validate() leave open together: decodeExact only proves data is valid
// JSON matching target's shape (no unknown fields, no trailing content),
// and Validate() only inspects the Go values decodeExact already
// produced. Once a field the wire schema marks required is absent (or
// explicit JSON null), decoding still leaves it at its Go zero value,
// and for a field whose zero value is itself semantically legitimate
// (permissions/entitlements all false, an empty summary, a 0.0
// confidence), Validate() has no way to tell "present as zero" apart
// from "never sent" - so it accepts a payload the JSON Schema's own
// "required" list would reject. This walks target's Go struct type via
// reflection and confirms every field whose `json` tag lacks
// `,omitempty` - this package's own contract types (internal/contracts/v1)
// use exactly that convention to mark a field wire-required, and every
// such field lines up 1:1 with its schema's "required" array - has a
// present, non-null value in data, recursing into nested structs, slice
// elements, and map values so a gap at any depth (for example
// capabilities.permissions.episode_write or
// context_packet.items[0].flags) is caught the same way a top-level one
// is.
func requiredFieldsPresent(data []byte, target any) error {
	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil
	}
	return requiredStructFieldsPresent(rv.Elem().Type(), json.RawMessage(data), "")
}

var timeType = reflect.TypeFor[time.Time]()

// requiredStructFieldsPresent checks one JSON object level against t's
// fields. raw must already be known-valid JSON for t's shape (decodeExact
// proves this before this function is ever called), so the generic
// map decode below cannot fail in practice; a failure is treated as a
// defensive no-op rather than a hard error, leaving Validate() as the
// backstop.
func requiredStructFieldsPresent(t reflect.Type, raw json.RawMessage, path string) error {
	if isJSONNull(raw) {
		return fmt.Errorf("%s: required value is missing or null", describePath(path))
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for i := range t.NumField() {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // unexported: never part of the wire shape
		}
		name, omitempty, skip := jsonFieldName(field)
		if skip || (field.Anonymous && field.Tag.Get("json") == "") {
			// An embedded field with no explicit tag would normally be
			// inlined per encoding/json's own promotion rules, but no
			// type actually decoded through this boundary
			// (Capabilities, ContextPacket, ExpandedEvidence, and their
			// nested field types) embeds one, so inlining is left
			// unimplemented rather than guessed at.
			continue
		}
		fieldPath := joinPath(path, name)
		rawValue, present := obj[name]
		if !omitempty && !present {
			return fmt.Errorf("%s: required field is missing", fieldPath)
		}
		if !omitempty && isJSONNull(rawValue) && field.Type.Kind() != reflect.Pointer {
			return fmt.Errorf("%s: required field is null", fieldPath)
		}
		if !present || isJSONNull(rawValue) {
			continue
		}
		if err := requiredNestedPresent(field.Type, rawValue, fieldPath); err != nil {
			return err
		}
	}
	return nil
}

// requiredNestedPresent dispatches presence checking for a field's value
// once it is known to be present and non-null: nested structs recurse
// through requiredStructFieldsPresent, slices/arrays and maps recurse
// per element, and every other kind (string, bool, numeric, enum) is
// already fully checked by the presence-and-non-null test the caller
// just performed.
func requiredNestedPresent(t reflect.Type, raw json.RawMessage, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		// time.Time marshals to a JSON string via its own MarshalJSON and
		// has no exported fields to walk; decodeExact already proved raw
		// parses into it, so presence is already fully established.
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		return requiredStructFieldsPresent(t, raw, path)
	case reflect.Slice, reflect.Array:
		return requiredElementsPresent(t.Elem(), raw, path)
	case reflect.Map:
		return requiredMapValuesPresent(t.Elem(), raw, path)
	default:
		return nil
	}
}

// requiredElementsPresent recurses into each element of a JSON array
// whose Go element type is itself a struct (for example
// []ContextPacketItem); arrays of scalars (for example []string) need no
// further check since the array's own presence was already confirmed.
func requiredElementsPresent(elemType reflect.Type, raw json.RawMessage, path string) error {
	for elemType.Kind() == reflect.Pointer {
		elemType = elemType.Elem()
	}
	if elemType.Kind() != reflect.Struct || elemType == timeType {
		return nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil
	}
	for i, element := range elements {
		if err := requiredStructFieldsPresent(elemType, element, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

// requiredMapValuesPresent mirrors requiredElementsPresent for map
// values; every map reachable from a decoded response
// (ExpandedEvidence.Structured, EvidenceRef.Metadata) is a
// map[string]any, so this only matters for a future struct-valued map.
func requiredMapValuesPresent(valueType reflect.Type, raw json.RawMessage, path string) error {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if valueType.Kind() != reflect.Struct || valueType == timeType {
		return nil
	}
	var elements map[string]json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil
	}
	for key, element := range elements {
		if err := requiredStructFieldsPresent(valueType, element, fmt.Sprintf("%s[%s]", path, strconv.Quote(key))); err != nil {
			return err
		}
	}
	return nil
}

// jsonFieldName mirrors encoding/json's own struct tag parsing: an
// explicit name before the first comma, `omitempty` among the options
// after it, a bare "-" tag meaning the field is never on the wire, and
// no tag at all falling back to the field's Go name.
func jsonFieldName(field reflect.StructField) (name string, omitempty, skip bool) {
	tag, ok := field.Tag.Lookup("json")
	if !ok || tag == "" {
		return field.Name, false, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "-" && len(parts) == 1 {
		return "", false, true
	}
	name = parts[0]
	if name == "" {
		name = field.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func joinPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}

func describePath(path string) string {
	if path == "" {
		return "response body"
	}
	return path
}
