package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
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
		return zero, errors.New("thread document is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return zero, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("decode thread document: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return zero, errors.New("thread document has trailing content")
	}
	written, err := json.Marshal(value)
	if err != nil {
		return zero, fmt.Errorf("encode thread document: %w", err)
	}
	if err := sameJSONKeyPaths(data, written); err != nil {
		return zero, err
	}
	if err := value.Validate(); err != nil {
		return zero, err
	}
	return value, nil
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
			return fmt.Errorf("decode thread document: %w", err)
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
				return fmt.Errorf("thread document names %q twice in one object", key)
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

func decodeJSONNumbers(data []byte, target *any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode thread document: %w", err)
	}
	return nil
}

func compareJSONKeyPaths(received, wrote any, path string) error {
	switch r := received.(type) {
	case nil:
		return fmt.Errorf("thread document carries a null at %s; no field is nullable", pathOrRoot(path))
	case map[string]any:
		w, ok := wrote.(map[string]any)
		if !ok {
			return fmt.Errorf("thread document %s is an object where the writer writes none", pathOrRoot(path))
		}
		keys := make([]string, 0, len(r)+len(w))
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
				return fmt.Errorf("thread document carries %s.%s, which the writer does not write: an empty, false or zero value of an optional field, or a key spelled in another case", pathOrRoot(path), key)
			case !inReceived && inWritten:
				return fmt.Errorf("thread document lacks the required key %s.%s", pathOrRoot(path), key)
			}
			if err := compareJSONKeyPaths(rv, wv, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		w, ok := wrote.([]any)
		if !ok || len(w) != len(r) {
			return fmt.Errorf("thread document %s is an array the writer does not write back", pathOrRoot(path))
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
