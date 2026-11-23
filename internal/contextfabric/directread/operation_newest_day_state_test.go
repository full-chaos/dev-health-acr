package directread_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// opAnswerForDocument builds the ops reply for a registered document: one value for every field the
// document selects, typed from the ops SDL, so the reply holds exactly what the producer's document
// asks for. NewestDayState is answered STALE and every day with a day before the request window.
func opAnswerForDocument(t *testing.T, document string) map[string]any {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "ops schema", Input: string(directread.EmbeddedOpsSchema())})
	if err != nil {
		t.Fatalf("load the ops SDL: %v", err)
	}
	doc, errs := gqlparser.LoadQuery(schema, document)
	if len(errs) > 0 || len(doc.Operations) != 1 {
		t.Fatalf("document does not validate: %v", errs)
	}
	var fill func(set ast.SelectionSet) map[string]any
	value := func(typ *ast.Type, sub ast.SelectionSet) any {
		var leaf func() any
		leaf = func() any {
			def := schema.Types[typ.Name()]
			switch {
			case len(sub) > 0:
				return fill(sub)
			case def != nil && def.Kind == ast.Enum && def.Name == "NewestDayState":
				return "STALE"
			case def != nil && def.Kind == ast.Enum:
				return def.EnumValues[0].Name
			}
			switch typ.Name() {
			case "Int":
				return 3
			case "Float":
				return 0.5
			case "Boolean":
				return false
			case "Date":
				return "2026-10-01"
			case "DateTime":
				return "2026-10-01T00:00:00Z"
			}
			return "x"
		}
		if typ.Elem != nil {
			return []any{leaf()}
		}
		return leaf()
	}
	fill = func(set ast.SelectionSet) map[string]any {
		out := map[string]any{}
		for _, sel := range set {
			f, ok := sel.(*ast.Field)
			if !ok || f.Name == "__typename" {
				continue
			}
			out[f.Alias] = value(f.Definition.Type, f.SelectionSet)
		}
		return out
	}
	return fill(doc.Operations[0].SelectionSet)
}

func opPathValue(root any, path string) (any, bool) {
	cur := []any{root}
	for _, seg := range strings.Split(path, ".") {
		name, array := strings.CutSuffix(seg, "[*]")
		var next []any
		for _, c := range cur {
			obj, ok := c.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := obj[name]
			if !ok {
				return nil, false
			}
			if array {
				items, ok := v.([]any)
				if !ok || len(items) == 0 {
					return nil, false
				}
				next = append(next, items...)
			} else {
				next = append(next, v)
			}
		}
		cur = next
	}
	return cur[0], true
}

// The read-state fields ops added to the forecast documents reach the answer acr serves, and a STALE
// newest day stays STALE with its day and key count: the answer never turns it into a MEASURED, a null or a 0.
func TestForecastReadStateFieldsReachTheServedAnswerAsStale(t *testing.T) {
	for _, tc := range []struct {
		operation string
		states    []string
		days      []string
		counts    []string
	}{
		{"capacityForecast", []string{"capacityForecast.backlogState"}, []string{"capacityForecast.backlogAsOfDay"}, nil},
		{"capacityForecasts", []string{"capacityForecasts.edges[*].node.backlogState"}, []string{"capacityForecasts.edges[*].node.backlogAsOfDay"}, nil},
		{"throughputForecast",
			[]string{"throughputForecast.wipCongestionState", "throughputForecast.estimateCoverage.state", "throughputForecast.staleWip.state"},
			[]string{"throughputForecast.wipCongestionAsOfDay", "throughputForecast.estimateCoverage.asOfDay", "throughputForecast.staleWip.asOfDay"},
			[]string{"throughputForecast.wipCongestionGrainCount", "throughputForecast.estimateCoverage.grainCount", "throughputForecast.staleWip.grainCount"}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			cat, err := directread.DefaultCatalogue()
			if err != nil {
				t.Fatal(err)
			}
			op, refusal := cat.Lookup(tc.operation)
			if refusal != nil {
				t.Fatalf("%s is not served: %+v", tc.operation, refusal)
			}
			body, _ := json.Marshal(map[string]any{"data": opAnswerForDocument(t, op.DocumentText)})
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(body) }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), tc.operation, opMinimalVariables(t, op))
			if resp.Call != directread.CallServed {
				t.Fatalf("not served: %+v", resp)
			}
			if strings.Contains(h.logs.String(), directread.OperationPathsRemovedLog) || h.runner.Counters().PathsRemoved != 0 {
				t.Fatalf("an output path of the registered document was removed:\n%s", h.logs.String())
			}
			var data any
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				t.Fatalf("answer data: %v", err)
			}
			for _, path := range tc.states {
				if got, ok := opPathValue(data, path); !ok || got != "STALE" {
					t.Errorf("%s = %v, present %v; want STALE: %s", path, got, ok, resp.Data)
				}
			}
			for _, path := range tc.days {
				if got, ok := opPathValue(data, path); !ok || got != "2026-10-01" {
					t.Errorf("%s = %v, present %v; want the stale newest day 2026-10-01: %s", path, got, ok, resp.Data)
				}
			}
			for _, path := range tc.counts {
				if got, ok := opPathValue(data, path); !ok || got != float64(3) {
					t.Errorf("%s = %v, present %v; want the key count 3, not 0: %s", path, got, ok, resp.Data)
				}
			}
		})
	}
}

// The ops SDL carries TestOpsRiskInput.teamIds, but acr sends and allows no team id on testopsRisk:
// the operation is not served by run_operation, and its root is refused by graphql_query.
func TestTestopsRiskTeamIdsIsCarriedBySchemaTextOnly(t *testing.T) {
	if !strings.Contains(string(directread.EmbeddedOpsSchema()), "input TestOpsRiskInput {") {
		t.Fatal("the embedded ops SDL has no TestOpsRiskInput")
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	cat := policy.Catalogue()
	op, refusal := cat.Lookup("testopsRisk")
	if op != nil || refusal == nil || refusal.Code != directread.RefusalUnknownOperation {
		t.Fatalf("testopsRisk lookup = %v, %+v; want unknown_operation", op, refusal)
	}
	for _, class := range directread.CallerClassVocabulary() {
		for _, served := range cat.Operations(class) {
			for _, rule := range served.Variables {
				if strings.Contains(rule.Type, "TestOpsRiskInput") {
					t.Errorf("%s (%s) takes a TestOpsRiskInput variable at %s", served.Name, class, rule.Path)
				}
			}
		}
	}
	if !slices.Contains(policy.RefusedRootFields(), "testopsRisk") {
		t.Error("testopsRisk is not a refused graphql_query root")
	}
}
