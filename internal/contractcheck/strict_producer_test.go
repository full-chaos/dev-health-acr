package contractcheck

import (
	"encoding/json"
	"testing"
)

func TestRejectUndeclaredChecksEachAlternativeOnItsOwn(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{
		"type":"object",
		"properties":{"kind":{"type":"string"}},
		"oneOf":[
			{"properties":{"kind":{"const":"a"},"only_a":{"type":"string"}},"required":["kind"]},
			{"properties":{"kind":{"const":"b"},"only_b":{"type":"string"}},"required":["kind"]}
		]}`), &schema); err != nil {
		t.Fatal(err)
	}
	registry := newSchemaRegistry()
	registry.byName["s"] = schema
	decode := func(raw string) any {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if err := registry.rejectUndeclared("s", decode(`{"kind":"a","only_a":"x"}`)); err != nil {
		t.Fatalf("a key of the matching alternative must pass: %v", err)
	}
	if err := registry.rejectUndeclared("s", decode(`{"kind":"b","only_b":"x"}`)); err != nil {
		t.Fatalf("a key of the matching alternative must pass: %v", err)
	}
	if err := registry.rejectUndeclared("s", decode(`{"kind":"b","only_a":"x"}`)); err == nil {
		t.Fatal("a key declared only by the other alternative must be refused")
	}
	if err := registry.rejectUndeclared("s", decode(`{"kind":"a","zz":1}`)); err == nil {
		t.Fatal("an undeclared key must be refused")
	}
}
