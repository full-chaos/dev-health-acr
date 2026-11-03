package v1

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// chaos12608_render_point_bounds_test.go holds the published schemas to the
// one keyed table of per-kind point caps (contextFabricRenderPointBounds):
// every render shape definition carries one clause per kind with its own cap
// plus one default clause, each marked with a "$comment" of
// "render-point-bound-kind: <kind>", and a series admits the largest cap.

// renderShapeDefinitions finds every object whose properties hold a
// `selected_by` naming the RenderShapeRule definition: a render shape.
func renderShapeDefinitions(node any, path string, out map[string]map[string]any) {
	switch value := node.(type) {
	case map[string]any:
		if properties, ok := value["properties"].(map[string]any); ok {
			if selectedBy, ok := properties["selected_by"].(map[string]any); ok && strings.HasSuffix(stringOf(selectedBy["$ref"]), "RenderShapeRule") {
				out[path] = value
			}
		}
		for key, child := range value {
			renderShapeDefinitions(child, path+"."+key, out)
		}
	case []any:
		for index, child := range value {
			renderShapeDefinitions(child, path+"."+strconv.Itoa(index), out)
		}
	}
}

func TestEveryRenderPointBoundKindHasItsSchemaClause(t *testing.T) {
	want := map[string]int{"default": ContextFabricRenderPointsMaxCount}
	for kind, points := range ContextFabricRenderPointBoundKinds() {
		want[string(kind)] = points
	}
	found := 0
	for _, file := range rowBoundSchemaFiles {
		definitions := map[string]map[string]any{}
		renderShapeDefinitions(loadSchemaDocument(t, file), file, definitions)
		if len(definitions) == 0 {
			t.Errorf("%s: no render shape definition found: the walk measured nothing", file)
		}
		for path, definition := range definitions {
			found++
			got := map[string]int{}
			clauses, _ := definition["allOf"].([]any)
			for _, raw := range clauses {
				clause, _ := raw.(map[string]any)
				comment, _ := clause["$comment"].(string)
				kind, marked := strings.CutPrefix(comment, "render-point-bound-kind: ")
				if !marked {
					continue
				}
				if _, repeated := got[kind]; repeated {
					t.Errorf("%s: kind %q has two clauses", path, kind)
				}
				var node any = clause["then"]
				for _, key := range []string{"properties", "series", "items", "properties", "points", "maxItems"} {
					object, _ := node.(map[string]any)
					node = object[key]
				}
				maxItems, _ := node.(float64)
				got[kind] = int(maxItems)
			}
			if !equalBounds(got, want) {
				t.Errorf("%s: point bound clauses %v, want %v (the Go table)", path, got, want)
			}
		}
	}
	if found < len(rowBoundSchemaFiles) {
		t.Fatalf("found %d render shape definitions across %d schemas", found, len(rowBoundSchemaFiles))
	}
}

// TestThePublishedRenderShapeSchemaHoldsEachPointBound validates shapes against
// the published common schema itself: a forecast admits its cap and refuses one
// more, and every other kind is refused at 65 points.
func TestThePublishedRenderShapeSchemaHoldsEachPointBound(t *testing.T) {
	loader := gojsonschema.NewSchemaLoader()
	common := loadSchemaDocument(t, "context_fabric_common.v1.schema.json")
	if err := loader.AddSchemas(gojsonschema.NewGoLoader(common)); err != nil {
		t.Fatalf("add common schema: %v", err)
	}
	schema, err := loader.Compile(gojsonschema.NewGoLoader(map[string]any{"$ref": "https://contracts.fullchaos.dev/acr/v1/context_fabric_common.v1.schema.json#/$defs/RenderShape"}))
	if err != nil {
		t.Fatalf("compile RenderShape: %v", err)
	}
	shape := func(kind ContextFabricRenderKind, rule ContextFabricRenderShapeRule, presentation ContextFabricRenderPresentation, points int) bool {
		t.Helper()
		series := ContextFabricRenderSeries{Key: "cumulative_share", Label: "Share"}
		for index := 0; index < points; index++ {
			row := index
			series.Points = append(series.Points, ContextFabricRenderPoint{
				Label: strconv.Itoa(index), Value: 0.5,
				Source: ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceClaimedFactTableRow, ClaimID: "claim_forecast", RowIndex: &row, Field: "cumulative_share"},
			})
		}
		encoded, err := json.Marshal(ContextFabricRenderShape{
			ShapeID: "rs_1", Kind: kind, Presentation: presentation, SelectedBy: rule, Title: "T", AxisKind: ContextFabricRenderAxisCategory,
			AxisLabel: "A", ValueLabel: "V", Series: []ContextFabricRenderSeries{series},
		})
		if err != nil {
			t.Fatal(err)
		}
		report, err := schema.Validate(gojsonschema.NewBytesLoader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		return report.Valid()
	}
	forecastCap := ContextFabricRenderPointsMax(ContextFabricRenderKindForecast)
	if !shape(ContextFabricRenderKindForecast, ContextFabricRenderRuleCompletionForecast, "", forecastCap) {
		t.Errorf("a forecast of %d points refused by the schema", forecastCap)
	}
	if shape(ContextFabricRenderKindForecast, ContextFabricRenderRuleCompletionForecast, "", forecastCap+1) {
		t.Errorf("a forecast of %d points admitted by the schema", forecastCap+1)
	}
	if !shape(ContextFabricRenderKindSeries, ContextFabricRenderRuleDatedFactTrend, ContextFabricRenderPresentationLine, ContextFabricRenderPointsMaxCount) {
		t.Errorf("a series of %d points refused by the schema", ContextFabricRenderPointsMaxCount)
	}
	if shape(ContextFabricRenderKindSeries, ContextFabricRenderRuleDatedFactTrend, ContextFabricRenderPresentationLine, ContextFabricRenderPointsMaxCount+1) {
		t.Errorf("a series of %d points admitted by the schema", ContextFabricRenderPointsMaxCount+1)
	}
}
