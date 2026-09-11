package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestEveryPublishedRefusalBasisEnumIsTheGoVocabulary sweeps EVERY published
// schema -- the canonical documents and the MCP embedded copies -- for every
// `refusal_basis` property, and holds each one's enum equal to the Go
// vocabulary.
//
// FOUND, NOT LISTED. The sites are discovered by walking every document, and
// the count found is checked against a raw-text count of the property key in
// the same file, so a site whose shape the walker does not understand (no
// enum, a $ref, an inlined variant) fails here rather than being skipped. A
// member added to Go and missed in one copy fails; a copy that adds a member
// Go does not have fails.
func TestEveryPublishedRefusalBasisEnumIsTheGoVocabulary(t *testing.T) {
	t.Parallel()
	vocabulary := ContextFabricRefusalBasisVocabulary()
	want := make([]string, 0, len(vocabulary))
	for _, member := range vocabulary {
		want = append(want, string(member))
	}
	sort.Strings(want)

	root := moduleRootForParity(t)
	var paths []string
	for _, pattern := range []string{
		filepath.Join(root, "contracts", "jsonschema", "v1", "*.json"),
		filepath.Join(root, "internal", "mcp", "schemas", "*.json"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		t.Fatal("no schema documents found -- the sweep would pass vacuously")
	}

	sites, documentsWithSites := 0, 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rawCount := strings.Count(string(raw), `"refusal_basis":`)
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		found := 0
		walkRefusalBasisProperties(document, "", func(pointer string, node map[string]any) {
			found++
			got := schemaEnumValues(node)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s %s publishes refusal_basis %v, want the Go vocabulary %v", filepath.Base(path), pointer, got, want)
			}
		})
		if found != rawCount {
			t.Errorf("%s: %d refusal_basis key(s) in the text, %d understood as enum properties -- a site in a shape this sweep cannot read is an unchecked site", filepath.Base(path), rawCount, found)
		}
		if found > 0 {
			documentsWithSites++
		}
		sites += found
	}
	if sites == 0 {
		t.Fatal("no refusal_basis property found in any published schema -- the sweep is vacuous")
	}
	t.Logf("refusal_basis enum sites swept: %d across %d documents (of %d read)", sites, documentsWithSites, len(paths))
}

// walkRefusalBasisProperties visits every object that is the VALUE of a
// `refusal_basis` key.
func walkRefusalBasisProperties(node any, pointer string, visit func(string, map[string]any)) {
	switch typed := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := typed[key]
			if key == "refusal_basis" {
				if object, ok := child.(map[string]any); ok {
					visit(pointer+"/"+key, object)
				}
			}
			walkRefusalBasisProperties(child, pointer+"/"+key, visit)
		}
	case []any:
		for i, child := range typed {
			walkRefusalBasisProperties(child, pointer+"/"+strconv.Itoa(i), visit)
		}
	}
}
