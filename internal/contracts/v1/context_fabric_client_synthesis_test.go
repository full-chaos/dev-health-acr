package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

func synthesisInputFixture() ContextFabricSynthesisInput {
	input := json.RawMessage(`{"facts":[]}`)
	sum := sha256.Sum256(input)
	return ContextFabricSynthesisInput{
		Contract: ContextFabricSynthesisContract{
			ModelOutputVersion: "context-fabric-model-output.v8",
			PromptVersion:      "context-fabric-synthesis.v3",
			SystemSHA256:       strings.Repeat("b", 64),
		},
		Input:       input,
		InputSHA256: hex.EncodeToString(sum[:]),
		Rules:       []string{"Use only the facts in this input."},
	}
}

func withInput(s *ContextFabricSynthesisInput, input json.RawMessage) {
	sum := sha256.Sum256(input)
	s.Input = input
	s.InputSHA256 = hex.EncodeToString(sum[:])
}

func TestSynthesisInputValidate(t *testing.T) {
	t.Parallel()
	atBound := json.RawMessage(`{"k":"` + strings.Repeat("a", ContextFabricSynthesisInputMaxBytes-8) + `"}`)
	if len(atBound) != ContextFabricSynthesisInputMaxBytes {
		t.Fatalf("fixture is %d bytes, want exactly the bound %d", len(atBound), ContextFabricSynthesisInputMaxBytes)
	}
	manyRules := make([]string, ContextFabricSynthesisInputRulesMaxCount)
	for i := range manyRules {
		manyRules[i] = "rule"
	}
	cases := []struct {
		name   string
		mutate func(*ContextFabricSynthesisInput)
		valid  bool
	}{
		{"complete", func(*ContextFabricSynthesisInput) {}, true},
		{"bounded", func(s *ContextFabricSynthesisInput) { s.Bounded = true }, true},
		{"input at the byte bound", func(s *ContextFabricSynthesisInput) { withInput(s, atBound) }, true},
		{"input one byte over the bound", func(s *ContextFabricSynthesisInput) { withInput(s, append(json.RawMessage(" "), atBound...)) }, false},
		{"model output version blank", func(s *ContextFabricSynthesisInput) { s.Contract.ModelOutputVersion = "  " }, false},
		{"model output version empty", func(s *ContextFabricSynthesisInput) { s.Contract.ModelOutputVersion = "" }, false},
		{"model output version over 256", func(s *ContextFabricSynthesisInput) { s.Contract.ModelOutputVersion = strings.Repeat("v", 257) }, false},
		{"model output version at 256", func(s *ContextFabricSynthesisInput) { s.Contract.ModelOutputVersion = strings.Repeat("v", 256) }, true},
		{"prompt version blank", func(s *ContextFabricSynthesisInput) { s.Contract.PromptVersion = "  " }, false},
		{"prompt version padded", func(s *ContextFabricSynthesisInput) { s.Contract.PromptVersion = " v3" }, false},
		{"prompt version over 256", func(s *ContextFabricSynthesisInput) { s.Contract.PromptVersion = strings.Repeat("v", 257) }, false},
		{"system sha256 absent", func(s *ContextFabricSynthesisInput) { s.Contract.SystemSHA256 = "" }, false},
		{"system sha256 uppercase", func(s *ContextFabricSynthesisInput) { s.Contract.SystemSHA256 = strings.Repeat("B", 64) }, false},
		{"system sha256 short", func(s *ContextFabricSynthesisInput) { s.Contract.SystemSHA256 = strings.Repeat("b", 63) }, false},
		{"input absent", func(s *ContextFabricSynthesisInput) { withInput(s, nil) }, false},
		{"input is an array", func(s *ContextFabricSynthesisInput) { withInput(s, json.RawMessage(`[]`)) }, false},
		{"input is a string", func(s *ContextFabricSynthesisInput) { withInput(s, json.RawMessage(`"facts"`)) }, false},
		{"input is two documents", func(s *ContextFabricSynthesisInput) { withInput(s, json.RawMessage(`{}{}`)) }, false},
		{"input is not JSON", func(s *ContextFabricSynthesisInput) { withInput(s, json.RawMessage(`{"facts":`)) }, false},
		{"input sha256 absent", func(s *ContextFabricSynthesisInput) { s.InputSHA256 = "" }, false},
		{"input sha256 uppercase", func(s *ContextFabricSynthesisInput) { s.InputSHA256 = strings.ToUpper(s.InputSHA256) }, false},
		{"input sha256 short", func(s *ContextFabricSynthesisInput) { s.InputSHA256 = s.InputSHA256[:63] }, false},
		{"input sha256 of another input", func(s *ContextFabricSynthesisInput) { s.InputSHA256 = strings.Repeat("c", 64) }, false},
		{"input changed after the digest", func(s *ContextFabricSynthesisInput) { s.Input = json.RawMessage(`{"facts":[1]}`) }, false},
		{"no rules", func(s *ContextFabricSynthesisInput) { s.Rules = nil }, false},
		{"rules at the count bound", func(s *ContextFabricSynthesisInput) { s.Rules = manyRules }, true},
		{"too many rules", func(s *ContextFabricSynthesisInput) { s.Rules = append(append([]string(nil), manyRules...), "rule") }, false},
		{"rule empty", func(s *ContextFabricSynthesisInput) { s.Rules = []string{""} }, false},
		{"rule blank", func(s *ContextFabricSynthesisInput) { s.Rules = []string{"  "} }, false},
		{"rule padded", func(s *ContextFabricSynthesisInput) { s.Rules = []string{"rule "} }, false},
		{"rule at 512", func(s *ContextFabricSynthesisInput) { s.Rules = []string{strings.Repeat("r", 512)} }, true},
		{"rule over 512", func(s *ContextFabricSynthesisInput) { s.Rules = []string{strings.Repeat("r", 513)} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := synthesisInputFixture()
			tc.mutate(&input)
			if err := input.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestInvestigationRequestSynthesisMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode  ContextFabricSynthesisMode
		valid bool
	}{
		{"", true},
		{ContextFabricSynthesisModeServer, true},
		{ContextFabricSynthesisModeClient, true},
		{"Client", false},
		{"both", false},
		{" client", false},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			var request ContextFabricInvestigationRequest
			if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_request.v1.json"), &request); err != nil {
				t.Fatal(err)
			}
			request.SynthesisMode = tc.mode
			if err := request.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestVersionSetSynthesisSource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		source ContextFabricSynthesisSource
		valid  bool
	}{
		{"", true},
		{ContextFabricSynthesisSourceServer, true},
		{ContextFabricSynthesisSourceClient, true},
		{"Server", false},
		{"both", false},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			t.Parallel()
			versions := contextFabricGoldenResult(t, "context_fabric_investigation_result.v1.json").Versions
			versions.SynthesisSource = tc.source
			if err := versions.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestClientSynthesisTextsFitTheirFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		max        int
	}{
		{"answer as direct_judgment", ContextFabricClientSynthesisAnswer, ContextFabricDirectJudgmentMaxLength},
		{"answer as current_state", ContextFabricClientSynthesisAnswer, ContextFabricCurrentStateMaxLength},
		{"answer as deterministic_answer", ContextFabricClientSynthesisAnswer, ContextFabricDeterministicAnswerMaxLength},
		{"limitation", ContextFabricClientSynthesisCommitNotAffirmedLimitation, ContextFabricLimitationMaxLength},
	} {
		if n := len([]rune(tc.text)); n < 1 || n > tc.max || strings.TrimSpace(tc.text) != tc.text {
			t.Errorf("%s: %d runes against a bound of %d", tc.name, n, tc.max)
		}
	}
	if !IsContextFabricServiceAuthoredLimitation(ContextFabricClientSynthesisCommitNotAffirmedLimitation) {
		t.Error("the commit-not-affirmed limitation is not registered as service-authored")
	}
	if IsContextFabricServiceAuthoredLimitation(ContextFabricClientSynthesisAnswer) {
		t.Error("the client synthesis answer is an answer sentence, not a limitation")
	}
}

func TestInvestigationResponseJSONShape(t *testing.T) {
	t.Parallel()
	result := contextFabricGoldenResult(t, "context_fabric_investigation_result.v1.json")
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var resultKeys map[string]json.RawMessage
	if err := json.Unmarshal(resultJSON, &resultKeys); err != nil {
		t.Fatal(err)
	}

	t.Run("sibling absent when nil", func(t *testing.T) {
		t.Parallel()
		encoded, err := json.Marshal(ContextFabricInvestigationResponse{ContextFabricInvestigationResult: result})
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != string(resultJSON) {
			t.Fatal("an envelope without a bundle is not byte-identical to the result alone")
		}
	})

	t.Run("flat with the sibling and round trip", func(t *testing.T) {
		t.Parallel()
		bundle := synthesisInputFixture()
		response := ContextFabricInvestigationResponse{ContextFabricInvestigationResult: result, SynthesisInput: &bundle}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &keys); err != nil {
			t.Fatal(err)
		}
		if _, nested := keys["ContextFabricInvestigationResult"]; nested || len(keys) != len(resultKeys)+1 {
			t.Fatalf("envelope has %d top-level keys, want the result's %d plus synthesis_input", len(keys), len(resultKeys))
		}
		if _, ok := keys["synthesis_input"]; !ok {
			t.Fatal("synthesis_input is missing from the envelope")
		}
		if err := contractcheck.ValidateSerialized("", "context_fabric_investigation_response.v1.schema.json", encoded); err != nil {
			t.Fatalf("envelope does not validate against its schema: %v", err)
		}
		var decoded ContextFabricInvestigationResponse
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.SynthesisInput == nil || !reflect.DeepEqual(*decoded.SynthesisInput, bundle) {
			t.Fatal("synthesis_input did not round trip")
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("decoded envelope does not validate: %v", err)
		}
	})

	t.Run("validate covers the bundle", func(t *testing.T) {
		t.Parallel()
		bundle := synthesisInputFixture()
		bundle.Rules = nil
		response := ContextFabricInvestigationResponse{ContextFabricInvestigationResult: result, SynthesisInput: &bundle}
		if err := response.Validate(); err == nil {
			t.Fatal("an invalid bundle passed the envelope validation")
		}
	})

	t.Run("validate covers the result", func(t *testing.T) {
		t.Parallel()
		bundle := synthesisInputFixture()
		broken := result
		broken.Question = ""
		if err := (ContextFabricInvestigationResponse{ContextFabricInvestigationResult: broken, SynthesisInput: &bundle}).Validate(); err == nil {
			t.Fatal("an invalid result passed the envelope validation")
		}
	})
}

// TestResponseSchemasCarryTheResultSchemas pins each response schema to the
// result schema it extends: the same document plus the synthesis_input
// property, so a change to the result schema cannot leave the response
// schema behind.
func TestResponseSchemasCarryTheResultSchemas(t *testing.T) {
	t.Parallel()
	load := func(name string) map[string]any {
		raw, err := os.ReadFile(repopath.Path(t, "contracts", "jsonschema", "v1", name))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	for _, version := range []string{"v1", "v2"} {
		result := load("context_fabric_investigation_result." + version + ".schema.json")
		response := load("context_fabric_investigation_response." + version + ".schema.json")
		props, _ := response["properties"].(map[string]any)
		if _, ok := props["synthesis_input"]; !ok {
			t.Fatalf("%s: response schema has no synthesis_input property", version)
		}
		delete(props, "synthesis_input")
		for _, identity := range []string{"$id", "title", "description"} {
			delete(result, identity)
			delete(response, identity)
		}
		if !reflect.DeepEqual(result, response) {
			t.Fatalf("%s: response schema has drifted from the result schema it extends", version)
		}
	}
}
