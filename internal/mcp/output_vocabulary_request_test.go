package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// TestNoAdditiveVocabularyIsReachableFromARequest pins that every vocabulary a
// request can carry stays frozen: Go request validation is closed, so a schema
// node a request reaches cannot be open.
func TestNoAdditiveVocabularyIsReachableFromARequest(t *testing.T) {
	dir := repopath.Path(t, "contracts", "jsonschema", "v1")
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]any{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		docs[filepath.Base(path)] = doc
	}
	embedded, err := schemaFiles.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for name := range docs {
		if strings.Contains(name, "_request.") {
			roots = append(roots, name)
		}
	}
	for _, entry := range embedded {
		if !strings.Contains(entry.Name(), "_request.") {
			continue
		}
		key := "embedded:" + entry.Name()
		docs[key] = readEmbeddedJSON(t, "schemas/"+entry.Name())
		roots = append(roots, key)
	}
	if len(roots) < 20 {
		t.Fatalf("only %d request schemas found; the measurement did not happen", len(roots))
	}
	seen := map[string]bool{}
	visited := 0
	var walk func(file string, node any, pointer string)
	walk = func(file string, node any, pointer string) {
		id := file + "#" + pointer
		if seen[id] {
			return
		}
		seen[id] = true
		visited++
		switch n := node.(type) {
		case map[string]any:
			if _, open := n["x-documented-values"]; open {
				t.Errorf("%s: an additive vocabulary is reachable from a request schema; freeze it", id)
			}
			for key, child := range n {
				switch key {
				case "$defs", "definitions":
					continue
				case "$ref":
					ref, _ := child.(string)
					target, fragment, _ := strings.Cut(ref, "#")
					targetFile := file
					if target != "" {
						targetFile = filepath.Base(target)
					}
					doc, ok := docs[targetFile]
					if !ok {
						t.Errorf("%s: $ref %q names no known schema", id, ref)
						continue
					}
					resolved := doc
					for _, part := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
						if part == "" {
							continue
						}
						switch r := resolved.(type) {
						case map[string]any:
							resolved = r[part]
						case []any:
							i, _ := strconv.Atoi(part)
							resolved = r[i]
						}
					}
					if resolved == nil {
						t.Errorf("%s: $ref %q does not resolve", id, ref)
						continue
					}
					walk(targetFile, resolved, fragment)
				default:
					walk(file, child, pointer+"/"+key)
				}
			}
		case []any:
			for i, child := range n {
				walk(file, child, pointer+"/"+strconv.Itoa(i))
			}
		}
	}
	for _, r := range roots {
		walk(r, docs[r], "")
	}
	if visited < 500 {
		t.Fatalf("visited only %d schema nodes; the measurement did not happen", visited)
	}
}
