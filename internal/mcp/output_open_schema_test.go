package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// closedOutputObjects lists the output-schema objects allowed to keep
// additionalProperties:false, keyed "<file>#<json pointer>" with the reason the
// object can never gain a field. A client that cached tools/list rejects any
// field added later to a closed object, so the list stays empty unless a
// fixed protocol envelope truly qualifies.
var closedOutputObjects = map[string]string{}

func outputResponseSchemaFiles(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(repopath.Path(t, "contracts", "jsonschema", "v1", "mcp_*_response.v1.schema.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no MCP response schemas found: %v", err)
	}
	return paths
}

func closedObjectPointers(node any, pointer string, out *[]string) {
	switch value := node.(type) {
	case map[string]any:
		if closed, ok := value["additionalProperties"].(bool); ok && !closed {
			*out = append(*out, pointer)
		}
		for key, child := range value {
			closedObjectPointers(child, pointer+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(key), out)
		}
	case []any:
		for i, child := range value {
			closedObjectPointers(child, fmt.Sprintf("%s/%d", pointer, i), out)
		}
	}
}

func TestEveryOutputObjectIsOpenUnlessListedClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, path := range outputResponseSchemaFiles(t) {
		base := filepath.Base(path)
		for _, copyName := range []string{path, ""} {
			var raw []byte
			var err error
			if copyName == "" {
				raw, err = schemaFiles.ReadFile("schemas/" + base)
			} else {
				raw, err = os.ReadFile(copyName)
			}
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatalf("%s: %v", base, err)
			}
			var pointers []string
			closedObjectPointers(document, "", &pointers)
			sort.Strings(pointers)
			for _, pointer := range pointers {
				key := base + "#" + pointer
				seen[key] = true
				if closedOutputObjects[key] == "" {
					t.Errorf("%s: output object %q sets additionalProperties:false; a client that cached tools/list would reject a field added later. Drop it, or list it in closedOutputObjects with the reason it can never grow", base, pointer)
				}
			}
		}
	}
	for key := range closedOutputObjects {
		if !seen[key] {
			t.Errorf("closedOutputObjects lists %s, which no longer exists", key)
		}
	}
}

func cachedOutputSchema(t *testing.T, raw []byte) *jsonschema.Resolved {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

type nestedObject struct {
	path   []string
	object map[string]any
}

func nestedObjects(node any, path []string, out *[]nestedObject) {
	switch value := node.(type) {
	case map[string]any:
		if len(path) > 0 {
			*out = append(*out, nestedObject{append([]string(nil), path...), value})
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			nestedObjects(value[key], append(path, key), out)
		}
	case []any:
		for i, child := range value {
			nestedObjects(child, append(path, fmt.Sprint(i)), out)
		}
	}
}

// A client holding the output schema as served before this change rejects a
// payload with one added field; the schema served now admits it, at the root
// and in a nested object, for every MCP tool that has a golden response.
func TestCachedOutputSchemaOfMainRejectsAddedFieldAndCurrentAdmitsIt(t *testing.T) {
	examples := map[string]string{}
	for _, path := range outputResponseSchemaFiles(t) {
		tool := strings.TrimSuffix(filepath.Base(path), ".v1.schema.json")
		examples[tool] = filepath.Base(path)
	}
	pairs := map[string]string{
		"mcp_context_for_task_response":     "mcp_context_for_task_response.v1.json",
		"mcp_data_catalog_response":         "mcp_data_catalog_response.v1.json",
		"mcp_find_subjects_response":        "mcp_find_subjects_response.v1.json",
		"mcp_graphql_query_response":        "mcp_graphql_query_response.v1.json",
		"mcp_investigate_question_response": "mcp_investigate_question_response.v1.json",
		"mcp_investigation_result_response": "mcp_investigation_result_response.v1.json",
		"mcp_read_facts_response":           "mcp_read_facts_response.v1.json",
		"mcp_read_relationships_response":   "mcp_read_relationships_response.v1.json",
		"mcp_record_episode_response":       "mcp_record_episode_response.v1.json",
		"mcp_run_operation_response":        "mcp_run_operation_response.v1.json",
		"mcp_source_evidence_response":      "mcp_source_evidence_response.v1.json",
	}
	if len(pairs) != len(examples) {
		t.Fatalf("example table covers %d response schemas, %d exist", len(pairs), len(examples))
	}
	for tool, example := range pairs {
		t.Run(tool, func(t *testing.T) {
			schemaFile := tool + ".v1.schema.json"
			cachedRaw, err := os.ReadFile(filepath.Join("testdata", "cached_output_schemas_main", schemaFile))
			if err != nil {
				t.Fatal(err)
			}
			currentRaw, err := schemaFiles.ReadFile("schemas/" + schemaFile)
			if err != nil {
				t.Fatal(err)
			}
			cached, current := cachedOutputSchema(t, cachedRaw), cachedOutputSchema(t, currentRaw)

			// The payload is frozen with the cached schema: a contract change
			// may show a new field in the live example without breaking the
			// simulation of a client that cached the old schema.
			exampleRaw, err := os.ReadFile(filepath.Join("testdata", "cached_output_payloads_main", example))
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			if err := json.Unmarshal(exampleRaw, &root); err != nil {
				t.Fatal(err)
			}
			if err := cached.Validate(root); err != nil {
				t.Fatalf("cached schema of main rejects its frozen payload: %v", err)
			}

			root["added_by_a_newer_server"] = "x"
			if cached.Validate(root) == nil {
				t.Fatal("cached schema of main admits a root field: the simulation does not model the defect")
			}
			if err := current.Validate(root); err != nil {
				t.Fatalf("current schema rejects an added root field: %v", err)
			}
			delete(root, "added_by_a_newer_server")

			var objects []nestedObject
			nestedObjects(root, nil, &objects)
			for _, candidate := range objects {
				candidate.object["added_by_a_newer_server"] = "x"
				rejectedByCache := cached.Validate(root) != nil
				currentErr := current.Validate(root)
				delete(candidate.object, "added_by_a_newer_server")
				if !rejectedByCache {
					continue
				}
				if currentErr != nil {
					t.Fatalf("current schema rejects a field added at %s: %v", strings.Join(candidate.path, "."), currentErr)
				}
				return
			}
			t.Fatal("no nested object of the example is closed in the cached schema: the nested simulation row did not run")
		})
	}
}

// The producer stays strict: the offline check refuses a served key that no
// schema branch declares, at the root and in a nested object.
func TestStrictProducerCheckRefusesAnUndeclaredServedKey(t *testing.T) {
	exampleRaw, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", "mcp_read_facts_response.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := contractcheck.ValidateSerializedStrict("", "mcp_read_facts_response.v1.schema.json", exampleRaw); err != nil {
		t.Fatalf("unchanged example: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(exampleRaw, &root); err != nil {
		t.Fatal(err)
	}
	root["undeclared_key"] = true
	planted, _ := json.Marshal(root)
	if err := contractcheck.ValidateSerialized("", "mcp_read_facts_response.v1.schema.json", planted); err != nil {
		t.Fatalf("consumer check must admit an added key: %v", err)
	}
	if err := contractcheck.ValidateSerializedStrict("", "mcp_read_facts_response.v1.schema.json", planted); err == nil {
		t.Fatal("producer check must refuse an undeclared root key")
	}
	delete(root, "undeclared_key")
	var objects []nestedObject
	nestedObjects(root, nil, &objects)
	refused := 0
	for _, candidate := range objects {
		candidate.object["undeclared_key"] = true
		planted, _ := json.Marshal(root)
		if contractcheck.ValidateSerializedStrict("", "mcp_read_facts_response.v1.schema.json", planted) != nil {
			refused++
		}
		delete(candidate.object, "undeclared_key")
	}
	if refused == 0 {
		t.Fatal("producer check refused no undeclared nested key")
	}
}
