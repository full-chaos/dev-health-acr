package contextfabric_test

// The settled work-item tuple admission line, certified against its
// eventspec declaration from the bytes the production slog JSON handler
// wrote: identity, level, every declared field's presence, type and closed
// vocabulary, and the VALUES a real sink call carries. Mirrors
// completeness_authority_certify_test.go's own structure for the settled
// admission line this arm added.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemTupleAdmissionTestRequestID derives a wire-valid request id
// ("req_" + 32 lowercase hex) from seed, so every scenario gets a
// distinct, valid id without hand counting hex digits.
func workItemTupleAdmissionTestRequestID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "req_" + hex.EncodeToString(sum[:])[:32]
}

// recordWorkItemTupleAdmissionJSON emits event through the real production
// sink and returns the raw JSON bytes.
func recordWorkItemTupleAdmissionJSON(requestID string, event contextfabric.WorkItemTupleAdmissionEvent) []byte {
	ctx := observability.WithRequestID(context.Background(), requestID)
	var buf bytes.Buffer
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		RecordWorkItemTupleAdmission(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

// TestTheWorkItemTupleAdmissionLineCertifiesAgainstItsSpecification
// certifies both shapes the settled decision actually produces: admitted
// with a real strip, and refused with nothing stripped.
func TestTheWorkItemTupleAdmissionLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		event    contextfabric.WorkItemTupleAdmissionEvent
		want     map[string]any
	}{
		{"admitted_with_strip", contextfabric.WorkItemTupleAdmissionEvent{
			Admitted: true, StrippedObligations: []contextfabric.AnswerObligation{contextfabric.ObligationRanking},
		}, map[string]any{"org_id": "org_1", "admitted": true, "stripped_obligations": []any{"ranking"}}},
		{"refused_no_strip", contextfabric.WorkItemTupleAdmissionEvent{
			Admitted: false, StrippedObligations: nil,
		}, map[string]any{"org_id": "org_1", "admitted": false, "stripped_obligations": []any{}}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			requestID := workItemTupleAdmissionTestRequestID(tc.scenario)
			raw := recordWorkItemTupleAdmissionJSON(requestID, tc.event)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			assertion := map[string]any{"request_id": requestID}
			for key, value := range tc.want {
				assertion[key] = value
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.WorkItemTupleAdmission, Want: assertion}); err != nil {
				t.Fatalf("certify %s: %v", tc.scenario, err)
			}
		})
	}
}

// TestTheWorkItemTupleAdmissionSpecificationDeclaresExactlyTheEmittedKeys
// pins that every declared key reaches the line and every emitted key is
// declared, in both directions, on a real production emission -- for both
// the admitted and the refused shape, since StrippedObligations's presence
// (non-empty vs empty) never removes the key itself.
func TestTheWorkItemTupleAdmissionSpecificationDeclaresExactlyTheEmittedKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		event    contextfabric.WorkItemTupleAdmissionEvent
	}{
		{"admitted", contextfabric.WorkItemTupleAdmissionEvent{Admitted: true, StrippedObligations: []contextfabric.AnswerObligation{contextfabric.ObligationRanking}}},
		{"refused", contextfabric.WorkItemTupleAdmissionEvent{Admitted: false, StrippedObligations: nil}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			requestID := workItemTupleAdmissionTestRequestID("declared_keys_" + tc.scenario)
			raw := recordWorkItemTupleAdmissionJSON(requestID, tc.event)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			lines := log.LinesWithMsg(eventspec.WorkItemTupleAdmission.Msg)
			if len(lines) != 1 {
				t.Fatalf("got %d settled-admission lines, want 1", len(lines))
			}
			line := lines[0]
			if line["request_id"] != requestID {
				t.Fatalf("request_id = %v, want %s", line["request_id"], requestID)
			}
			declared := map[string]bool{}
			for _, field := range eventspec.WorkItemTupleAdmission.Fields {
				declared[field.Key] = true
				if _, ok := line[field.Key]; !ok {
					t.Errorf("declared key %q is not on the emitted line", field.Key)
				}
			}
			for key := range line {
				switch key {
				case "time", "level", "msg":
					continue
				}
				if !declared[key] {
					t.Errorf("emitted key %q is not declared on %s", key, eventspec.WorkItemTupleAdmission.ID)
				}
			}
		})
	}
}

// TestEveryClosedValueOnTheWorkItemTupleAdmissionLineCertifies sweeps every
// member of the stripped_obligations closed vocabulary (every
// AnswerObligation, not just ranking -- the one this arm's predicate
// actually produces) through the real production sink, so the certifier is
// proven against the whole declared vocabulary, not only the value one
// production call site happens to emit.
func TestEveryClosedValueOnTheWorkItemTupleAdmissionLineCertifies(t *testing.T) {
	t.Parallel()
	var strippedField *eventspec.Field
	for i := range eventspec.WorkItemTupleAdmission.Fields {
		if eventspec.WorkItemTupleAdmission.Fields[i].Key == "stripped_obligations" {
			strippedField = &eventspec.WorkItemTupleAdmission.Fields[i]
		}
	}
	if strippedField == nil {
		t.Fatal("stripped_obligations is not declared on WorkItemTupleAdmission")
	}
	if len(strippedField.ClosedVocabulary) == 0 {
		t.Fatal("stripped_obligations declares no closed vocabulary to sweep")
	}
	for _, member := range strippedField.ClosedVocabulary {
		requestID := workItemTupleAdmissionTestRequestID("sweep_stripped_obligations_" + member)
		raw := recordWorkItemTupleAdmissionJSON(requestID, contextfabric.WorkItemTupleAdmissionEvent{
			Admitted: true, StrippedObligations: []contextfabric.AnswerObligation{contextfabric.AnswerObligation(member)},
		})
		log, err := certify.Parse(raw)
		if err != nil {
			t.Fatalf("certify.Parse(): %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.WorkItemTupleAdmission,
			Want:  map[string]any{"org_id": "org_1", "request_id": requestID, "stripped_obligations": []any{member}},
		}); err != nil {
			t.Fatalf("stripped_obligations=[%q]: the certifier refused a real production line: %v", member, err)
		}
	}
}

// TestWorkItemTupleAdmissionRejectsAnOutOfVocabularyStrippedObligation is
// the negative control the sweep above needs beside it: a value no
// production path ever emits (workItemTupleStripSurveyObligations can only
// ever return ObligationRanking) but that the wire type does not prevent a
// caller from constructing must still be REFUSED by the certifier -- proof
// that the per-element ClosedVocabulary check on a string_slice field
// (certify.go, added alongside this event: no field previously declared
// one) actually enforces, not merely declares.
func TestWorkItemTupleAdmissionRejectsAnOutOfVocabularyStrippedObligation(t *testing.T) {
	t.Parallel()
	requestID := workItemTupleAdmissionTestRequestID("out_of_vocabulary_stripped_obligation")
	raw := recordWorkItemTupleAdmissionJSON(requestID, contextfabric.WorkItemTupleAdmissionEvent{
		Admitted: true, StrippedObligations: []contextfabric.AnswerObligation{"not_a_real_obligation"},
	})
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	_, err = certify.Certify(log, certify.Assertion{
		Event: eventspec.WorkItemTupleAdmission,
		Want:  map[string]any{"org_id": "org_1", "request_id": requestID, "stripped_obligations": []any{"not_a_real_obligation"}},
	})
	if err == nil {
		t.Fatal("certify accepted an out-of-vocabulary stripped_obligations element; the per-element ClosedVocabulary check did not fire")
	}
}
