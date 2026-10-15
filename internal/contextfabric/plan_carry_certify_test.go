package contextfabric_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const planCarryTestRequestID = "req_0123456789abcdef0123456789abcdef"

// planCarryLegacyKeys and planCarryOutcomeLegacyKeys are the key sequences
// the two lines carried before they were declared in eventspec. They are
// literal on purpose: a declaration edit must not be able to move them.
var (
	planCarryLegacyKeys = []string{
		"time", "level", "msg",
		"org_id", "family_replaced", "source_replaced", "family_carried", "source_result_id",
		"family_source", "route_class", "route_disposition", "route_switched", "request_id",
	}
	planCarryOutcomeLegacyKeys = []string{
		"time", "level", "msg",
		"org_id", "outcome", "source_result_id", "seed_source", "request_id",
	}
)

func planCarryTelemetry(buf *bytes.Buffer) contextfabric.SlogEngineTelemetry {
	return contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func planCarryContext() context.Context {
	return observability.WithRequestID(context.Background(), planCarryTestRequestID)
}

func jsonKeyOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("value: %v", err)
		}
	}
	return keys
}

func recordPlanCarry(buf *bytes.Buffer) {
	families := contextfabric.QuestionFamilyVocabulary()
	planCarryTelemetry(buf).RecordPlanCarry(planCarryContext(), storage.Principal{OrgID: "org_1"}, contextfabric.PlanCarryEvent{
		FamilyReplaced: "",
		SourceReplaced: contextfabric.QuestionFamilySource("model_plurality_rejected"),
		FamilyCarried:  families[0],
		SourceResultID: "res_parent",
		Route: contextfabric.FamilyRouteDecision{
			Family:      families[0],
			Source:      contextfabric.FamilyRouteSource("projected"),
			Class:       contextfabric.FamilyAgreementClass("agreed"),
			Disposition: contextfabric.FamilyRouteDisposition("route_from_projection"),
			Switched:    true,
		},
	})
}

func TestPlanCarryLinePinsItsLegacyKeysAndCertifies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	recordPlanCarry(&buf)
	if got := jsonKeyOrder(t, buf.Bytes()); !reflect.DeepEqual(got, planCarryLegacyKeys) {
		t.Fatalf("plan carry key sequence = %v, want %v", got, planCarryLegacyKeys)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	lines := log.LinesWithMsg(eventspec.PlanCarry.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d plan carry lines, want 1", len(lines))
	}
	if eventspec.PlanCarry.Msg != "context fabric plan carry" {
		t.Fatalf("msg drifted: %q", eventspec.PlanCarry.Msg)
	}
	declared := eventspec.FieldKeys(eventspec.PlanCarry)
	if !reflect.DeepEqual(declared, planCarryLegacyKeys[3:]) {
		t.Fatalf("declared keys = %v, want %v", declared, planCarryLegacyKeys[3:])
	}
	for _, field := range eventspec.PlanCarry.Fields {
		if _, ok := lines[0][field.Key]; !ok {
			t.Errorf("declared key %q missing from the emitted line", field.Key)
		}
	}
	if lines[0]["request_id"] != planCarryTestRequestID {
		t.Errorf("request_id = %v", lines[0]["request_id"])
	}
}

func TestPlanCarryOutcomeLinePinsItsLegacyKeysAndEveryClosedValueCertifies(t *testing.T) {
	t.Parallel()
	outcomeField, _ := fieldOf(eventspec.PlanCarryOutcome, "outcome")
	seedField, _ := fieldOf(eventspec.PlanCarryOutcome, "seed_source")
	if eventspec.PlanCarryOutcome.Msg != "context fabric plan carry outcome" {
		t.Fatalf("msg drifted: %q", eventspec.PlanCarryOutcome.Msg)
	}
	if got := eventspec.FieldKeys(eventspec.PlanCarryOutcome); !reflect.DeepEqual(got, planCarryOutcomeLegacyKeys[3:]) {
		t.Fatalf("declared keys = %v, want %v", got, planCarryOutcomeLegacyKeys[3:])
	}
	for _, outcome := range outcomeField.ClosedVocabulary {
		for _, seed := range seedField.ClosedVocabulary {
			var buf bytes.Buffer
			planCarryTelemetry(&buf).RecordPlanCarryOutcome(planCarryContext(), storage.Principal{OrgID: "org_1"},
				contextfabric.PlanCarryOutcome(outcome), "res_parent", contextfabric.CarrySeedSource(seed))
			if got := jsonKeyOrder(t, buf.Bytes()); !reflect.DeepEqual(got, planCarryOutcomeLegacyKeys) {
				t.Fatalf("plan carry outcome key sequence = %v, want %v", got, planCarryOutcomeLegacyKeys)
			}
			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse: %v", err)
			}
			lines := log.LinesWithMsg(eventspec.PlanCarryOutcome.Msg)
			if len(lines) != 1 {
				t.Fatalf("outcome %q seed %q: got %d lines, want 1", outcome, seed, len(lines))
			}
			if lines[0]["outcome"] != outcome || lines[0]["seed_source"] != seed {
				t.Errorf("emitted outcome/seed = %v/%v, want %s/%s", lines[0]["outcome"], lines[0]["seed_source"], outcome, seed)
			}
		}
	}
}

func fieldOf(ev eventspec.Event, key string) (eventspec.Field, bool) {
	for _, field := range ev.Fields {
		if field.Key == key {
			return field, true
		}
	}
	return eventspec.Field{}, false
}
