package v1

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

func openAPIReasonEnum(t *testing.T, response string) []any {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	node = openAPIStep(t, node, "components", "responses", response, "content", "application/json", "schema", "allOf")
	list, ok := node.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("%s: allOf = %v, want the error schema and its refinement", response, node)
	}
	node = openAPIStep(t, list[1], "properties", "error", "properties", "details", "properties", "reason", "enum")
	values, ok := node.([]any)
	if !ok {
		t.Fatalf("%s: reason enum = %v", response, node)
	}
	return values
}

func openAPIStep(t *testing.T, node any, keys ...string) any {
	t.Helper()
	for _, key := range keys {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI: %q is not under an object", key)
		}
		if node, ok = object[key]; !ok {
			t.Fatalf("OpenAPI: no %q", key)
		}
	}
	return node
}

func TestCreateInvestigationRefusalsAllowOnlyTheirOwnReason(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ContextFabricInvestigationBadRequest": ContextFabricSuppliedSynthesisReasonInterpretationRequired,
		"ContextFabricInvestigationConflict":   ContextFabricSuppliedSynthesisReasonInputChanged,
	}
	for response, reason := range cases {
		if got := openAPIReasonEnum(t, response); !reflect.DeepEqual(got, []any{reason}) {
			t.Fatalf("%s: reason enum = %v, want only %q", response, got, reason)
		}
	}
}
