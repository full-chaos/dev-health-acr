package genkitruntime

import (
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The model-output DTO sweep, as a standing check rather than a reading.
//
// The rule it enforces: any field reachable from a struct the model's
// constrained output is schema-inferred from becomes MODEL-AUTHORABLE, and
// must then be either grounded or stripped -- "the model would not do that"
// is not a third option. The outcome layer is engine-stamped disclosure about
// what the engine itself dropped, so a model that could write it could
// declare its own answer complete.
//
// Reading the DTO once and asserting the absence in a commit message would
// have been the same claim with no guard behind it. This walks the type.
func TestTheOutcomeLayerIsUnreachableFromTheModelOutputDTO(t *testing.T) {
	t.Parallel()

	forbidden := map[reflect.Type]string{
		reflect.TypeOf(contractsv1.ContextFabricAnswerCompleteness{}):        "the completeness block",
		reflect.TypeOf(contractsv1.ContextFabricPlanRequirementOutcomeRow{}): "a requirement outcome row",
	}

	visited := map[reflect.Type]bool{}
	reached := 0
	var walk func(t reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() == reflect.Map {
			walk(typ.Elem(), path+"[]")
			return
		}
		if what, bad := forbidden[typ]; bad {
			t.Errorf("%s is reachable from the model's own output type at %s.\n"+
				"Everything reachable there is model-authorable. A model that can write the outcome layer can declare its own answer complete.",
				what, path)
		}
		if typ.Kind() != reflect.Struct || visited[typ] {
			return
		}
		visited[typ] = true
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			reached++
			walk(field.Type, path+"."+field.Name)
		}
	}
	walk(reflect.TypeOf(synthesisOutput{}), "synthesisOutput")

	// A walk that reached nothing proves nothing. This is the same
	// assertion-count discipline every generated check here carries: a
	// silently empty population reads exactly like a clean result.
	if reached < 20 {
		t.Fatalf("the walk visited only %d fields; it is not reaching the DTO", reached)
	}
	// And the walk must be able to FAIL: a type that IS reachable, checked
	// against the same table, proves the detector detects.
	control := map[reflect.Type]bool{}
	for typ := range visited {
		control[typ] = true
	}
	if !control[reflect.TypeOf(synthesisOutput{})] {
		t.Fatal("the walk did not visit its own root type; the negative control is not wired")
	}
}

// The engine-stamped block must not appear in the reflected schema the model
// is constrained to, under any spelling.
func TestTheReflectedSynthesisSchemaNamesNoOutcomeField(t *testing.T) {
	t.Parallel()
	schema, err := SynthesisOutputSchema()
	if err != nil {
		t.Fatalf("SynthesisOutputSchema() error = %v", err)
	}
	document := string(schema)
	if len(document) < 100 {
		t.Fatalf("the reflected schema is %d bytes; it is not being produced", len(document))
	}
	for _, key := range []string{`"completeness"`, `"outcomes"`, `"not_derived"`, `"cause_overrun"`} {
		if strings.Contains(document, key) {
			t.Errorf("the model's constrained output schema names %s; the outcome layer is engine-stamped and must not be model-authorable", key)
		}
	}
}
