package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// stripNullFieldsJSON returns raw with every JSON-null object key removed,
// recursively. answerOne validates and publishes this document, so an
// explicit null is an absent key to the schema and to the engine decoder.
// Number tokens are kept verbatim (UseNumber): an integer above 2^53 is never
// rounded.
func stripNullFieldsJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after the JSON document")
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
