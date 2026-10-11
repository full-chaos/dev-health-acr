package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// expectedFailureEnumSites is how many property definitions carry each of the
// two failure enums across the published and the embedded schemas. Pinned so a
// schema quietly losing the property cannot reduce the walk to nothing.
const expectedFailureEnumSites = 8

// failurePropertyEnums returns the enum of every property named name that is
// a string with an enum, anywhere in a decoded schema document.
func failurePropertyEnums(node any, name string) [][]string {
	var found [][]string
	switch typed := node.(type) {
	case map[string]any:
		if properties, ok := typed["properties"].(map[string]any); ok {
			if property, ok := properties[name].(map[string]any); ok {
				if raw, ok := property["enum"].([]any); ok {
					members := make([]string, 0, len(raw))
					for _, member := range raw {
						if text, isText := member.(string); isText {
							members = append(members, text)
						}
					}
					found = append(found, members)
				}
			}
		}
		for _, child := range typed {
			found = append(found, failurePropertyEnums(child, name)...)
		}
	case []any:
		for _, child := range typed {
			found = append(found, failurePropertyEnums(child, name)...)
		}
	}
	return found
}

func TestFailureClassAndRetryHintEnumsMatchTheGoVocabularyInEverySchema(t *testing.T) {
	classes := make([]string, 0, len(contextFabricSynthesisFailureClasses))
	for _, class := range ContextFabricSynthesisFailureClasses() {
		classes = append(classes, string(class))
	}
	hints := make([]string, 0, len(contextFabricRetryHints))
	for _, hint := range contextFabricRetryHints {
		hints = append(hints, string(hint))
	}
	sort.Strings(classes)
	sort.Strings(hints)
	want := map[string][]string{"failure_class": classes, "retry_hint": hints}
	sites := map[string]int{}
	for _, root := range coverageDetailSchemaRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read schema root %s: %v", root, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(root, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var document any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for name, members := range want {
				for _, got := range failurePropertyEnums(document, name) {
					sites[name]++
					sorted := append([]string(nil), got...)
					sort.Strings(sorted)
					if !equalStringSlices(sorted, members) {
						t.Errorf("%s: %s enum does not match the Go vocabulary\n schema: %v\n     go: %v", path, name, sorted, members)
					}
				}
			}
		}
	}
	for name := range want {
		if sites[name] != expectedFailureEnumSites {
			t.Fatalf("found %d %s enum sites, want %d -- a schema gained or lost the property; update the pin deliberately", sites[name], name, expectedFailureEnumSites)
		}
	}
}
