package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestClientSynthesisDecisionLineMatchesItsDeclaration(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithRequestID(context.Background(), "req_81020000000000000000000000000000")
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&output, nil))).
		RecordClientSynthesisDecision(ctx, storage.Principal{OrgID: "org_8102"}, contextfabric.ClientSynthesisDecisionEvent{
			Outcome: contextfabric.ClientSynthesisServed, Status: contextfabric.InvestigationPartial,
			BundleBytes: 4096, MaxBytes: 131072, Bounded: true, FactsRead: 40, FactsGiven: 12, CommitsRetracted: 1,
		})

	parsed, err := certify.Parse(output.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() = %v", err)
	}
	want := map[string]any{
		"request_id": "req_81020000000000000000000000000000", "org_id": "org_8102", "outcome": "served", "status": "partial",
		"bundle_bytes": 4096, "max_bytes": 131072, "bounded": true, "facts_read": 40, "facts_given": 12, "commits_retracted": 1,
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.ClientSynthesisDecision, Want: want}); err != nil {
		t.Fatalf("certify the decision line: %v", err)
	}
	if eventspec.ClientSynthesisDecision.Msg != contextfabric.ClientSynthesisDecisionLogMessage {
		t.Fatalf("declared message %q differs from the production constant %q", eventspec.ClientSynthesisDecision.Msg, contextfabric.ClientSynthesisDecisionLogMessage)
	}
	lines := parsed.LinesWithMsg(eventspec.ClientSynthesisDecision.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d decision lines, want one", len(lines))
	}
	declared := map[string]bool{}
	for _, field := range eventspec.ClientSynthesisDecision.Fields {
		declared[field.Key] = true
	}
	for key := range lines[0] {
		switch key {
		case "time", "level", "msg":
			continue
		}
		if !declared[key] {
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.ClientSynthesisDecision.ID)
		}
	}
}

func TestClientSynthesisOutcomesAreTheDeclaredVocabulary(t *testing.T) {
	declared := eventspec.ClientSynthesisOutcomeVocabulary()
	want := []string{
		string(contextfabric.ClientSynthesisServed), string(contextfabric.ClientSynthesisUnavailable), string(contextfabric.ClientSynthesisInputTooLarge),
	}
	if len(declared) != len(want) {
		t.Fatalf("declared outcomes = %v, want %v", declared, want)
	}
	for index := range want {
		if declared[index] != want[index] {
			t.Fatalf("declared outcomes = %v, want %v", declared, want)
		}
	}
}
