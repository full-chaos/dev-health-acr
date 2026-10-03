package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestEverySuppliedInterpretationOutcomeCertifiesAgainstTheDeclaration drives
// the real SuppliedInterpreter through a real JSON slog handler, once per
// outcome, and certifies the bytes it wrote against the declared event. The
// outcomes it drives must be exactly the declared vocabulary.
func TestEverySuppliedInterpretationOutcomeCertifiesAgainstTheDeclaration(t *testing.T) {
	t.Parallel()
	validatorRejected := validInterpretationOutput()
	validatorRejected.RequestedJudgment = ""
	scenarios := []struct {
		name    string
		request func(t *testing.T, s *SuppliedInterpreter) contextfabric.InvestigationRequest
		want    map[string]any
		slice   []any
	}{
		{
			name: "accepted",
			request: func(t *testing.T, s *SuppliedInterpreter) contextfabric.InvestigationRequest {
				request := suppliedRequestFor(s, mustMarshalOutput(t, validInterpretationOutput()))
				request.SuppliedInterpretation.ClientModel = "claude-test"
				return request
			},
			want:  map[string]any{"outcome": "success", "client_model": "claude-test", "schema_error_type": "", "rejection_reason": ""},
			slice: []any{},
		},
		{
			name: "refused by the output schema",
			request: func(t *testing.T, s *SuppliedInterpreter) contextfabric.InvestigationRequest {
				return suppliedRequestFor(s, []byte(`{"shape":"open"}`))
			},
			want:  map[string]any{"outcome": "invalid_output", "client_model": "undeclared", "schema_error_type": "required", "rejection_reason": "unclassified"},
			slice: []any{},
		},
		{
			name: "refused by the validator",
			request: func(t *testing.T, s *SuppliedInterpreter) contextfabric.InvestigationRequest {
				return suppliedRequestFor(s, mustMarshalOutput(t, validatorRejected))
			},
			want:  map[string]any{"outcome": "invalid_output", "client_model": "undeclared", "schema_error_type": "", "rejection_reason": "requested_judgment_invalid"},
			slice: []any{},
		},
		{
			name: "refused for its contract",
			request: func(t *testing.T, s *SuppliedInterpreter) contextfabric.InvestigationRequest {
				request := suppliedRequestFor(s, mustMarshalOutput(t, validInterpretationOutput()))
				request.SuppliedInterpretation.PromptVersion = "interpret-v0"
				request.SuppliedInterpretation.ModelOutputVersion = "schema-v0"
				return request
			},
			want:  map[string]any{"outcome": "contract_mismatch", "client_model": "undeclared", "schema_error_type": "", "rejection_reason": ""},
			slice: []any{"model_output_version", "prompt_version"},
		},
		{
			name: "refused as a request",
			request: func(t *testing.T, s *SuppliedInterpreter) contextfabric.InvestigationRequest {
				request := suppliedRequestFor(s, mustMarshalOutput(t, validInterpretationOutput()))
				request.SuppliedInterpretation.ClientModel = "not a\nmodel"
				return request
			},
			want:  map[string]any{"outcome": "request_invalid", "client_model": "", "schema_error_type": "", "rejection_reason": ""},
			slice: []any{},
		},
	}
	var driven []string
	for _, scenario := range scenarios {
		var buffer bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
		supplied := mustSuppliedInterpreter(t, logger)
		request := scenario.request(t, supplied)
		_, _, _ = supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)

		log, err := certify.Parse(buffer.Bytes())
		if err != nil {
			t.Fatalf("%s: certify.Parse() on the emitted log: %v", scenario.name, err)
		}
		want := map[string]any{
			"request_id": request.RequestID, "interpretation_source": "client",
			"prompt_version": "interpret-v1", "model_output_version": "schema-v1",
		}
		for key, value := range scenario.want {
			want[key] = value
		}
		result, err := certify.Certify(log, certify.Assertion{Event: eventspec.SuppliedInterpretationDecision, Want: want})
		if err != nil {
			t.Fatalf("%s: the emitted line failed certification: %v\n%s", scenario.name, err, buffer.String())
		}
		if got, _ := result.Line["contract_mismatch"].([]any); !reflect.DeepEqual(got, scenario.slice) {
			t.Fatalf("%s: contract_mismatch = %#v, want %#v", scenario.name, result.Line["contract_mismatch"], scenario.slice)
		}
		if hash, _ := result.Line["org_id_hash"].(string); hash == "" || hash == "org_1" {
			t.Fatalf("%s: org_id_hash = %q, want a digest and never the organization id", scenario.name, hash)
		}
		driven = append(driven, scenario.want["outcome"].(string))
	}
	seen := map[string]bool{}
	for _, outcome := range driven {
		seen[outcome] = true
	}
	var distinct []string
	for outcome := range seen {
		distinct = append(distinct, outcome)
	}
	sort.Strings(distinct)
	declared := append([]string(nil), eventspec.SuppliedInterpretationOutcomeVocabulary()...)
	sort.Strings(declared)
	if !reflect.DeepEqual(distinct, declared) {
		t.Fatalf("outcomes driven = %v, declared vocabulary = %v: every declared outcome needs a real producer run, and no run may emit an undeclared one", distinct, declared)
	}
}

// TestModelInterpretationEmitsNoSuppliedInterpretationLine certifies the
// absence: a request the model interprets writes no supplied-interpretation
// decision line.
func TestModelInterpretationEmitsNoSuppliedInterpretationLine(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	model := mustRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{Logger: logger})
	request := validRequest()
	if _, _, err := model.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	log, err := certify.Parse(buffer.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if lines := log.LinesWithMsg(decisionEventMessage); len(lines) != 1 {
		t.Fatalf("model decision lines = %d, want 1: the control must have logged", len(lines))
	}
	if lines := log.LinesWithMsg(eventspec.SuppliedInterpretationDecision.Msg); len(lines) != 0 {
		raw, _ := json.Marshal(lines)
		t.Fatalf("supplied-interpretation lines on the model path = %s, want none", raw)
	}
}
