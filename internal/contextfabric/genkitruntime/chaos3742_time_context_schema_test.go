package genkitruntime

import (
	"encoding/json"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// TestOutputTimeContextSchemaEnforcesPerAxisShape validates sample
// time_context instances against outputTimeContext's own JSONSchema()
// output using gojsonschema -- the SAME validator genkit's own
// custom-constrained-output path (ai.WithCustomConstrainedOutput, used by
// both InterpretQuestion and SynthesizeAnswer above) enforces a model's
// structured output against, per genkit's own doc comment on that option.
//
// This is the direct regression test for CHAOS-3742 RUN 3 (2026-08-21):
// the pre-fix schema accepted {"axis":"range"} alone (case "range missing
// bounds" below) -- exactly what the file-exchange codex responder sent,
// and exactly what contractsv1.ContextFabricTimeContext.Validate()
// (wrapped by InterpretedQuestion.Validate as "time_context: %w") then
// rejected downstream, after the (simulated, in production a real) model
// call had already been paid for. codex adversarial review (2026-08-21)
// additionally found the FIRST version of this fix closed only the range
// case, leaving current/valid_time/observed_time still able to carry the
// wrong fields -- every axis is covered here.
func TestOutputTimeContextSchemaEnforcesPerAxisShape(t *testing.T) {
	raw, err := InterpretationOutputSchema()
	if err != nil {
		t.Fatalf("InterpretationOutputSchema: %v", err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	properties, ok := full["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no top-level properties: %v", full)
	}
	timeContextSchema, ok := properties["time_context"]
	if !ok {
		t.Fatalf("schema has no time_context property: %v", properties)
	}
	schemaLoader := gojsonschema.NewGoLoader(timeContextSchema)

	cases := []struct {
		name      string
		instance  string
		wantValid bool
	}{
		{"current valid", `{"axis":"current"}`, true},
		{"current with as_of rejected", `{"axis":"current","as_of":"2026-01-01T00:00:00Z"}`, false},
		{"current with start rejected", `{"axis":"current","start":"2026-01-01T00:00:00Z"}`, false},
		{"valid_time valid", `{"axis":"valid_time","as_of":"2026-01-01T00:00:00Z"}`, true},
		{"valid_time missing as_of rejected", `{"axis":"valid_time"}`, false},
		{"valid_time with start rejected", `{"axis":"valid_time","as_of":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z"}`, false},
		{"observed_time valid", `{"axis":"observed_time","as_of":"2026-01-01T00:00:00Z"}`, true},
		{"observed_time missing as_of rejected", `{"axis":"observed_time"}`, false},
		{"range valid", `{"axis":"range","start":"2026-01-01T00:00:00Z","end":"2026-01-02T00:00:00Z"}`, true},
		{"range missing bounds rejected (RUN 3 live regression)", `{"axis":"range"}`, false},
		{"range missing end rejected", `{"axis":"range","start":"2026-01-01T00:00:00Z"}`, false},
		{"range missing start rejected", `{"axis":"range","end":"2026-01-02T00:00:00Z"}`, false},
		{"range with as_of rejected", `{"axis":"range","start":"2026-01-01T00:00:00Z","end":"2026-01-02T00:00:00Z","as_of":"2026-01-01T00:00:00Z"}`, false},
		{"unknown top-level field rejected", `{"axis":"current","unexpected":true}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := gojsonschema.Validate(schemaLoader, gojsonschema.NewStringLoader(tc.instance))
			if err != nil {
				t.Fatalf("validate %s: %v", tc.instance, err)
			}
			if result.Valid() != tc.wantValid {
				t.Errorf("instance %s: valid=%v (want %v), errors=%v", tc.instance, result.Valid(), tc.wantValid, result.Errors())
			}
		})
	}
}
