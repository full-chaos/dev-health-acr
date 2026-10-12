package v1

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The published result schema must refuse the derivation shapes the Go
// validator refuses, and accept the ones it accepts.
func TestPublishedResultSchemaAgreesWithTheGoValidatorOnDerivedClaimShapes(t *testing.T) {
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", "context_fabric_investigation_result.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	input := func(id, field string) map[string]any { return map[string]any{"fact_id": id, "field": field} }
	two := []any{input("fact_aaaaaaaa", "open_items"), input("fact_bbbbbbbb", "closed_items")}
	three := append(append([]any{}, two...), input("fact_cccccccc", "other_items"))
	cases := []struct {
		name   string
		fields map[string]any
		valid  bool
	}{
		{"sum of two", map[string]any{"derived_from": two, "operation": "sum", "value": map[string]any{"integer": 10}}, true},
		{"sum of three", map[string]any{"derived_from": three, "operation": "sum", "value": map[string]any{"number": 10.5}}, true},
		{"same input twice", map[string]any{"derived_from": []any{input("fact_aaaaaaaa", "open_items"), input("fact_aaaaaaaa", "open_items")}, "operation": "difference", "value": map[string]any{"integer": 0}}, true},
		{"inputs without an operation", map[string]any{"derived_from": two, "value": map[string]any{"integer": 10}}, false},
		{"operation without inputs", map[string]any{"operation": "sum", "value": map[string]any{"integer": 10}}, false},
		{"fact_id beside inputs", map[string]any{"derived_from": two, "operation": "sum", "fact_id": "fact_aaaaaaaa", "value": map[string]any{"integer": 10}}, false},
		{"string value", map[string]any{"derived_from": two, "operation": "sum", "value": map[string]any{"string": "10"}}, false},
		{"ratio of three", map[string]any{"derived_from": three, "operation": "ratio", "value": map[string]any{"number": 1.5}}, false},
		{"difference of three", map[string]any{"derived_from": three, "operation": "difference", "value": map[string]any{"integer": 1}}, false},
		{"unknown operation", map[string]any{"derived_from": two, "operation": "product", "value": map[string]any{"integer": 10}}, false},
		{"one input", map[string]any{"derived_from": two[:1], "operation": "sum", "value": map[string]any{"integer": 10}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			claims := doc["claimed_facts"].([]any)
			claim := claims[0].(map[string]any)
			for _, key := range []string{"rows", "table", "fact_id", "time_series_rows", "time_series_table"} {
				delete(claim, key)
			}
			claim["field"] = "derived_value"
			for key, value := range tc.fields {
				claim[key] = value
			}
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			schemaErr := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded)
			var goClaim ContextFabricClaimedFact
			claimBytes, _ := json.Marshal(claim)
			if err := json.Unmarshal(claimBytes, &goClaim); err != nil {
				t.Fatal(err)
			}
			goErr := goClaim.Validate()
			if (schemaErr == nil) != tc.valid {
				t.Fatalf("schema verdict = %v, want valid=%v", schemaErr, tc.valid)
			}
			if (goErr == nil) != tc.valid {
				t.Fatalf("go verdict = %v, want valid=%v", goErr, tc.valid)
			}
		})
	}
}
