package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// suppliedTestConfig names the same contract mustRuntime gives a Runtime, so
// the two paths are compared under one prompt and one output version.
func suppliedTestConfig(logger *slog.Logger) SuppliedInterpreterConfig {
	return SuppliedInterpreterConfig{
		InterpretationPromptVersion: "interpret-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		MaxInputBytes: 128 << 10, Logger: logger,
	}
}

func mustSuppliedInterpreter(t *testing.T, logger *slog.Logger) *SuppliedInterpreter {
	t.Helper()
	supplied, err := NewSuppliedInterpreter(suppliedTestConfig(logger))
	if err != nil {
		t.Fatalf("NewSuppliedInterpreter() error = %v", err)
	}
	fixed := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	supplied.now = func() time.Time { return fixed }
	return supplied
}

// suppliedRequestFor returns validRequest carrying raw as an interpretation
// made under the interpreter's own contract.
func suppliedRequestFor(supplied *SuppliedInterpreter, raw []byte) contextfabric.InvestigationRequest {
	request := validRequest()
	contract := supplied.Contract()
	request.SuppliedInterpretation = &contextfabric.SuppliedInterpretation{
		Output: raw, ModelOutputVersion: contract.ModelOutputVersion,
		PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
	}
	return request
}

// onlySuppliedDecision returns the one supplied-interpretation decision line
// the capture logger recorded.
func onlySuppliedDecision(t *testing.T, h *captureLogger) decisionRecord {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []decisionRecord
	for _, r := range h.records {
		if r.Message == eventspec.SuppliedInterpretationDecision.Msg {
			out = append(out, r)
		}
	}
	if len(out) != 1 || len(h.records) != 1 {
		t.Fatalf("log lines = %#v, want exactly one, the supplied-interpretation decision", h.records)
	}
	return out[0]
}

func mustMarshalOutput(t *testing.T, output interpretationOutput) []byte {
	t.Helper()
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal interpretation output: %v", err)
	}
	return raw
}

// richInterpretationOutput sets every signal the three sanitizers read,
// including one out-of-vocabulary pick, so a path that skipped a sanitizer
// would differ from one that ran it.
func richInterpretationOutput() interpretationOutput {
	output := validInterpretationOutput()
	output.WindowClass = "trend_assessment"
	output.WindowConfidence = "high"
	output.QuestionFamily = "not_a_family"
	output.GroupKind = "team"
	output.ScopeAnchorTerm = "Project Alpha"
	output.ScopeAnchorKind = "project"
	output.RequestedSubjectKind = "work_item"
	output.QuestionFrame = &questionFrameOutput{
		Goals: []string{"assess_state"},
		SubjectExpression: &subjectExpressionOutput{
			Kind: "children_of_scope", AnchorTerms: []string{"Project Alpha"},
			MemberKind: "work_item", MemberQualifier: "status",
		},
	}
	return output
}

func TestSuppliedInterpretationEqualsTheModelPathForTheSameOutput(t *testing.T) {
	t.Parallel()
	output := richInterpretationOutput()
	principal := storage.Principal{OrgID: "org_1"}

	model := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	wantQuestion, wantReceipt, err := model.InterpretQuestion(context.Background(), principal, validRequest())
	if err != nil {
		t.Fatalf("model path: InterpretQuestion() error = %v", err)
	}

	supplied := mustSuppliedInterpreter(t, nil)
	request := suppliedRequestFor(supplied, mustMarshalOutput(t, output))
	request.SuppliedInterpretation.ClientModel = "claude-test"
	gotQuestion, gotReceipt, err := supplied.InterpretSuppliedQuestion(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("supplied path: InterpretSuppliedQuestion() error = %v", err)
	}

	if !reflect.DeepEqual(gotQuestion, wantQuestion) {
		t.Fatalf("interpreted question differs\nsupplied: %#v\nmodel:    %#v", gotQuestion, wantQuestion)
	}
	if gotReceipt.Provider != contractsv1.ContextFabricClientSuppliedProvider || gotReceipt.Model != "claude-test" {
		t.Fatalf("supplied receipt identity = %q/%q, want client-supplied/claude-test", gotReceipt.Provider, gotReceipt.Model)
	}
	if wantReceipt.QuestionFrame == nil || wantReceipt.GroupKind == "" || wantReceipt.WindowClass == "" || !wantReceipt.QuestionFamilyUnrecognized {
		t.Fatalf("model receipt = %#v, want the frame, family and window captures set: the fixture must exercise all three sanitizers", wantReceipt)
	}
	// Every other receipt field must be equal: the digests, the versions and
	// all three sanitizer captures.
	normalized := gotReceipt
	normalized.Provider, normalized.Model, normalized.ModelVersion = wantReceipt.Provider, wantReceipt.Model, wantReceipt.ModelVersion
	normalized.StartedAt, normalized.CompletedAt, normalized.Usage = wantReceipt.StartedAt, wantReceipt.CompletedAt, wantReceipt.Usage
	if !reflect.DeepEqual(normalized, wantReceipt) {
		t.Fatalf("receipt differs beyond the model identity\nsupplied: %#v\nmodel:    %#v", normalized, wantReceipt)
	}
	if err := gotReceipt.Validate(); err != nil {
		t.Fatalf("supplied receipt does not validate: %v", err)
	}
}

func TestSuppliedInterpretationWithoutAClientModelIsUndeclared(t *testing.T) {
	t.Parallel()
	supplied := mustSuppliedInterpreter(t, nil)
	request := suppliedRequestFor(supplied, mustMarshalOutput(t, validInterpretationOutput()))
	_, receipt, err := supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("InterpretSuppliedQuestion() error = %v", err)
	}
	if receipt.Model != contractsv1.ContextFabricClientModelUndeclared {
		t.Fatalf("receipt.Model = %q, want %q", receipt.Model, contractsv1.ContextFabricClientModelUndeclared)
	}
}

func TestSuppliedInterpretationContractMismatchIsRefusedWithTheCurrentContract(t *testing.T) {
	t.Parallel()
	const otherSHA = "0000000000000000000000000000000000000000000000000000000000000000"
	cases := []struct {
		name   string
		mutate func(*contextfabric.SuppliedInterpretation)
		want   []string
	}{
		{"model output version", func(s *contextfabric.SuppliedInterpretation) { s.ModelOutputVersion = "schema-v0" }, []string{"model_output_version"}},
		{"prompt version", func(s *contextfabric.SuppliedInterpretation) { s.PromptVersion = "interpret-v0" }, []string{"prompt_version"}},
		{"system sha256", func(s *contextfabric.SuppliedInterpretation) { s.SystemSHA256 = otherSHA }, []string{"system_sha256"}},
		{"all three", func(s *contextfabric.SuppliedInterpretation) {
			s.ModelOutputVersion, s.PromptVersion, s.SystemSHA256 = "schema-v0", "interpret-v0", otherSHA
		}, []string{"model_output_version", "prompt_version", "system_sha256"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler, logger := newCaptureLogger()
			supplied := mustSuppliedInterpreter(t, logger)
			request := suppliedRequestFor(supplied, mustMarshalOutput(t, validInterpretationOutput()))
			tc.mutate(request.SuppliedInterpretation)

			_, receipt, err := supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			var mismatch *contextfabric.SuppliedInterpretationContractMismatch
			if !errors.As(err, &mismatch) {
				t.Fatalf("error = %v, want a contract mismatch", err)
			}
			if !reflect.DeepEqual(mismatch.Refusal.Mismatch, tc.want) {
				t.Fatalf("mismatch = %v, want %v", mismatch.Refusal.Mismatch, tc.want)
			}
			if mismatch.Refusal.Current != supplied.Contract() {
				t.Fatalf("current = %#v, want the service's own contract %#v", mismatch.Refusal.Current, supplied.Contract())
			}
			if err := mismatch.Refusal.Validate(); err != nil {
				t.Fatalf("refusal does not validate: %v", err)
			}
			if errors.Is(err, contextfabric.ErrInterpretationRejected) {
				t.Fatal("a contract mismatch must not read as a rejected interpretation: nothing was interpreted")
			}
			if receipt.Operation != "" {
				t.Fatalf("receipt = %#v, want none: no interpretation was evaluated", receipt)
			}
			attrs := onlySuppliedDecision(t, handler).Attrs
			if got := attrString(t, attrs, "outcome"); got != "contract_mismatch" {
				t.Fatalf("decision outcome = %q, want contract_mismatch", got)
			}
			if got, _ := attrs["contract_mismatch"].([]string); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decision contract_mismatch = %#v, want %v", attrs["contract_mismatch"], tc.want)
			}
		})
	}
}

func TestSuppliedInterpretationWithoutASystemSHAIsServed(t *testing.T) {
	t.Parallel()
	supplied := mustSuppliedInterpreter(t, nil)
	request := suppliedRequestFor(supplied, mustMarshalOutput(t, validInterpretationOutput()))
	request.SuppliedInterpretation.SystemSHA256 = ""
	if _, _, err := supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("InterpretSuppliedQuestion() error = %v, want an omitted system_sha256 to be accepted", err)
	}
}

func TestSuppliedInterpretationThatBreaksTheOutputSchemaIsRefused(t *testing.T) {
	t.Parallel()
	const marker = "PLANTED_CLIENT_TEXT"
	valid := func(t *testing.T) map[string]any {
		t.Helper()
		var object map[string]any
		if err := json.Unmarshal(mustMarshalOutput(t, richInterpretationOutput()), &object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown top-level field", func(o map[string]any) { o[marker] = marker }},
		{"unknown nested field", func(o map[string]any) { o["question_frame"].(map[string]any)[marker] = marker }},
		{"unknown time context field", func(o map[string]any) { o["time_context"].(map[string]any)[marker] = marker }},
		{"wrong type", func(o map[string]any) { o["subject_terms"] = marker }},
		{"missing required field", func(o map[string]any) { delete(o, "shape") }},
		{"enum violation", func(o map[string]any) { o["shape"] = marker }},
		{"axis without its bound", func(o map[string]any) { o["time_context"] = map[string]any{"axis": "range"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler, logger := newCaptureLogger()
			supplied := mustSuppliedInterpreter(t, logger)
			object := valid(t)
			tc.mutate(object)
			raw, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			_, receipt, err := supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, suppliedRequestFor(supplied, raw))
			if !errors.Is(err, contextfabric.ErrInterpretationRejected) || !errors.Is(err, contextfabric.ErrModelOutput) {
				t.Fatalf("error = %v, want a rejected interpretation", err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("error text carries the client's own text: %q", err.Error())
			}
			if receipt.Outcome != "invalid_output" || receipt.InterpretationRejectionReason != contextfabric.InterpretationRejectionUnclassified {
				t.Fatalf("receipt outcome = %q reason = %q, want invalid_output/unclassified", receipt.Outcome, receipt.InterpretationRejectionReason)
			}
			if err := receipt.Validate(); err != nil {
				t.Fatalf("rejected receipt does not validate: %v", err)
			}
			event := onlySuppliedDecision(t, handler)
			if got := attrString(t, event.Attrs, "outcome"); got != "invalid_output" {
				t.Fatalf("decision outcome = %q, want invalid_output", got)
			}
			if attrString(t, event.Attrs, "schema_error_type") == "" {
				t.Fatal("decision line names no schema_error_type")
			}
			for key, value := range event.Attrs {
				if text := fmt.Sprint(value); strings.Contains(text, marker) {
					t.Fatalf("decision attribute %q carries the client's own text: %q", key, text)
				}
			}
		})
	}
}

func TestSuppliedInterpretationValidatorRejectionNamesTheRuleTheModelPathNames(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.ClarificationNeeded = true
	principal := storage.Principal{OrgID: "org_1"}

	model := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, _, modelErr := model.InterpretQuestion(context.Background(), principal, validRequest())
	want := contextfabric.InterpretationRejectionReasonOf(modelErr)
	if !errors.Is(modelErr, contextfabric.ErrInterpretationRejected) || want == contextfabric.InterpretationRejectionUnclassified {
		t.Fatalf("model path error = %v (reason %q), want a classified rejection: the fixture must trip a validator rule", modelErr, want)
	}

	supplied := mustSuppliedInterpreter(t, nil)
	_, receipt, err := supplied.InterpretSuppliedQuestion(context.Background(), principal, suppliedRequestFor(supplied, mustMarshalOutput(t, output)))
	if !errors.Is(err, contextfabric.ErrInterpretationRejected) {
		t.Fatalf("supplied path error = %v, want a rejected interpretation", err)
	}
	if got := contextfabric.InterpretationRejectionReasonOf(err); got != want {
		t.Fatalf("supplied path rejection reason = %q, model path = %q", got, want)
	}
	if receipt.Outcome != "invalid_output" || receipt.InterpretationRejectionReason != want {
		t.Fatalf("receipt outcome = %q reason = %q, want invalid_output/%q", receipt.Outcome, receipt.InterpretationRejectionReason, want)
	}
}

func TestInterpretDecisionLineNamesTheInterpretationSource(t *testing.T) {
	t.Parallel()
	const marker = "PLANTED_SUBJECT_TERM"
	output := validInterpretationOutput()
	output.SubjectTerms = []string{marker}
	principal := storage.Principal{OrgID: "org_1"}

	modelHandler, modelLogger := newCaptureLogger()
	model := mustRuntime(t, &generatorStub{interpretation: output}, Config{Logger: modelLogger})
	if _, _, err := model.InterpretQuestion(context.Background(), principal, validRequest()); err != nil {
		t.Fatalf("model path error = %v", err)
	}
	if got := attrString(t, onlyDecisionEvent(t, modelHandler).Attrs, "interpretation_source"); got != "server" {
		t.Fatalf("model path interpretation_source = %q, want server", got)
	}

	suppliedHandler, suppliedLogger := newCaptureLogger()
	supplied := mustSuppliedInterpreter(t, suppliedLogger)
	request := suppliedRequestFor(supplied, mustMarshalOutput(t, output))
	request.SuppliedInterpretation.ClientModel = "claude-test"
	if _, _, err := supplied.InterpretSuppliedQuestion(context.Background(), principal, request); err != nil {
		t.Fatalf("supplied path error = %v", err)
	}
	event := onlySuppliedDecision(t, suppliedHandler)
	for key, want := range map[string]string{
		"interpretation_source": "client", "outcome": "success", "client_model": "claude-test",
		"prompt_version": "interpret-v1", "model_output_version": "schema-v1",
	} {
		if got := attrString(t, event.Attrs, key); got != want {
			t.Fatalf("supplied path %s = %q, want %q", key, got, want)
		}
	}
	for key, value := range event.Attrs {
		if text := fmt.Sprint(value); strings.Contains(text, marker) {
			t.Fatalf("decision attribute %q carries interpretation text: %q", key, text)
		}
	}
}

func TestSuppliedInterpreterDefaultsToTheContractTheRuntimeRuns(t *testing.T) {
	t.Parallel()
	supplied, err := NewSuppliedInterpreter(SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatalf("NewSuppliedInterpreter() error = %v", err)
	}
	want := contextfabric.InterpretationContract{
		ModelOutputVersion: DefaultSchemaVersion, PromptVersion: DefaultInterpretationPromptVersion,
		SystemSHA256: contextfabric.DigestModelValue([]byte(InterpretationSystemPrompt())),
	}
	if supplied.Contract() != want {
		t.Fatalf("Contract() = %#v, want %#v", supplied.Contract(), want)
	}
}

// TestSuppliedDecodeRefusesUnknownFieldsAndTrailingDataWithoutTheSchema pins
// the strict decode by itself: with a schema that admits anything, an unknown
// field and a second document are still refused.
func TestSuppliedDecodeRefusesUnknownFieldsAndTrailingDataWithoutTheSchema(t *testing.T) {
	t.Parallel()
	permissive, err := gojsonschema.NewSchema(gojsonschema.NewStringLoader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	supplied := &SuppliedInterpreter{schema: permissive}
	valid := string(mustMarshalOutput(t, validInterpretationOutput()))
	if _, _, err := supplied.decode([]byte(valid)); err != nil {
		t.Fatalf("decode of a valid output error = %v", err)
	}
	unknown := strings.Replace(valid, `{`, `{"not_a_field":1,`, 1)
	if _, _, err := supplied.decode([]byte(unknown)); err == nil {
		t.Fatal("an unknown field decoded, want it refused")
	}
	if _, _, err := supplied.decode([]byte(valid + valid)); err == nil {
		t.Fatal("two documents decoded, want the trailing one refused")
	}
}
