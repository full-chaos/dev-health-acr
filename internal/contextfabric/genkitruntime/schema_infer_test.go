package genkitruntime

import (
	"encoding/json"
	"os"
	"testing"
)

// The golden files hold the exact bytes github.com/firebase/genkit/go v1.11.0
// core.InferSchemaMap produced for each output type this package infers.
func TestInferSchemaMapMatchesGenkitGolden(t *testing.T) {
	cases := map[string]any{
		"interpretation": interpretationOutput{},
		"synthesis":      synthesisOutput{},
		"phrasing":       phrasingOutput{},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile("testdata/genkit_schema_golden/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if len(want) == 0 {
				t.Fatal("golden file is empty")
			}
			got, err := json.Marshal(inferSchemaMap(value))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("inferred schema drifted from the genkit golden\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestOutputSchemaInstructionsEmbedGoldenBytes(t *testing.T) {
	want, err := os.ReadFile("testdata/genkit_schema_golden/interpretation.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := newOutputSchema(interpretationOutput{}, true)
	if err != nil {
		t.Fatal(err)
	}
	wantText := "Output should be in JSON format and conform to the following schema:\n\n```" + string(want) + "```"
	if schema.instructions != wantText {
		t.Fatalf("instructions drifted:\n got: %s\nwant: %s", schema.instructions, wantText)
	}
}
