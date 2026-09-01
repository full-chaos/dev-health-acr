package genkitruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4690 Commit F, design §4.1 (r2 F1): synthesisOutput.CoverageDisclosures
// is DELIBERATELY typed json.RawMessage, unconstrained in the output
// schema, so genkit's structured-output validation can never reject a
// malformed value on this field. All shape policing is local, in
// parseCoverageDisclosures. These tests pin that decode-isolation property
// directly against the parser, then against toDomain, then against the
// full Runtime.SynthesizeAnswer call -- the r2 F1 scenario itself: a
// malformed disclosure entry must never cost the caller the rest of an
// otherwise-valid answer.

func TestParseCoverageDisclosures_AbsentIsNotUndecodable(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]json.RawMessage{
		"nil":          nil,
		"empty":        json.RawMessage(``),
		"whitespace":   json.RawMessage("   \n\t"),
		"literal null": json.RawMessage(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			disclosures, undecodable := parseCoverageDisclosures(raw)
			if undecodable {
				t.Fatalf("parseCoverageDisclosures(%q) undecodable = true, want false (absent is not a decode failure)", raw)
			}
			if disclosures != nil {
				t.Fatalf("parseCoverageDisclosures(%q) = %#v, want nil", raw, disclosures)
			}
		})
	}
}

func TestParseCoverageDisclosures_ValidArrayDecodes(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"detail_id":"cov-01","text":"Some readiness data could not be reached."}]`)
	disclosures, undecodable := parseCoverageDisclosures(raw)
	if undecodable {
		t.Fatalf("parseCoverageDisclosures() undecodable = true, want false for a well-formed array")
	}
	if len(disclosures) != 1 || disclosures[0].DetailID != "cov-01" || disclosures[0].Text != "Some readiness data could not be reached." {
		t.Fatalf("parseCoverageDisclosures() = %#v, want the one decoded entry verbatim", disclosures)
	}
}

// TestParseCoverageDisclosures_NonStringTextIsUndecodable is the r2 F1
// scenario's own smallest reproduction: a non-string "text" value fails
// unmarshalling into []contextfabric.CoverageDisclosure, and that failure
// must be reported as undecodable, never propagated as an error.
func TestParseCoverageDisclosures_NonStringTextIsUndecodable(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"detail_id":"cov-01","text":17}]`)
	disclosures, undecodable := parseCoverageDisclosures(raw)
	if !undecodable {
		t.Fatalf("parseCoverageDisclosures() undecodable = false, want true for a non-string text field")
	}
	if disclosures != nil {
		t.Fatalf("parseCoverageDisclosures() = %#v, want nil on an undecodable value", disclosures)
	}
}

func TestParseCoverageDisclosures_NonArrayTopLevelIsUndecodable(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"detail_id":"cov-01","text":"not an array"}`)
	disclosures, undecodable := parseCoverageDisclosures(raw)
	if !undecodable {
		t.Fatalf("parseCoverageDisclosures() undecodable = false, want true for a non-array top-level value")
	}
	if disclosures != nil {
		t.Fatalf("parseCoverageDisclosures() = %#v, want nil on an undecodable value", disclosures)
	}
}

// TestSynthesisOutputToDomain_UndecodableCoverageDisclosuresNeverErrors pins
// the same scenario one layer up: toDomain must still succeed (the
// deterministic_answer bound is otherwise satisfied) and the draft must
// report undecodable, not carry a stray disclosure.
func TestSynthesisOutputToDomain_UndecodableCoverageDisclosuresNeverErrors(t *testing.T) {
	t.Parallel()
	output := validSynthesisOutput()
	output.CoverageDisclosures = json.RawMessage(`[{"detail_id":"cov-01","text":17}]`)
	draft, err := output.toDomain()
	if err != nil {
		t.Fatalf("toDomain() error = %v, want nil -- an undecodable coverage_disclosures value must never fail the whole draft", err)
	}
	if !draft.CoverageDisclosuresUndecodable {
		t.Fatalf("draft.CoverageDisclosuresUndecodable = false, want true")
	}
	if draft.CoverageDisclosures != nil {
		t.Fatalf("draft.CoverageDisclosures = %#v, want nil when undecodable", draft.CoverageDisclosures)
	}
	if draft.DeterministicAnswer == "" {
		t.Fatalf("draft.DeterministicAnswer is empty, want the rest of the draft to decode normally")
	}
}

// TestRuntimeSynthesizeAnswer_UndecodableCoverageDisclosuresServesTheAnswer
// is the FULL r2 F1 scenario against the actual production call site: a
// malformed coverage_disclosures value must never cost the caller the rest
// of an otherwise-valid answer. This is RED on parent f94df525, where
// synthesisOutput carries no coverage_disclosures field at all (a compile
// failure, not a runtime failure -- proof the surface is new).
func TestRuntimeSynthesizeAnswer_UndecodableCoverageDisclosuresServesTheAnswer(t *testing.T) {
	t.Parallel()
	output := validSynthesisOutput()
	output.CoverageDisclosures = json.RawMessage(`[{"detail_id":"cov-01","text":17}]`)
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{})
	draft, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want nil -- the answer must be served despite the malformed disclosure", err)
	}
	if receipt.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", receipt.Outcome)
	}
	if !draft.CoverageDisclosuresUndecodable {
		t.Fatalf("draft.CoverageDisclosuresUndecodable = false, want true")
	}
	if draft.CoverageDisclosures != nil {
		t.Fatalf("draft.CoverageDisclosures = %#v, want nil", draft.CoverageDisclosures)
	}
}

// TestSynthesisOutputSchema_CoverageDisclosuresIsUnconstrained pins the
// invopop/jsonschema special-case this whole design leans on (design
// §4.1's own verification): the reflected schema for synthesisOutput must
// carry NO array/object type constraint on coverage_disclosures, so a
// malformed value can never fail genkit's own schema validation upstream
// of the local guard.
func TestSynthesisOutputSchema_CoverageDisclosuresIsUnconstrained(t *testing.T) {
	t.Parallel()
	raw, err := SynthesisOutputSchema()
	if err != nil {
		t.Fatalf("SynthesisOutputSchema() error = %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("json.Unmarshal(schema) error = %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %s", raw)
	}
	field, ok := properties["coverage_disclosures"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties has no coverage_disclosures entry: %s", raw)
	}
	if _, hasType := field["type"]; hasType {
		t.Fatalf("coverage_disclosures schema = %#v, want NO type constraint (json.RawMessage must reflect unconstrained)", field)
	}
	if _, hasItems := field["items"]; hasItems {
		t.Fatalf("coverage_disclosures schema = %#v, want NO items constraint", field)
	}
}
