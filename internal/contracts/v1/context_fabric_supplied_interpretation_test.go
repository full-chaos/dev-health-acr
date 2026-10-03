package v1

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func validSuppliedInterpretationFixture() ContextFabricSuppliedInterpretation {
	return ContextFabricSuppliedInterpretation{
		Output:             json.RawMessage(`{"shape":"open"}`),
		ModelOutputVersion: "context-fabric-model-output.v8",
		PromptVersion:      "context-fabric-interpretation.v21",
		SystemSHA256:       strings.Repeat("a", 64),
		ClientModel:        "anthropic/claude-test:1.0_a-b",
	}
}

func TestSuppliedInterpretationValidate(t *testing.T) {
	t.Parallel()
	atBound := json.RawMessage(`{"k":"` + strings.Repeat("a", ContextFabricSuppliedInterpretationMaxBytes-8) + `"}`)
	if len(atBound) != ContextFabricSuppliedInterpretationMaxBytes {
		t.Fatalf("fixture is %d bytes, want exactly the bound %d", len(atBound), ContextFabricSuppliedInterpretationMaxBytes)
	}
	cases := []struct {
		name   string
		mutate func(*ContextFabricSuppliedInterpretation)
		valid  bool
	}{
		{"complete", func(*ContextFabricSuppliedInterpretation) {}, true},
		{"no system sha256 and no client model", func(s *ContextFabricSuppliedInterpretation) { s.SystemSHA256, s.ClientModel = "", "" }, true},
		{"output at the byte bound", func(s *ContextFabricSuppliedInterpretation) { s.Output = atBound }, true},
		{"output one byte over the bound", func(s *ContextFabricSuppliedInterpretation) { s.Output = append(json.RawMessage(" "), atBound...) }, false},
		{"output absent", func(s *ContextFabricSuppliedInterpretation) { s.Output = nil }, false},
		{"output is an array", func(s *ContextFabricSuppliedInterpretation) { s.Output = json.RawMessage(`[]`) }, false},
		{"output is a string", func(s *ContextFabricSuppliedInterpretation) { s.Output = json.RawMessage(`"open"`) }, false},
		{"output is two documents", func(s *ContextFabricSuppliedInterpretation) { s.Output = json.RawMessage(`{}{}`) }, false},
		{"output is not JSON", func(s *ContextFabricSuppliedInterpretation) { s.Output = json.RawMessage(`{"shape":`) }, false},
		{"model output version absent", func(s *ContextFabricSuppliedInterpretation) { s.ModelOutputVersion = "" }, false},
		{"prompt version absent", func(s *ContextFabricSuppliedInterpretation) { s.PromptVersion = "" }, false},
		{"prompt version padded", func(s *ContextFabricSuppliedInterpretation) { s.PromptVersion = " v21" }, false},
		{"prompt version over 256", func(s *ContextFabricSuppliedInterpretation) { s.PromptVersion = strings.Repeat("v", 257) }, false},
		{"system sha256 uppercase", func(s *ContextFabricSuppliedInterpretation) { s.SystemSHA256 = strings.Repeat("A", 64) }, false},
		{"system sha256 short", func(s *ContextFabricSuppliedInterpretation) { s.SystemSHA256 = strings.Repeat("a", 63) }, false},
		{"client model with a space", func(s *ContextFabricSuppliedInterpretation) { s.ClientModel = "claude test" }, false},
		{"client model with a newline", func(s *ContextFabricSuppliedInterpretation) { s.ClientModel = "claude\ntest" }, false},
		{"client model over 128", func(s *ContextFabricSuppliedInterpretation) { s.ClientModel = strings.Repeat("m", 129) }, false},
		{"client model at 128", func(s *ContextFabricSuppliedInterpretation) { s.ClientModel = strings.Repeat("m", 128) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			supplied := validSuppliedInterpretationFixture()
			tc.mutate(&supplied)
			if err := supplied.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", err, tc.valid)
			}
		})
	}
}

func TestClientSuppliedModelIdentity(t *testing.T) {
	t.Parallel()
	if got := ContextFabricClientSuppliedModelIdentity(""); got != "client-supplied/undeclared" {
		t.Fatalf("identity with no declared model = %q", got)
	}
	longest := ContextFabricClientSuppliedModelIdentity(strings.Repeat("m", ContextFabricClientModelMaxLength))
	if !validModelIdentity(longest) || !IsContextFabricClientSuppliedModelIdentity(longest) {
		t.Fatalf("the longest client identity (%d bytes) must be a valid, client-supplied model identity", len(longest))
	}
	if IsContextFabricClientSuppliedModelIdentity("openai-compatible/client-supplied") {
		t.Fatal("a server identity that only mentions client-supplied in its model half must not read as client-supplied")
	}
}

func TestInterpretationProvenancePairsTheSourceWithTheIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		source   ContextFabricInterpretationSource
		identity string
		valid    bool
	}{
		{"absent", "", "", true},
		{"server with identity", ContextFabricInterpretationSourceServer, "openai-compatible/gpt-test", true},
		{"server with no identity", ContextFabricInterpretationSourceServer, "", true},
		{"client with client identity", ContextFabricInterpretationSourceClient, "client-supplied/undeclared", true},
		{"client with no identity", ContextFabricInterpretationSourceClient, "", false},
		{"client with a server identity", ContextFabricInterpretationSourceClient, "openai-compatible/gpt-test", false},
		{"server with a client identity", ContextFabricInterpretationSourceServer, "client-supplied/claude-test", false},
		{"no source with a client identity", "", "client-supplied/claude-test", false},
		{"unknown source", "model", "", false},
		{"padded identity", ContextFabricInterpretationSourceServer, " openai-compatible/gpt-test", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateInterpretationProvenance(tc.source, tc.identity); (err == nil) != tc.valid {
				t.Fatalf("validateInterpretationProvenance(%q, %q) error = %v, want valid = %t", tc.source, tc.identity, err, tc.valid)
			}
		})
	}
}

func TestInterpretationContractRefusalValidate(t *testing.T) {
	t.Parallel()
	valid := func() ContextFabricInterpretationContractRefusal {
		return ContextFabricInterpretationContractRefusal{
			Mismatch: []string{ContextFabricInterpretationContractFieldPromptVersion},
			Current: ContextFabricInterpretationContract{
				ModelOutputVersion: "context-fabric-model-output.v8", PromptVersion: "context-fabric-interpretation.v21",
				SystemSHA256: strings.Repeat("a", 64),
			},
		}
	}
	cases := []struct {
		name   string
		mutate func(*ContextFabricInterpretationContractRefusal)
		valid  bool
	}{
		{"one field", func(*ContextFabricInterpretationContractRefusal) {}, true},
		{"all three fields", func(r *ContextFabricInterpretationContractRefusal) {
			r.Mismatch = []string{"model_output_version", "prompt_version", "system_sha256"}
		}, true},
		{"no field", func(r *ContextFabricInterpretationContractRefusal) { r.Mismatch = nil }, false},
		{"unknown field", func(r *ContextFabricInterpretationContractRefusal) { r.Mismatch = []string{"service_version"} }, false},
		{"repeated field", func(r *ContextFabricInterpretationContractRefusal) {
			r.Mismatch = []string{"prompt_version", "prompt_version"}
		}, false},
		{"current sha256 absent", func(r *ContextFabricInterpretationContractRefusal) { r.Current.SystemSHA256 = "" }, false},
		{"current prompt version absent", func(r *ContextFabricInterpretationContractRefusal) { r.Current.PromptVersion = "" }, false},
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

func TestInvestigateWithInterpretationRequestDecodesFlatAndStrict(t *testing.T) {
	t.Parallel()
	decode := func(raw string) (MCPInvestigateWithInterpretationRequest, error) {
		var request MCPInvestigateWithInterpretationRequest
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&request)
		return request, err
	}
	const arguments = `{"question":"What is the status of Ask Dev?","parent_result_id":"result_12345678","interpretation":{"shape":"open"},"contract":{"model_output_version":"context-fabric-model-output.v8","prompt_version":"context-fabric-interpretation.v21"},"client_model":"claude-test"}`
	request, err := decode(arguments)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if request.Question != "What is the status of Ask Dev?" || request.ParentResultID != "result_12345678" {
		t.Fatalf("request = %#v, want the investigate_question arguments at the top level", request)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	supplied := request.Supplied()
	if string(supplied.Output) != `{"shape":"open"}` || supplied.ClientModel != "claude-test" || supplied.PromptVersion != "context-fabric-interpretation.v21" {
		t.Fatalf("Supplied() = %#v, want the tool's interpretation arguments", supplied)
	}
	if _, err := decode(strings.Replace(arguments, `"client_model"`, `"service_version":"x","client_model"`, 1)); err == nil {
		t.Fatal("an unknown top-level argument decoded, want it refused")
	}
	if _, err := decode(strings.Replace(arguments, `"prompt_version"`, `"service_version":"x","prompt_version"`, 1)); err == nil {
		t.Fatal("an unknown contract key decoded, want it refused")
	}
	missing, err := decode(`{"question":"What is the status of Ask Dev?"}`)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if err := missing.Validate(); err == nil {
		t.Fatal("a request with no interpretation validated, want it refused")
	}
}
