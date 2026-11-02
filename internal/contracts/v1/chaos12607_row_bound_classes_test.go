package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// chaos12607_row_bound_classes_test.go holds the published schemas to the one
// keyed row bound table (contextFabricClaimedFactRowBounds). Every claimed or
// projected fact definition with a `rows`/`table` pair carries one allOf
// clause per row bound class and one default clause, each marked with a
// "$comment" of "row-bound-class: <class>", and its `rows` maxItems is the largest cap. A class added
// to the Go table without its clause, or a clause left behind, fails here.

// rowBoundSchemaFiles are the published schemas that carry a claimed or
// projected fact definition.
var rowBoundSchemaFiles = []string{
	"context_fabric_common.v1.schema.json",
	"context_fabric_answer_projection.v1.schema.json",
	"mcp_investigate_question_response.v1.schema.json",
	"mcp_investigation_result_response.v1.schema.json",
}

// rowBoundDefinitions finds every object in a schema whose properties hold the
// CHAOS-4347 `rows` and a `table`: a claimed or projected fact definition.
func rowBoundDefinitions(node any, path string, out map[string]map[string]any) {
	switch value := node.(type) {
	case map[string]any:
		if properties, ok := value["properties"].(map[string]any); ok {
			rows, hasRows := properties["rows"].(map[string]any)
			_, hasTable := properties["table"]
			if hasRows && hasTable && strings.Contains(stringOf(rows["description"]), "CHAOS-4347") {
				out[path] = value
			}
		}
		for key, child := range value {
			rowBoundDefinitions(child, path+"."+key, out)
		}
	case []any:
		for index, child := range value {
			rowBoundDefinitions(child, path+"."+strconv.Itoa(index), out)
		}
	}
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

func TestEveryRowBoundClassHasItsSchemaClause(t *testing.T) {
	classes := ContextFabricClaimedFactRowBoundClasses()
	want := map[string]int{"default": ContextFabricClaimedFactMaxRows}
	largest := ContextFabricClaimedFactMaxRows
	for class, rows := range classes {
		want[string(class)] = rows
		largest = max(largest, rows)
	}
	found := 0
	for _, file := range rowBoundSchemaFiles {
		definitions := map[string]map[string]any{}
		rowBoundDefinitions(loadSchemaDocument(t, file), file, definitions)
		if len(definitions) == 0 {
			t.Errorf("%s: no claimed or projected fact definition found: the walk measured nothing", file)
		}
		for path, definition := range definitions {
			found++
			rows := definition["properties"].(map[string]any)["rows"].(map[string]any)
			if got, _ := rows["maxItems"].(float64); int(got) != largest {
				t.Errorf("%s: rows maxItems = %v, want the largest cap %d", path, rows["maxItems"], largest)
			}
			got := map[string]int{}
			clauses, _ := definition["allOf"].([]any)
			for _, raw := range clauses {
				clause, _ := raw.(map[string]any)
				comment, _ := clause["$comment"].(string)
				class, marked := strings.CutPrefix(comment, "row-bound-class: ")
				if !marked {
					continue
				}
				if _, repeated := got[class]; repeated {
					t.Errorf("%s: row bound class %q has two clauses", path, class)
				}
				then, _ := clause["then"].(map[string]any)
				properties, _ := then["properties"].(map[string]any)
				bound, _ := properties["rows"].(map[string]any)
				maxItems, _ := bound["maxItems"].(float64)
				got[class] = int(maxItems)
			}
			if !equalBounds(got, want) {
				t.Errorf("%s: row bound clauses %v, want %v (the Go table)", path, got, want)
			}
		}
	}
	if found < len(rowBoundSchemaFiles) {
		t.Fatalf("found %d definitions across %d schemas", found, len(rowBoundSchemaFiles))
	}
}

func equalBounds(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for key := range a {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if b[key] != a[key] {
			return false
		}
	}
	return true
}

// TestThePublishedClaimedFactSchemaHoldsEachRowBound validates claimed facts
// against the published common schema itself: each class admits its cap and
// refuses one more, and a table in no class, and rows with no declaration,
// are refused at 65.
func TestThePublishedClaimedFactSchemaHoldsEachRowBound(t *testing.T) {
	common, err := filepath.Abs(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(common); err != nil {
		t.Fatal(err)
	}
	loader := gojsonschema.NewSchemaLoader()
	if err := loader.AddSchemas(gojsonschema.NewReferenceLoader("file://" + common)); err != nil {
		t.Fatalf("add common schema: %v", err)
	}
	schema, err := loader.Compile(gojsonschema.NewGoLoader(map[string]any{"$ref": "https://contracts.fullchaos.dev/acr/v1/context_fabric_common.v1.schema.json#/$defs/ClaimedFact"}))
	if err != nil {
		t.Fatalf("compile ClaimedFact: %v", err)
	}
	valid := func(fact ContextFabricClaimedFact) bool {
		t.Helper()
		encoded, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		report, err := schema.Validate(gojsonschema.NewBytesLoader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		return report.Valid()
	}
	shapes := map[ContextFabricClaimedFactRowBoundClass]ContextFabricFactTableShape{
		ContextFabricClaimedFactRowBoundDistribution: ContextFabricFactTableShapeDistribution,
	}
	for class, rows := range ContextFabricClaimedFactRowBoundClasses() {
		shape, ok := shapes[class]
		if !ok {
			t.Fatalf("row bound class %q has no example declaration here: add one", class)
		}
		if !valid(claimedFactWithTable(shape, distributionRows(rows))) {
			t.Errorf("class %s: %d rows refused by the schema", class, rows)
		}
		if valid(claimedFactWithTable(shape, distributionRows(rows+1))) {
			t.Errorf("class %s: %d rows admitted by the schema", class, rows+1)
		}
	}
	if !valid(claimedFactWithTable(ContextFabricFactTableShapeBreakdown, distributionRows(ContextFabricClaimedFactMaxRows))) {
		t.Errorf("a breakdown at %d rows refused by the schema", ContextFabricClaimedFactMaxRows)
	}
	if valid(claimedFactWithTable(ContextFabricFactTableShapeBreakdown, distributionRows(ContextFabricClaimedFactMaxRows+1))) {
		t.Errorf("a breakdown at %d rows admitted by the schema", ContextFabricClaimedFactMaxRows+1)
	}
	undeclared := claimedFactWithTable(ContextFabricFactTableShapeDistribution, distributionRows(ContextFabricClaimedFactMaxRows+1))
	undeclared.Table = nil
	if valid(undeclared) {
		t.Errorf("%d undeclared rows admitted by the schema", ContextFabricClaimedFactMaxRows+1)
	}
}
