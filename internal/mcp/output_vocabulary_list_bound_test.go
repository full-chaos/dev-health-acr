package mcp

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestCachedSchemaAcceptsAFactKindListOfAGrownVocabulary pins that no list of
// an additive vocabulary is capped at that vocabulary's current size: a server
// that adds a fact kind may serve one entry more than a cached schema saw.
func TestCachedSchemaAcceptsAFactKindListOfAGrownVocabulary(t *testing.T) {
	kinds := contractsv1.ContextFabricFactKindVocabulary()
	grown := append([]any{}, vocabSentinel)
	requirements := []any{map[string]any{"kind": vocabSentinel}}
	for _, kind := range kinds {
		grown = append(grown, string(kind))
		requirements = append(requirements, map[string]any{"kind": string(kind)})
	}
	for _, file := range []string{"schemas/mcp_investigation_result_response.v1.schema.json", "schemas/mcp_investigate_question_response.v1.schema.json"} {
		doc := readEmbeddedJSON(t, file)
		defs := doc["$defs"].(map[string]any)
		common := defs["context_fabric_common.v1"].(map[string]any)["$defs"].(map[string]any)
		for _, tc := range []struct {
			name  string
			node  any
			value any
		}{
			{"AnswerPlan.fact_kinds", common["AnswerPlan"].(map[string]any)["properties"].(map[string]any)["fact_kinds"], grown},
			{"PlanRequirement.fact_kinds", common["PlanRequirement"].(map[string]any)["properties"].(map[string]any)["fact_kinds"], grown},
			{"PlanRequirement.input_fact_kinds", common["PlanRequirement"].(map[string]any)["properties"].(map[string]any)["input_fact_kinds"], grown},
			{"InterpretedQuestion.fact_requirements", common["InterpretedQuestion"].(map[string]any)["properties"].(map[string]any)["fact_requirements"], requirements},
		} {
			node := map[string]any{"$defs": defs}
			for k, v := range tc.node.(map[string]any) {
				node[k] = v
			}
			raw, err := json.Marshal(node)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatalf("%s %s: %v", file, tc.name, err)
			}
			if err := resolved.Validate(tc.value); err != nil {
				t.Errorf("%s %s: a cached schema rejects a list of %d entries: %v", file, tc.name, len(kinds)+1, err)
			}
		}
	}
}
