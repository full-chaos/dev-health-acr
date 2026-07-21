package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

func strictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, newConfigParseError(parserClassSyntax, "client JSON must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder); err != nil {
		return nil, newConfigParseError(parserClassSyntax, "parse client JSON: %v", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, newConfigParseError(parserClassSyntax, "parse client JSON trailing data")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, newConfigParseError(parserClassShape, "client JSON root must be an object")
	}
	return root, nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for decoder.More() {
			key, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if keys[name] {
				return fmt.Errorf("duplicate object key %q", name)
			}
			keys[name] = true
			if valueErr := validateJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return fmt.Errorf("invalid JSON delimiter %q", delim)
	}
}

func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, newConfigParseError(parserClassShape, "client JSON value must be an object")
	}
	return object, nil
}

func requiredJSONObject(object map[string]json.RawMessage, name string) (map[string]json.RawMessage, error) {
	raw, ok := object[name]
	if !ok {
		return nil, newConfigParseError(parserClassShape, "client JSON is missing %q", name)
	}
	return rawJSONObject(raw)
}

func requireKeys(object map[string]json.RawMessage, required ...string) error {
	return requireKnownKeys(object, keySet(required...), required...)
}

func requireKnownKeys(object map[string]json.RawMessage, allowed map[string]bool, required ...string) error {
	for key := range object {
		if !allowed[key] {
			return newConfigParseError(parserClassShape, "client JSON has unrecognized key %q", key)
		}
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return newConfigParseError(parserClassShape, "client JSON is missing %q", key)
		}
	}
	return nil
}

func keySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func requiredJSONString(object map[string]json.RawMessage, name string) (string, error) {
	raw, ok := object[name]
	if !ok {
		return "", newConfigParseError(parserClassShape, "client JSON is missing %q", name)
	}
	return rawJSONString(raw)
}

func rawJSONString(raw json.RawMessage) (string, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil || bytes.Equal(raw, []byte("null")) {
		return "", newConfigParseError(parserClassShape, "client JSON value must be a non-null string")
	}
	return value, nil
}

func requiredJSONStringArray(object map[string]json.RawMessage, name string) ([]string, error) {
	raw, ok := object[name]
	if !ok {
		return nil, newConfigParseError(parserClassShape, "client JSON is missing %q", name)
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || bytes.Equal(raw, []byte("null")) {
		return nil, newConfigParseError(parserClassShape, "client JSON value must be a non-null string array")
	}
	return values, nil
}

func requireJSONBool(object map[string]json.RawMessage, name string, expected bool) error {
	raw, ok := object[name]
	if !ok {
		return newConfigParseError(parserClassShape, "client JSON is missing %q", name)
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil || bytes.Equal(raw, []byte("null")) || value != expected {
		return newConfigParseError(parserClassShape, "client JSON has invalid %q", name)
	}
	return nil
}

func optionalStringMap(object map[string]json.RawMessage, name string) (map[string]string, error) {
	raw, ok := object[name]
	if !ok {
		return nil, nil
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, newConfigParseError(parserClassShape, "client JSON value must be a non-null string map")
	}
	return values, nil
}
