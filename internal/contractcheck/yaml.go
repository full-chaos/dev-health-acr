package contractcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func marshalCanonicalYAML(value any) ([]byte, error) {
	var body strings.Builder
	if err := writeYAML(&body, value, 0); err != nil {
		return nil, err
	}
	content := []byte(body.String())
	digest := sha256.Sum256(content)
	header := fmt.Sprintf(
		"# Generated from acr-v1.json by `go run ./cmd/contractcheck -write`. DO NOT EDIT.\n# canonical-content-sha256: %s\n",
		hex.EncodeToString(digest[:]),
	)
	return append([]byte(header), content...), nil
}

func writeYAML(output *strings.Builder, value any, indent int) error {
	prefix := strings.Repeat(" ", indent)
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			output.WriteString(prefix + "{}\n")
			return nil
		}
		for _, key := range orderedYAMLKeys(typed, indent) {
			child := typed[key]
			output.WriteString(prefix)
			output.WriteString(yamlKey(key))
			output.WriteString(":")
			if yamlScalarValue(child) || emptyContainer(child) {
				output.WriteString(" ")
				output.WriteString(yamlScalar(child))
				output.WriteString("\n")
			} else {
				output.WriteString("\n")
				if err := writeYAML(output, child, indent+2); err != nil {
					return err
				}
			}
		}
	case []any:
		if len(typed) == 0 {
			output.WriteString(prefix + "[]\n")
			return nil
		}
		for _, child := range typed {
			output.WriteString(prefix + "-")
			if yamlScalarValue(child) || emptyContainer(child) {
				output.WriteString(" ")
				output.WriteString(yamlScalar(child))
				output.WriteString("\n")
			} else {
				output.WriteString("\n")
				if err := writeYAML(output, child, indent+2); err != nil {
					return err
				}
			}
		}
	default:
		output.WriteString(prefix)
		output.WriteString(yamlScalar(typed))
		output.WriteString("\n")
	}
	return nil
}

func orderedYAMLKeys(values map[string]any, indent int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if indent != 0 {
		return keys
	}
	preferred := []string{"openapi", "info", "servers", "security", "paths", "components"}
	result := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range preferred {
		if _, exists := values[key]; exists {
			result = append(result, key)
			seen[key] = true
		}
	}
	for _, key := range keys {
		if !seen[key] {
			result = append(result, key)
		}
	}
	return result
}

var safeYAMLKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./{}:-]*$`)

func yamlKey(key string) string {
	if safeYAMLKey.MatchString(key) && !yamlReserved(key) {
		return key
	}
	encoded, _ := json.Marshal(key)
	return string(encoded)
}

func yamlScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case string:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	case []any:
		if len(typed) == 0 {
			return "[]"
		}
	case map[string]any:
		if len(typed) == 0 {
			return "{}"
		}
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func yamlScalarValue(value any) bool {
	switch value.(type) {
	case nil, bool, string, json.Number, float64:
		return true
	default:
		return false
	}
}

func emptyContainer(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func yamlReserved(value string) bool {
	switch strings.ToLower(value) {
	case "null", "true", "false", "yes", "no", "on", "off", "~":
		return true
	default:
		return false
	}
}
