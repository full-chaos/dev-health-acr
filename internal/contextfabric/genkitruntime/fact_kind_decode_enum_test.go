package genkitruntime

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func interpretationSchemaLoader(t *testing.T) *gojsonschema.Schema {
	t.Helper()
	raw, err := InterpretationOutputSchema()
	if err != nil {
		t.Fatalf("InterpretationOutputSchema: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(doc))
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func schemaFactKindEnum(t *testing.T) []string {
	t.Helper()
	raw, err := InterpretationOutputSchema()
	if err != nil {
		t.Fatalf("InterpretationOutputSchema: %v", err)
	}
	var doc struct {
		Properties struct {
			FactRequirements struct {
				Items struct {
					Properties struct {
						Kind struct {
							Enum []string `json:"enum"`
						} `json:"kind"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"fact_requirements"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return doc.Properties.FactRequirements.Items.Properties.Kind.Enum
}

// The kind enum handed to the model, the closed set the validator accepts and
// the published vocabulary are one set.
func TestInterpretationSchemaFactKindEnumIsTheValidatorVocabulary(t *testing.T) {
	t.Parallel()
	enum := schemaFactKindEnum(t)
	vocabulary := contractsv1.ContextFabricFactKindVocabulary()
	if len(enum) == 0 {
		t.Fatal("schema fact_requirements[].kind carries no enum")
	}
	want := make([]string, 0, len(vocabulary))
	for _, kind := range vocabulary {
		want = append(want, string(kind))
		if err := (contractsv1.ContextFabricFactRequirement{Kind: kind}).Validate(); err != nil {
			t.Fatalf("vocabulary kind %q is rejected by the validator: %v", kind, err)
		}
	}
	got := append([]string(nil), enum...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("schema enum = %v, want the vocabulary %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("schema enum = %v, want the vocabulary %v", got, want)
		}
	}
}

// A kind outside the vocabulary cannot pass the schema the model is bound to;
// every vocabulary kind can.
func TestInterpretationSchemaRefusesOutOfVocabularyFactKind(t *testing.T) {
	t.Parallel()
	schema := interpretationSchemaLoader(t)
	build := func(kind string) gojsonschema.JSONLoader {
		output := validInterpretationOutput()
		output.FactRequirements = []factRequirementOutput{{Kind: kind}}
		return gojsonschema.NewGoLoader(output)
	}
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		result, err := schema.Validate(build(string(kind)))
		if err != nil {
			t.Fatalf("validate %q: %v", kind, err)
		}
		if !result.Valid() {
			t.Errorf("vocabulary kind %q refused by the schema: %v", kind, result.Errors())
		}
	}
	for _, kind := range []string{"dependencies", "dependencies_and_blockers", "", " status", "STATUS"} {
		result, err := schema.Validate(build(kind))
		if err != nil {
			t.Fatalf("validate %q: %v", kind, err)
		}
		if result.Valid() {
			t.Errorf("out-of-vocabulary kind %q passed the schema", kind)
		}
	}
}

// The hand-authored fact_requirements item schema keeps the shape the
// struct-tag default had: kind required, parameters an optional string map,
// no other property.
func TestInterpretationSchemaFactRequirementShapeIsClosed(t *testing.T) {
	t.Parallel()
	schema := interpretationSchemaLoader(t)
	for name, tc := range map[string]struct {
		requirements string
		valid        bool
	}{
		"kind only":              {`[{"kind":"status"}]`, true},
		"kind with parameters":   {`[{"kind":"status","parameters":{"a":"b"}}]`, true},
		"non-string parameter":   {`[{"kind":"status","parameters":{"a":1}}]`, false},
		"missing kind":           {`[{"parameters":{}}]`, false},
		"unknown extra property": {`[{"kind":"status","extra":"x"}]`, false},
	} {
		document := `{"shape":"open","requested_judgment":"j","time_context":{"axis":"current"},"clarification_needed":false,"fact_requirements":` + tc.requirements + `}`
		result, err := schema.Validate(gojsonschema.NewStringLoader(document))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.Valid() != tc.valid {
			t.Errorf("%s: valid = %t, want %t: %v", name, result.Valid(), tc.valid, result.Errors())
		}
	}
}
