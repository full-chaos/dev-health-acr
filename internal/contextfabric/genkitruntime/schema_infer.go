// The schema inference below is derived from github.com/firebase/genkit/go
// v1.11.0 (core.InferSchemaMap, internal/base/json.go), Apache-2.0,
// Copyright 2025 Google LLC. The wire text it produces is what the model
// sees, so it must stay byte-identical to that version's output.

package genkitruntime

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
)

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// inferSchemaMap infers a flattened (no $ref) JSON Schema from a Go value and
// returns it as a map. It panics only if the reflected schema cannot be
// marshaled, which no output type of this package can trigger.
func inferSchemaMap(value any) map[string]any {
	return schemaAsMap(inferJSONSchema(value))
}

// inferJSONSchema marks each struct type in progress while it is reflected, so
// a self-reference gets an "any" schema and sibling uses of one type each get
// a full reflection.
func inferJSONSchema(x any) *jsonschema.Schema {
	inProgress := make(map[reflect.Type]bool)
	var mapper func(reflect.Type) *jsonschema.Schema
	mapper = func(t reflect.Type) *jsonschema.Schema {
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Interface {
			return &jsonschema.Schema{
				Type:  "array",
				Items: &jsonschema.Schema{AdditionalProperties: jsonschema.TrueSchema},
			}
		}
		baseType := t
		if t.Kind() == reflect.Ptr {
			baseType = t.Elem()
		}
		if baseType.Kind() != reflect.Struct {
			return nil
		}
		if inProgress[baseType] {
			return anyStructSchema(baseType)
		}

		inProgress[baseType] = true
		defer delete(inProgress, baseType)

		firstCall := true
		sub := jsonschema.Reflector{
			DoNotReference: true,
			Anonymous:      true,
			Mapper: func(st reflect.Type) *jsonschema.Schema {
				if firstCall && st == baseType {
					firstCall = false
					return nil
				}
				return mapper(st)
			},
		}
		s := sub.ReflectFromType(baseType)
		s.Version = ""
		return s
	}

	r := jsonschema.Reflector{DoNotReference: true, Anonymous: true, Mapper: mapper}
	s := r.Reflect(x)
	s.Version = ""
	return s
}

func anyStructSchema(t reflect.Type) *jsonschema.Schema {
	if t.Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType) {
		return &jsonschema.Schema{AdditionalProperties: jsonschema.TrueSchema}
	}
	return &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: jsonschema.TrueSchema,
	}
}

func schemaAsMap(s *jsonschema.Schema) map[string]any {
	jsb, err := s.MarshalJSON()
	if err != nil {
		panic(fmt.Sprintf("marshal output schema: %v", err))
	}
	if string(jsb) == "true" {
		return make(map[string]any)
	}
	var m map[string]any
	if err := json.Unmarshal(jsb, &m); err != nil {
		panic(fmt.Sprintf("unmarshal output schema: %v", err))
	}
	return m
}
