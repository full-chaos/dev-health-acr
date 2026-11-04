package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// CHAOS-12726: Go rejects scope_count_at_least without scope_count, so every
// published copy of the summary says so too (if/then).
func TestEverySchemaRequiresTheCountBesideItsFloor(t *testing.T) {
	files := []string{
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json"),
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_answer_projection.v1.schema.json"),
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "mcp_investigate_question_response.v1.schema.json"),
		filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "mcp_investigation_result_response.v1.schema.json"),
		filepath.Join("..", "..", "mcp", "schemas", "mcp_investigate_question_response.v1.schema.json"),
		filepath.Join("..", "..", "mcp", "schemas", "mcp_investigation_result_response.v1.schema.json"),
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		found := 0
		var walk func(node any)
		walk = func(node any) {
			switch value := node.(type) {
			case map[string]any:
				if properties, ok := value["properties"].(map[string]any); ok {
					_, floor := properties["scope_count_at_least"]
					_, count := properties["scope_count"]
					if floor && count {
						found++
						condition, _ := value["if"].(map[string]any)
						consequence, _ := value["then"].(map[string]any)
						floor, _ := condition["properties"].(map[string]any)["scope_count_at_least"].(map[string]any)
						when, _ := condition["required"].([]any)
						needs, _ := consequence["required"].([]any)
						if floor["const"] != true || len(when) != 1 || when[0] != "scope_count_at_least" || len(needs) != 1 || needs[0] != "scope_count" {
							t.Errorf("%s: a summary definition states scope_count_at_least: true without requiring scope_count (if = %v, then = %v)", file, condition, consequence)
						}
					}
				}
				for _, child := range value {
					walk(child)
				}
			case []any:
				for _, child := range value {
					walk(child)
				}
			}
		}
		walk(document)
		if found == 0 {
			t.Errorf("%s: no summary definition carries scope_count_at_least", file)
		}
	}
}

func TestAFloorWithoutACountIsRefusedByTheValidator(t *testing.T) {
	summary := ContextFabricClaimedCompletionForecast{State: ContextFabricCompletionDistributionAvailable, ComputedAt: "2026-11-02T04:00:00Z", ScopeCountAtLeast: true}
	if err := summary.validate(); err == nil {
		t.Fatal("validate() = nil, want a refusal: a floor needs its count")
	}
	one := int64(1)
	summary.ScopeCount = &one
	if err := summary.validate(); err != nil {
		t.Fatalf("validate() = %v, want valid with a count", err)
	}
	zero := int64(0)
	summary.ScopeCount = &zero
	if err := summary.validate(); err == nil {
		t.Fatal("validate() = nil, want a refusal of scope_count 0")
	}
}
