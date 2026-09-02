package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The coverage-detail code vocabulary is declared in Go and MIRRORED, by
// hand, into every JSON Schema that describes a coverage detail. Nothing
// checked that the two agreed.
//
// This test exists because that gap silently accepted a real defect while
// this change was being written: a new member was added to the Go closed
// vocabulary, `make contract-write` regenerated every derived artifact and
// `make contract-test` passed clean -- while the schemas still enumerated
// the OLD eleven codes. The engine would have emitted a code its own
// published schema rejects, and the first symptom would have been a
// consumer failing closed on a live answer and reading as a rig fault.
//
// contractcheck validates INSTANCES against schemas; it has no notion of
// "this schema's enum must equal that Go vocabulary", so a hand-mirrored
// enum can drift in exactly the direction that is invisible until
// production. This is the narrow guard for this one vocabulary. The
// general version -- every closed Go vocabulary against every schema enum
// that mirrors it -- is a broader sweep and is recorded as follow-up work
// rather than smuggled in here.

// coverageDetailCodeEnumMarker identifies a coverage-detail code enum
// wherever it appears in a schema document. Keyed on a member that has
// been in the vocabulary since it was introduced, so the marker does not
// have to change every time the vocabulary does.
const coverageDetailCodeEnumMarker = "fact_unconfigured"

// coverageDetailSchemaRoots are every published surface that mirrors the
// vocabulary: the canonical JSON Schema directory and the MCP response
// schemas embedded in the server binary.
var coverageDetailSchemaRoots = []string{
	filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1"),
	filepath.Join("..", "..", "mcp", "schemas"),
}

// expectedCoverageDetailEnumSites is how many distinct enum arrays carry
// this vocabulary today. Pinned so that a schema QUIETLY LOSING its enum
// (or a new schema being added without one) fails here rather than
// reducing this test to a vacuous walk over nothing.
const expectedCoverageDetailEnumSites = 8

func TestCoverageDetailCodeEnumMatchesTheGoVocabularyInEverySchema(t *testing.T) {
	want := make([]string, 0, ContextFabricCoverageDetailCodeCount)
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		want = append(want, string(code))
	}
	sort.Strings(want)

	sites := 0
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
			for _, got := range coverageDetailCodeEnums(document) {
				sites++
				sorted := append([]string(nil), got...)
				sort.Strings(sorted)
				if !equalStringSlices(sorted, want) {
					t.Errorf("%s: coverage detail code enum does not match the Go vocabulary\n schema: %v\n     go: %v\nmissing from schema: %v\nextra in schema: %v",
						path, sorted, want, difference(want, sorted), difference(sorted, want))
				}
			}
		}
	}
	if sites != expectedCoverageDetailEnumSites {
		t.Fatalf("found %d coverage-detail code enum sites, want %d -- a schema gained or lost the enum; update the pin deliberately, never to make this pass", sites, expectedCoverageDetailEnumSites)
	}
}

// coverageDetailCodeEnums walks an arbitrary decoded schema document and
// returns every enum array that is recognisably this vocabulary.
func coverageDetailCodeEnums(node any) [][]string {
	var found [][]string
	switch typed := node.(type) {
	case map[string]any:
		if raw, ok := typed["enum"].([]any); ok {
			members := make([]string, 0, len(raw))
			marker := false
			for _, member := range raw {
				text, isText := member.(string)
				if !isText {
					members = nil
					break
				}
				if text == coverageDetailCodeEnumMarker {
					marker = true
				}
				members = append(members, text)
			}
			if marker && members != nil {
				found = append(found, members)
			}
		}
		for _, child := range typed {
			found = append(found, coverageDetailCodeEnums(child)...)
		}
	case []any:
		for _, child := range typed {
			found = append(found, coverageDetailCodeEnums(child)...)
		}
	}
	return found
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// difference returns members of a that are absent from b.
func difference(a, b []string) []string {
	present := make(map[string]struct{}, len(b))
	for _, value := range b {
		present[value] = struct{}{}
	}
	var only []string
	for _, value := range a {
		if _, ok := present[value]; !ok {
			only = append(only, value)
		}
	}
	return only
}
