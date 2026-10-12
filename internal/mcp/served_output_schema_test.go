package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestServedOutputSchemasEqualTheCanonicalManifestSchemas(t *testing.T) {
	root := findRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "contracts/mcp/tools.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Tools []struct {
			Name            string `json:"name"`
			OutputSchemaRef string `json:"output_schema_ref"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	h := newDTHosted(t)
	client, closeFn := connectedClient(t, h.boot(t, append([]string{toolGraphQLQuery}, dtAllTools...)...))
	defer closeFn()
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]any{}
	for _, tool := range listed.Tools {
		served[tool.Name] = tool.OutputSchema
	}
	checked := 0
	for _, entry := range manifest.Tools {
		got, ok := served[entry.Name]
		if !ok {
			continue
		}
		canonical, err := os.ReadFile(filepath.Join(root, "contracts/mcp", entry.OutputSchemaRef))
		if err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		var want any
		if err := json.Unmarshal(canonical, &want); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var gotDoc any
		if err := json.Unmarshal(encoded, &gotDoc); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotDoc, want) {
			t.Errorf("%s: tools/list outputSchema differs from %s", entry.Name, entry.OutputSchemaRef)
		}
		checked++
	}
	if checked < 4 {
		t.Fatalf("only %d tools compared; the measurement did not happen", checked)
	}
}
