package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestSuppliedSynthesisDecisionLineMatchesItsDeclaration(t *testing.T) {
	cases := map[string]contextfabric.SuppliedSynthesisDecisionEvent{
		"served": {Outcome: contextfabric.SuppliedSynthesisServed, ClientModel: "writer-1", OutputBytes: 812},
		"contract_mismatch": {
			Outcome: contextfabric.SuppliedSynthesisContractMismatch, ClientModel: "undeclared", OutputBytes: 20,
			Mismatch: []string{"prompt_version", "input_sha256"},
		},
		"rejected": {
			Outcome: contextfabric.SuppliedSynthesisRejected, ClientModel: "writer-1", OutputBytes: 99,
			RejectionReason: contextfabric.RejectionReasonOutputSchemaMismatch,
		},
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := observability.WithRequestID(context.Background(), "req_85060000000000000000000000000000")
			contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&output, nil))).
				RecordSuppliedSynthesisDecision(ctx, storage.Principal{OrgID: "org_8506"}, event)
			parsed, err := certify.Parse(output.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse() = %v", err)
			}
			mismatch := []any{}
			for _, field := range event.Mismatch {
				mismatch = append(mismatch, field)
			}
			want := map[string]any{
				"request_id": "req_85060000000000000000000000000000", "org_id": "org_8506", "outcome": string(event.Outcome),
				"contract_mismatch": mismatch, "rejection_reason": string(event.RejectionReason), "client_model": event.ClientModel, "output_bytes": event.OutputBytes,
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.SuppliedSynthesisDecision, Want: want}); err != nil {
				t.Fatalf("certify the decision line: %v", err)
			}
			lines := parsed.LinesWithMsg(eventspec.SuppliedSynthesisDecision.Msg)
			if len(lines) != 1 {
				t.Fatalf("got %d decision lines, want one", len(lines))
			}
			declared := map[string]bool{}
			for _, field := range eventspec.SuppliedSynthesisDecision.Fields {
				declared[field.Key] = true
			}
			for key := range lines[0] {
				switch key {
				case "time", "level", "msg":
					continue
				}
				if !declared[key] {
					t.Errorf("emitted key %q is not declared on %s", key, eventspec.SuppliedSynthesisDecision.ID)
				}
			}
		})
	}
	if eventspec.SuppliedSynthesisDecision.Msg != contextfabric.SuppliedSynthesisDecisionLogMessage {
		t.Fatalf("declared message %q differs from the production constant %q", eventspec.SuppliedSynthesisDecision.Msg, contextfabric.SuppliedSynthesisDecisionLogMessage)
	}
}

func TestSuppliedSynthesisOutcomesAreTheDeclaredVocabulary(t *testing.T) {
	want := []string{
		string(contextfabric.SuppliedSynthesisServed), string(contextfabric.SuppliedSynthesisContractMismatch),
		string(contextfabric.SuppliedSynthesisInterpretationRequired), string(contextfabric.SuppliedSynthesisInputChangedOutcome),
		string(contextfabric.SuppliedSynthesisRejected), string(contextfabric.SuppliedSynthesisUnavailable),
	}
	if got := eventspec.SuppliedSynthesisOutcomeVocabulary(); !reflect.DeepEqual(got, want) {
		t.Fatalf("declared outcomes = %v, want %v", got, want)
	}
	if got, want := eventspec.SuppliedSynthesisOutcomeInputChanged, contractsv1.ContextFabricSuppliedSynthesisReasonInputChanged; got != want {
		t.Fatalf("input_changed outcome %q differs from the contract reason %q", got, want)
	}
	fields := []string{
		contractsv1.ContextFabricSynthesisContractFieldModelOutputVersion, contractsv1.ContextFabricSynthesisContractFieldPromptVersion,
		contractsv1.ContextFabricSynthesisContractFieldSystemSHA256, contractsv1.ContextFabricSynthesisContractFieldInputSHA256,
	}
	if got := eventspec.SuppliedSynthesisContractFieldVocabulary(); !reflect.DeepEqual(got, fields) {
		t.Fatalf("declared mismatch fields = %v, want the contract's %v", got, fields)
	}
}
