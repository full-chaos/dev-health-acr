package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validSuppliedSynthesisFixture() ContextFabricSuppliedSynthesis {
	return ContextFabricSuppliedSynthesis{
		Output:             json.RawMessage(`{"answer":"open"}`),
		ModelOutputVersion: "context-fabric-synthesis-output.v3",
		PromptVersion:      "context-fabric-synthesis.v17",
		SystemSHA256:       strings.Repeat("a", 64),
		InputSHA256:        strings.Repeat("b", 64),
		ClientModel:        "anthropic/claude-test:1.0_a-b",
	}
}

func TestSuppliedSynthesisValidate(t *testing.T) {
	t.Parallel()
	atBound := json.RawMessage(`{"k":"` + strings.Repeat("a", ContextFabricSuppliedSynthesisMaxBytes-8) + `"}`)
	if len(atBound) != ContextFabricSuppliedSynthesisMaxBytes {
		t.Fatalf("fixture is %d bytes, want exactly the bound %d", len(atBound), ContextFabricSuppliedSynthesisMaxBytes)
	}
	cases := []struct {
		name   string
		mutate func(*ContextFabricSuppliedSynthesis)
		valid  bool
	}{
		{"complete", func(*ContextFabricSuppliedSynthesis) {}, true},
		{"no client model", func(s *ContextFabricSuppliedSynthesis) { s.ClientModel = "" }, true},
		{"system sha256 absent: left to the contract gate", func(s *ContextFabricSuppliedSynthesis) { s.SystemSHA256 = "" }, true},
		{"input sha256 absent: left to the contract gate", func(s *ContextFabricSuppliedSynthesis) { s.InputSHA256 = "" }, true},
		{"model output version absent: left to the contract gate", func(s *ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "" }, true},
		{"prompt version absent: left to the contract gate", func(s *ContextFabricSuppliedSynthesis) { s.PromptVersion = "" }, true},
		{"output at the byte bound", func(s *ContextFabricSuppliedSynthesis) { s.Output = atBound }, true},
		{"output one byte over the bound", func(s *ContextFabricSuppliedSynthesis) { s.Output = append(json.RawMessage(" "), atBound...) }, false},
		{"output absent", func(s *ContextFabricSuppliedSynthesis) { s.Output = nil }, false},
		{"output empty", func(s *ContextFabricSuppliedSynthesis) { s.Output = json.RawMessage(``) }, false},
		{"output is an array", func(s *ContextFabricSuppliedSynthesis) { s.Output = json.RawMessage(`[]`) }, false},
		{"output is a string", func(s *ContextFabricSuppliedSynthesis) { s.Output = json.RawMessage(`"open"`) }, false},
		{"output is two documents", func(s *ContextFabricSuppliedSynthesis) { s.Output = json.RawMessage(`{}{}`) }, false},
		{"output is not JSON", func(s *ContextFabricSuppliedSynthesis) { s.Output = json.RawMessage(`{"answer":`) }, false},
		{"model output version blank", func(s *ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "  " }, false},
		{"model output version over 256", func(s *ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = strings.Repeat("v", 257) }, false},
		{"model output version at 256", func(s *ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = strings.Repeat("v", 256) }, true},
		{"prompt version padded", func(s *ContextFabricSuppliedSynthesis) { s.PromptVersion = " v17" }, false},
		{"prompt version over 256", func(s *ContextFabricSuppliedSynthesis) { s.PromptVersion = strings.Repeat("v", 257) }, false},
		{"system sha256 uppercase", func(s *ContextFabricSuppliedSynthesis) { s.SystemSHA256 = strings.Repeat("A", 64) }, false},
		{"system sha256 short", func(s *ContextFabricSuppliedSynthesis) { s.SystemSHA256 = strings.Repeat("a", 63) }, false},
		{"system sha256 not hex", func(s *ContextFabricSuppliedSynthesis) { s.SystemSHA256 = strings.Repeat("g", 64) }, false},
		{"input sha256 uppercase", func(s *ContextFabricSuppliedSynthesis) { s.InputSHA256 = strings.Repeat("B", 64) }, false},
		{"input sha256 short", func(s *ContextFabricSuppliedSynthesis) { s.InputSHA256 = strings.Repeat("b", 63) }, false},
		{"input sha256 long", func(s *ContextFabricSuppliedSynthesis) { s.InputSHA256 = strings.Repeat("b", 65) }, false},
		{"client model with a space", func(s *ContextFabricSuppliedSynthesis) { s.ClientModel = "claude test" }, false},
		{"client model with a newline", func(s *ContextFabricSuppliedSynthesis) { s.ClientModel = "claude\ntest" }, false},
		{"client model over 128", func(s *ContextFabricSuppliedSynthesis) { s.ClientModel = strings.Repeat("m", 129) }, false},
		{"client model at 128", func(s *ContextFabricSuppliedSynthesis) { s.ClientModel = strings.Repeat("m", 128) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			supplied := validSuppliedSynthesisFixture()
			tc.mutate(&supplied)
			if err := supplied.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestSuppliedSynthesisMissing(t *testing.T) {
	t.Parallel()
	if got := validSuppliedSynthesisFixture().Missing(); len(got) != 0 {
		t.Fatalf("Missing() on a complete contract = %v, want none", got)
	}
	cases := []struct {
		name   string
		mutate func(*ContextFabricSuppliedSynthesis)
		want   []string
	}{
		{"model output version", func(s *ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "" }, []string{"model_output_version"}},
		{"prompt version", func(s *ContextFabricSuppliedSynthesis) { s.PromptVersion = "" }, []string{"prompt_version"}},
		{"system sha256", func(s *ContextFabricSuppliedSynthesis) { s.SystemSHA256 = "" }, []string{"system_sha256"}},
		{"input sha256", func(s *ContextFabricSuppliedSynthesis) { s.InputSHA256 = "" }, []string{"input_sha256"}},
		{"prompt version and input sha256", func(s *ContextFabricSuppliedSynthesis) { s.PromptVersion, s.InputSHA256 = "", "" }, []string{"prompt_version", "input_sha256"}},
		{"all four", func(s *ContextFabricSuppliedSynthesis) {
			s.ModelOutputVersion, s.PromptVersion, s.SystemSHA256, s.InputSHA256 = "", "", "", ""
		}, []string{"model_output_version", "prompt_version", "system_sha256", "input_sha256"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			supplied := validSuppliedSynthesisFixture()
			tc.mutate(&supplied)
			if got := supplied.Missing(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Missing() = %v, want %v", got, tc.want)
			}
			contract := MCPSynthesisContract{
				ModelOutputVersion: supplied.ModelOutputVersion,
				PromptVersion:      supplied.PromptVersion,
				SystemSHA256:       supplied.SystemSHA256,
				InputSHA256:        supplied.InputSHA256,
			}
			if got := contract.Missing(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("MCPSynthesisContract.Missing() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSuppliedSynthesisReasonVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	want := []string{"input_changed", "supplied_interpretation_required"}
	if got := ContextFabricSuppliedSynthesisReasons(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	for _, reason := range want {
		if !ValidContextFabricSuppliedSynthesisReason(reason) {
			t.Fatalf("%q is not accepted", reason)
		}
	}
	for _, reason := range []string{"", "Input_Changed", "input_changed ", "rejected", "contract_mismatch"} {
		if ValidContextFabricSuppliedSynthesisReason(reason) {
			t.Fatalf("%q is accepted", reason)
		}
	}
	if ContextFabricSuppliedSynthesisReasonInputChanged != "input_changed" || ContextFabricSuppliedSynthesisReasonInterpretationRequired != "supplied_interpretation_required" {
		t.Fatal("reason constants changed")
	}
	keys := []string{ContextFabricSynthesisContractDetailsKey, ContextFabricSuppliedSynthesisReasonKey, ContextFabricSynthesisInputDetailsKey, ContextFabricSynthesisRejectionReasonKey}
	if !reflect.DeepEqual(keys, []string{"synthesis_contract", "reason", "synthesis_input", "rejection_reason"}) {
		t.Fatalf("details keys = %v", keys)
	}
}

func TestSynthesisContractRefusalValidate(t *testing.T) {
	t.Parallel()
	valid := func() ContextFabricSynthesisContractRefusal {
		return ContextFabricSynthesisContractRefusal{
			Mismatch: []string{"prompt_version", "input_sha256"},
			Current: ContextFabricSynthesisContract{
				ModelOutputVersion: "context-fabric-synthesis-output.v3",
				PromptVersion:      "context-fabric-synthesis.v17",
				SystemSHA256:       strings.Repeat("a", 64),
			},
		}
	}
	cases := []struct {
		name   string
		mutate func(*ContextFabricSynthesisContractRefusal)
		valid  bool
	}{
		{"valid", func(*ContextFabricSynthesisContractRefusal) {}, true},
		{"all four fields", func(r *ContextFabricSynthesisContractRefusal) {
			r.Mismatch = []string{"model_output_version", "prompt_version", "system_sha256", "input_sha256"}
		}, true},
		{"no field", func(r *ContextFabricSynthesisContractRefusal) { r.Mismatch = nil }, false},
		{"five fields", func(r *ContextFabricSynthesisContractRefusal) {
			r.Mismatch = []string{"model_output_version", "prompt_version", "system_sha256", "input_sha256", "input_sha256"}
		}, false},
		{"unknown field", func(r *ContextFabricSynthesisContractRefusal) { r.Mismatch = []string{"output"} }, false},
		{"duplicate field", func(r *ContextFabricSynthesisContractRefusal) {
			r.Mismatch = []string{"prompt_version", "prompt_version"}
		}, false},
		{"current version blank", func(r *ContextFabricSynthesisContractRefusal) { r.Current.PromptVersion = "" }, false},
		{"current model output version blank", func(r *ContextFabricSynthesisContractRefusal) { r.Current.ModelOutputVersion = "" }, false},
		{"current sha256 short", func(r *ContextFabricSynthesisContractRefusal) { r.Current.SystemSHA256 = "abc" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			refusal := valid()
			tc.mutate(&refusal)
			if err := refusal.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestInvestigationRequestSuppliedSynthesisNeedsClientMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mode  ContextFabricSynthesisMode
		mut   func(*ContextFabricSuppliedSynthesis)
		valid bool
	}{
		{"mode absent", "", nil, false},
		{"mode server", ContextFabricSynthesisModeServer, nil, false},
		{"mode client", ContextFabricSynthesisModeClient, nil, true},
		{"mode client, bad draft", ContextFabricSynthesisModeClient, func(s *ContextFabricSuppliedSynthesis) { s.Output = json.RawMessage(`[]`) }, false},
		{"mode client, contract value absent", ContextFabricSynthesisModeClient, func(s *ContextFabricSuppliedSynthesis) { s.InputSHA256 = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := validContextFabricContractRequest()
			supplied := validSuppliedSynthesisFixture()
			if tc.mut != nil {
				tc.mut(&supplied)
			}
			request.SynthesisMode = tc.mode
			request.SuppliedSynthesis = &supplied
			if err := request.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
	request := validContextFabricContractRequest()
	if err := request.Validate(); err != nil {
		t.Fatalf("a request without supplied_synthesis must stay valid: %v", err)
	}
}

func validMCPWriteBackFixture() MCPInvestigateWithInterpretationRequest {
	return MCPInvestigateWithInterpretationRequest{
		MCPInvestigateQuestionRequest: MCPInvestigateQuestionRequest{
			Question:  "Is pull request 532 ready to merge?",
			Synthesis: ContextFabricSynthesisModeClient,
		},
		Interpretation: json.RawMessage(`{"shape":"open"}`),
		Contract: ContextFabricInterpretationContract{
			ModelOutputVersion: "context-fabric-model-output.v8",
			PromptVersion:      "context-fabric-interpretation.v23",
			SystemSHA256:       strings.Repeat("a", 64),
		},
		ClientModel:     "example-model",
		SynthesisOutput: json.RawMessage(`{"answer":"open"}`),
		SynthesisContract: &MCPSynthesisContract{
			ModelOutputVersion: "context-fabric-synthesis-output.v3",
			PromptVersion:      "context-fabric-synthesis.v17",
			SystemSHA256:       strings.Repeat("c", 64),
			InputSHA256:        strings.Repeat("d", 64),
		},
	}
}

func TestMCPWriteBackValidate(t *testing.T) {
	t.Parallel()
	atBound := json.RawMessage(`{"k":"` + strings.Repeat("a", ContextFabricSuppliedSynthesisMaxBytes-8) + `"}`)
	cases := []struct {
		name   string
		mutate func(*MCPInvestigateWithInterpretationRequest)
		valid  bool
	}{
		{"both present with synthesis client", func(*MCPInvestigateWithInterpretationRequest) {}, true},
		{"both absent", func(r *MCPInvestigateWithInterpretationRequest) {
			r.SynthesisOutput, r.SynthesisContract = nil, nil
		}, true},
		{"both absent, no synthesis", func(r *MCPInvestigateWithInterpretationRequest) {
			r.SynthesisOutput, r.SynthesisContract, r.Synthesis = nil, nil, ""
		}, true},
		{"output without contract", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisContract = nil }, false},
		{"contract without output", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisOutput = nil }, false},
		{"both present, synthesis absent", func(r *MCPInvestigateWithInterpretationRequest) { r.Synthesis = "" }, false},
		{"both present, synthesis server", func(r *MCPInvestigateWithInterpretationRequest) { r.Synthesis = ContextFabricSynthesisModeServer }, false},
		{"output is an array", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisOutput = json.RawMessage(`[]`) }, false},
		{"output is not JSON", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisOutput = json.RawMessage(`{"a":`) }, false},
		{"output at the bound", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisOutput = atBound }, true},
		{"output over the bound", func(r *MCPInvestigateWithInterpretationRequest) {
			r.SynthesisOutput = append(json.RawMessage(" "), atBound...)
		}, false},
		{"model output version missing", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisContract.ModelOutputVersion = "" }, false},
		{"prompt version missing", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisContract.PromptVersion = "" }, false},
		{"system sha256 missing", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisContract.SystemSHA256 = "" }, false},
		{"input sha256 missing", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisContract.InputSHA256 = "" }, false},
		{"input sha256 uppercase", func(r *MCPInvestigateWithInterpretationRequest) {
			r.SynthesisContract.InputSHA256 = strings.Repeat("D", 64)
		}, false},
		{"system sha256 short", func(r *MCPInvestigateWithInterpretationRequest) { r.SynthesisContract.SystemSHA256 = "abc" }, false},
		{"prompt version over 256", func(r *MCPInvestigateWithInterpretationRequest) {
			r.SynthesisContract.PromptVersion = strings.Repeat("v", 257)
		}, false},
		{"client model bad", func(r *MCPInvestigateWithInterpretationRequest) { r.ClientModel = "a b" }, false},
		{"no client model", func(r *MCPInvestigateWithInterpretationRequest) { r.ClientModel = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := validMCPWriteBackFixture()
			contract := *request.SynthesisContract
			request.SynthesisContract = &contract
			tc.mutate(&request)
			if err := request.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestMCPWriteBackBuildsTheHostedDraft(t *testing.T) {
	t.Parallel()
	request := validMCPWriteBackFixture()
	supplied := request.SuppliedSynthesis()
	if supplied == nil {
		t.Fatal("SuppliedSynthesis() = nil for a write-back")
	}
	want := ContextFabricSuppliedSynthesis{
		Output:             json.RawMessage(`{"answer":"open"}`),
		ModelOutputVersion: "context-fabric-synthesis-output.v3",
		PromptVersion:      "context-fabric-synthesis.v17",
		SystemSHA256:       strings.Repeat("c", 64),
		InputSHA256:        strings.Repeat("d", 64),
		ClientModel:        "example-model",
	}
	if !reflect.DeepEqual(*supplied, want) {
		t.Fatalf("SuppliedSynthesis() = %+v, want %+v", *supplied, want)
	}
	request.ClientModel = ""
	if got := request.SuppliedSynthesis(); got == nil || got.ClientModel != "" {
		t.Fatalf("an undeclared client model must stay empty, got %+v", got)
	}
	request.SynthesisOutput, request.SynthesisContract = nil, nil
	if got := request.SuppliedSynthesis(); got != nil {
		t.Fatalf("SuppliedSynthesis() = %+v without write-back arguments, want nil", got)
	}
}

func TestMCPArgumentsCarrySynthesisWriteBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"neither", `{"question":"q","synthesis":"client"}`, false},
		{"output", `{"question":"q","synthesis_output":{}}`, true},
		{"contract", `{"question":"q","synthesis_contract":{}}`, true},
		{"both", `{"question":"q","synthesis_output":{},"synthesis_contract":{}}`, true},
		{"null value still carried", `{"question":"q","synthesis_output":null}`, true},
		{"only a similar key", `{"question":"q","synthesis_outputs":{}}`, false},
		{"not an object", `[]`, false},
		{"not JSON", `{`, false},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		if got := MCPArgumentsCarrySynthesisWriteBack([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestWriteBackArgumentsAreAdvertisedOnlyByTheInterpretationTool(t *testing.T) {
	t.Parallel()
	properties := func(file string) map[string]json.RawMessage {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jsonschema", "v1", file))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Properties
	}
	question := properties("mcp_investigate_question_request.v1.schema.json")
	interpretation := properties("mcp_investigate_with_interpretation_request.v1.schema.json")
	for _, name := range []string{"synthesis_output", "synthesis_contract"} {
		if _, ok := question[name]; ok {
			t.Errorf("investigate_question advertises %s", name)
		}
		if _, ok := interpretation[name]; !ok {
			t.Errorf("investigate_with_interpretation does not advertise %s", name)
		}
	}
}
