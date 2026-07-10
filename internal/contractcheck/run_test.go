package contractcheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryContracts(t *testing.T) {
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(Options{Root: root, Out: &output}); err != nil {
		t.Fatalf("contract validation failed: %v\n%s", err, output.String())
	}
}

func TestValidatorRejectsObservedItemWithoutEvidence(t *testing.T) {
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.loadSchemas(); err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSONFile(filepath.Join(root, "contracts", "examples", "v1", "context_packet_item.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	item := value.(map[string]any)
	item["claim_kind"] = "observed"
	item["evidence_ref_ids"] = []any{}
	if err := check.registry.validate("context_packet_item.v1.schema.json", item); err == nil {
		t.Fatal("expected observed item without evidence to fail")
	}
}

func TestCanonicalYAMLIsDeterministicAndStored(t *testing.T) {
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSONFile(filepath.Join(root, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := marshalCanonicalYAML(value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalCanonicalYAML(value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("canonical YAML generation is not deterministic")
	}
	stored, err := os.ReadFile(filepath.Join(root, "contracts", "openapi", "acr-v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, stored) {
		t.Fatal("stored OpenAPI YAML is stale")
	}
}

func TestJSONSchemaProfileRejectsUnsupportedKeyword(t *testing.T) {
	registry := newSchemaRegistry()
	schema := map[string]any{
		"$schema": draft202012,
		"$id":     "test.schema.json",
		"type":    "object",
		"contains": map[string]any{
			"type": "string",
		},
	}
	if err := registry.add("test.schema.json", schema); err == nil {
		t.Fatal("expected unsupported keyword to fail closed")
	}
}

func TestJSONNumberEquality(t *testing.T) {
	if !jsonEqual(json.Number("1"), json.Number("1.0")) {
		t.Fatal("JSON numbers with the same value should compare equal")
	}
}
