package eventspec

import (
	"encoding/json"
	"os"
	"testing"
)

// TestEveryDeclaredBoundReachesTheGeneratedSchema requires the generated JSON
// declaration to carry the same bound the Go declaration states: a slice field
// that declares MaxItems must appear in schema.json with max_items equal to it,
// and a field that declares none must not carry one.
func TestEveryDeclaredBoundReachesTheGeneratedSchema(t *testing.T) {
	raw, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatalf("read schema.json: %v", err)
	}
	type schemaField struct {
		Key      string        `json:"key"`
		MaxItems int           `json:"max_items"`
		Fields   []schemaField `json:"fields"`
	}
	var schema []struct {
		ID     string        `json:"id"`
		Fields []schemaField `json:"fields"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse schema.json: %v", err)
	}
	byID := map[string][]schemaField{}
	for _, e := range schema {
		byID[e.ID] = e.Fields
	}
	bounded := 0
	var walk func(event string, declared []Field, generated []schemaField)
	walk = func(event string, declared []Field, generated []schemaField) {
		if len(declared) != len(generated) {
			t.Fatalf("%s: %d declared fields, %d generated", event, len(declared), len(generated))
		}
		for i, field := range declared {
			if field.MaxItems > 0 {
				bounded++
			}
			if generated[i].Key != field.Key || generated[i].MaxItems != field.MaxItems {
				t.Errorf("%s.%s: declared MaxItems=%d, generated schema max_items=%d", event, field.Key, field.MaxItems, generated[i].MaxItems)
			}
			walk(event+"."+field.Key, field.Fields, generated[i].Fields)
		}
	}
	for _, event := range All {
		generated, ok := byID[event.ID]
		if !ok {
			t.Fatalf("schema.json has no entry for %s", event.ID)
		}
		walk(event.ID, event.Fields, generated)
	}
	if bounded == 0 {
		t.Fatal("no declared field carries MaxItems, so this test proved nothing")
	}
}
