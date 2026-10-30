package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"
)

// ParseContextFabricThread is the consumer decode of a thread body. A Go
// value cannot tell an absent key from a key given its zero value, so
// Validate alone would accept documents the published schema refuses. Parse
// refuses them first: data that is not UTF-8, a key given twice anywhere, an
// unknown key, trailing content, a null anywhere (encoding/json reads a null
// into a non-pointer field as no value at all, so "retention_days": null would
// read as the valid retention 0; no field of either schema is nullable), a key
// present as empty, false or zero where the writer omits the field (question,
// result_id, result_withheld, earlier_turns_cursor), and a required key that
// is missing (a missing retention_days would read as 0 too). It then holds
// the value to Validate.
//
// Every number of either document is an integer field, and Parse reads it
// exactly, with strconv.ParseInt on the literal: a fraction or an exponent
// spelling (30.0, 3e1) is refused even where its value is an integer. That is
// stricter than a standard validator of the published schema, which reads
// 30.0 as the integer 30; ACR's writer never writes either form (lead ruling
// on the review rounds: the standard decode path, no hand-rolled reader).
func ParseContextFabricThread(data []byte) (ContextFabricThread, error) {
	return parseContextFabricThreadDocument[ContextFabricThread](data)
}

// ParseContextFabricThreadList is ParseContextFabricThread for the list body.
func ParseContextFabricThreadList(data []byte) (ContextFabricThreadList, error) {
	return parseContextFabricThreadDocument[ContextFabricThreadList](data)
}

func parseContextFabricThreadDocument[T interface{ Validate() error }](data []byte) (T, error) {
	var zero, value T
	if !utf8.Valid(data) {
		return zero, errors.New("document is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return zero, err
	}
	if err := requireJSONIntegerLiterals(data); err != nil {
		return zero, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("decode document: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return zero, errors.New("document has trailing content")
	}
	written, err := json.Marshal(value)
	if err != nil {
		return zero, fmt.Errorf("encode document: %w", err)
	}
	if err := sameJSONKeyPaths(data, written); err != nil {
		return zero, err
	}
	if err := value.Validate(); err != nil {
		return zero, err
	}
	return value, nil
}

// requireJSONIntegerLiterals decodes the document's first JSON value with
// numbers kept as written (json.Decoder.UseNumber) and refuses every number that strconv.ParseInt does not read exactly as
// an int64: a fraction, an exponent, or a value beyond int64. Every number in
// a thread document is an integer field (a feedback document has none).
func requireJSONIntegerLiterals(data []byte) error {
	var tree any
	if err := decodeJSONNumbers(data, &tree); err != nil {
		return err
	}
	var walk func(any, string) error
	walk = func(value any, path string) error {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if err := walk(item, path+"."+key); err != nil {
					return err
				}
			}
		case []any:
			for index, item := range v {
				if err := walk(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		case json.Number:
			if _, err := strconv.ParseInt(string(v), 10, 64); err != nil {
				return fmt.Errorf("document carries %s at %s; an integer field takes an integer literal (no fraction, no exponent, within int64)", string(v), pathOrRoot(path))
			}
		}
		return nil
	}
	return walk(tree, "")
}

// rejectDuplicateJSONKeys refuses a document that names a key twice in any
// object: encoding/json keeps the last one silently, so two readers of one
// payload could read different values.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	type frame struct {
		object bool
		keys   map[string]bool
		// expectKey is true in an object where the next token is a key.
		expectKey bool
	}
	var stack []*frame
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode document: %w", err)
		}
		top := (*frame)(nil)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				if top != nil && top.object {
					top.expectKey = true
				}
				stack = append(stack, &frame{object: delim == '{', keys: map[string]bool{}, expectKey: true})
			case '}', ']':
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].object {
					stack[len(stack)-1].expectKey = true
				}
			}
			continue
		}
		if top == nil || !top.object {
			continue
		}
		if top.expectKey {
			key, _ := token.(string)
			if top.keys[key] {
				return fmt.Errorf("document names %q twice in one object", key)
			}
			top.keys[key] = true
			top.expectKey = false
			continue
		}
		top.expectKey = true
	}
}

// sameJSONKeyPaths holds data to the keys its Go value writes back: a null
// anywhere in data is refused (it decodes as no value); a key in data the
// writer omits was present as an empty, false or zero value, or under a
// spelling encoding/json matched case-insensitively; and a key the writer
// writes that data lacks is a required key that was missing (decoded as its
// zero value).
func sameJSONKeyPaths(data, written []byte) error {
	var received, wrote any
	if err := decodeJSONNumbers(data, &received); err != nil {
		return err
	}
	if err := decodeJSONNumbers(written, &wrote); err != nil {
		return err
	}
	return compareJSONKeyPaths(received, wrote, "")
}

// decodeJSONNumbers decodes the first JSON value with numbers kept as
// written. Trailing content is refused by the document decode in
// parseContextFabricThreadDocument (one check, one place).
func decodeJSONNumbers(data []byte, target *any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode document: %w", err)
	}
	return nil
}

func compareJSONKeyPaths(received, wrote any, path string) error {
	switch r := received.(type) {
	case nil:
		return fmt.Errorf("document carries a null at %s; no field is nullable", pathOrRoot(path))
	case map[string]any:
		w, ok := wrote.(map[string]any)
		if !ok {
			return fmt.Errorf("document %s is an object where the writer writes none", pathOrRoot(path))
		}
		keys := make([]string, 0, len(r))
		for key := range r {
			keys = append(keys, key)
		}
		for key := range w {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range slices.Compact(keys) {
			rv, inReceived := r[key]
			wv, inWritten := w[key]
			switch {
			case inReceived && !inWritten:
				return fmt.Errorf("document carries %s.%s, which the writer does not write: an empty, false or zero value of an optional field, or a key spelled in another case", pathOrRoot(path), key)
			case !inReceived && inWritten:
				return fmt.Errorf("document lacks the required key %s.%s", pathOrRoot(path), key)
			}
			if err := compareJSONKeyPaths(rv, wv, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		w, ok := wrote.([]any)
		if !ok || len(w) != len(r) {
			return fmt.Errorf("document %s is an array the writer does not write back", pathOrRoot(path))
		}
		for index := range r {
			if err := compareJSONKeyPaths(r[index], w[index], fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func pathOrRoot(path string) string {
	if path == "" {
		return "$"
	}
	return "$" + path
}
