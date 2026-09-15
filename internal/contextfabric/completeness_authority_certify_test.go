package contextfabric_test

// The completeness authority line, certified against its eventspec
// declaration from the bytes the production slog JSON handler wrote:
// identity, level, every declared field's presence, type and closed
// vocabulary, and the VALUES a real sink call carries.

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
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// completenessAuthorityTestRequestID derives a wire-valid request id
// ("req_" + 32 lowercase hex, observability.parseRequestID's own shape) from
// seed, so every scenario below gets a distinct, valid id without hand
// counting hex digits.
func completenessAuthorityTestRequestID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "req_" + hex.EncodeToString(sum[:])[:32]
}

// recordCompletenessAuthorityJSON emits event through the real production
// sink and returns the raw JSON bytes.
func recordCompletenessAuthorityJSON(requestID string, event contextfabric.CompletenessAuthorityObservation) []byte {
	ctx := observability.WithRequestID(context.Background(), requestID)
	var buf bytes.Buffer
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		RecordCompletenessAuthority(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

// TestTheCompletenessAuthorityLineCertifiesAgainstItsSpecification certifies
// one non-trivial, internally-distinct scenario per disposition/basis
// combination the derivation actually produces.
func TestTheCompletenessAuthorityLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		event    contextfabric.CompletenessAuthorityObservation
		want     map[string]any
	}{
		{"complete_to_degraded", contextfabric.CompletenessAuthorityObservation{
			ModelStatus: contextfabric.InvestigationComplete,
			Disposition: contextfabric.AnswerDispositionAnswer,
			Basis:       contextfabric.CompletenessAuthorityBasisOutcomeDerived,
			ServerState: contractsv1.ContextFabricAnswerCompletenessDegraded,
			Derived:     true, Disagreed: true, WouldFlip: true,
			Direction: contextfabric.CompletenessAuthorityDirectionCompleteToDegraded,
			Version:   contextfabric.CompletenessAuthorityVersion,
		}, map[string]any{
			"org_id": "org_1", "model_status": "complete", "disposition": "answer", "basis": "outcome_derived",
			"server_state": "degraded", "derived": true, "disagreed": true, "would_flip": true,
			"direction": "complete_to_degraded", "version": contextfabric.CompletenessAuthorityVersion,
		}},
		{"partial_to_degraded_agree", contextfabric.CompletenessAuthorityObservation{
			ModelStatus: contextfabric.InvestigationPartial,
			Disposition: contextfabric.AnswerDispositionAnswer,
			Basis:       contextfabric.CompletenessAuthorityBasisOutcomeDerived,
			ServerState: contractsv1.ContextFabricAnswerCompletenessPartial,
			Derived:     true, Disagreed: false, WouldFlip: false,
			Direction: contextfabric.CompletenessAuthorityDirectionNone,
			Version:   contextfabric.CompletenessAuthorityVersion,
		}, map[string]any{
			"model_status": "partial", "basis": "outcome_derived", "server_state": "partial",
			"derived": true, "disagreed": false, "would_flip": false, "direction": "none",
		}},
		{"degraded_to_partial", contextfabric.CompletenessAuthorityObservation{
			ModelStatus: contextfabric.InvestigationDegraded,
			Disposition: contextfabric.AnswerDispositionAnswer,
			Basis:       contextfabric.CompletenessAuthorityBasisOutcomeDerived,
			ServerState: contractsv1.ContextFabricAnswerCompletenessPartial,
			Derived:     true, Disagreed: true, WouldFlip: true,
			Direction: contextfabric.CompletenessAuthorityDirectionDegradedToPartial,
			Version:   contextfabric.CompletenessAuthorityVersion,
		}, map[string]any{
			"model_status": "degraded", "basis": "outcome_derived", "server_state": "partial",
			"derived": true, "disagreed": true, "would_flip": true, "direction": "degraded_to_partial",
		}},
		{"not_an_answer", contextfabric.CompletenessAuthorityObservation{
			ModelStatus: contextfabric.InvestigationClarificationRequired,
			Disposition: contextfabric.AnswerDispositionClarification,
			Basis:       contextfabric.CompletenessAuthorityBasisNotAnAnswer,
			Direction:   contextfabric.CompletenessAuthorityDirectionNone,
			Version:     contextfabric.CompletenessAuthorityVersion,
		}, map[string]any{
			"model_status": "clarification_required", "disposition": "clarification", "basis": "not_an_answer",
			"server_state": "", "derived": false, "disagreed": false, "would_flip": false, "direction": "none",
		}},
		{"unavailable_basis", contextfabric.CompletenessAuthorityObservation{
			ModelStatus: contextfabric.InvestigationComplete,
			Disposition: contextfabric.AnswerDispositionAnswer,
			Basis:       contextfabric.CompletenessAuthorityBasisUnavailable,
			Direction:   contextfabric.CompletenessAuthorityDirectionNone,
			Version:     contextfabric.CompletenessAuthorityVersion,
		}, map[string]any{
			"model_status": "complete", "disposition": "answer", "basis": "unavailable",
			"server_state": "", "derived": false, "disagreed": false, "would_flip": false, "direction": "none",
		}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			requestID := completenessAuthorityTestRequestID(tc.scenario)
			raw := recordCompletenessAuthorityJSON(requestID, tc.event)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			assertion := map[string]any{"request_id": requestID}
			for key, value := range tc.want {
				assertion[key] = value
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.CompletenessAuthority, Want: assertion}); err != nil {
				t.Fatalf("certify %s: %v", tc.scenario, err)
			}
		})
	}
}

// TestTheCompletenessAuthoritySpecificationDeclaresExactlyTheEmittedKeys
// pins that every declared key reaches the line and every emitted key is
// declared, in both directions, on a real production emission.
func TestTheCompletenessAuthoritySpecificationDeclaresExactlyTheEmittedKeys(t *testing.T) {
	t.Parallel()
	requestID := completenessAuthorityTestRequestID("declared_keys")
	raw := recordCompletenessAuthorityJSON(requestID, contextfabric.CompletenessAuthorityObservation{
		ModelStatus: contextfabric.InvestigationComplete,
		Disposition: contextfabric.AnswerDispositionAnswer,
		Basis:       contextfabric.CompletenessAuthorityBasisOutcomeDerived,
		ServerState: contractsv1.ContextFabricAnswerCompletenessPartial,
		Derived:     true, Disagreed: true, WouldFlip: true,
		Direction: contextfabric.CompletenessAuthorityDirectionCompleteToPartial,
		Version:   contextfabric.CompletenessAuthorityVersion,
	})
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	lines := log.LinesWithMsg(eventspec.CompletenessAuthority.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d completeness authority lines, want 1", len(lines))
	}
	line := lines[0]
	if line["request_id"] != requestID {
		t.Fatalf("request_id = %v, want %s", line["request_id"], requestID)
	}
	declared := map[string]bool{}
	for _, field := range eventspec.CompletenessAuthority.Fields {
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
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.CompletenessAuthority.ID)
		}
	}
}

// TestEveryClosedValueOnTheCompletenessAuthorityLineCertifies sweeps every
// member of every closed vocabulary on the line through the real production
// sink -- including members no production call site exercises for every
// OTHER field on the same line, so the certifier is proven against the
// value, not against a scenario chosen to make every field simultaneously
// realistic.
func TestEveryClosedValueOnTheCompletenessAuthorityLineCertifies(t *testing.T) {
	t.Parallel()
	base := contextfabric.CompletenessAuthorityObservation{
		ModelStatus: contextfabric.InvestigationComplete,
		Disposition: contextfabric.AnswerDispositionAnswer,
		Basis:       contextfabric.CompletenessAuthorityBasisOutcomeDerived,
		ServerState: contractsv1.ContextFabricAnswerCompletenessDegraded,
		Derived:     true, Disagreed: true, WouldFlip: true,
		Direction: contextfabric.CompletenessAuthorityDirectionCompleteToDegraded,
		Version:   contextfabric.CompletenessAuthorityVersion,
	}
	set := map[string]func(*contextfabric.CompletenessAuthorityObservation, string){
		"model_status": func(e *contextfabric.CompletenessAuthorityObservation, v string) {
			e.ModelStatus = contextfabric.InvestigationStatus(v)
		},
		"disposition": func(e *contextfabric.CompletenessAuthorityObservation, v string) {
			e.Disposition = contextfabric.AnswerDisposition(v)
		},
		"basis": func(e *contextfabric.CompletenessAuthorityObservation, v string) {
			e.Basis = contextfabric.CompletenessAuthorityBasis(v)
		},
		"server_state": func(e *contextfabric.CompletenessAuthorityObservation, v string) {
			e.ServerState = contractsv1.ContextFabricAnswerCompletenessState(v)
		},
		"direction": func(e *contextfabric.CompletenessAuthorityObservation, v string) {
			e.Direction = contextfabric.CompletenessAuthorityDirection(v)
		},
	}
	closed := 0
	for _, field := range eventspec.CompletenessAuthority.Fields {
		if len(field.ClosedVocabulary) == 0 {
			continue
		}
		closed++
		apply, ok := set[field.Key]
		if !ok {
			t.Fatalf("closed field %q has no setter in this sweep; add one", field.Key)
		}
		for _, member := range field.ClosedVocabulary {
			event := base
			apply(&event, member)
			requestID := completenessAuthorityTestRequestID("sweep_" + field.Key + "_" + member)
			raw := recordCompletenessAuthorityJSON(requestID, event)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.CompletenessAuthority,
				Want:  map[string]any{"request_id": requestID, field.Key: member},
			}); err != nil {
				t.Fatalf("%s=%q: the certifier refused a real production line: %v", field.Key, member, err)
			}
		}
	}
	if closed != len(set) {
		t.Fatalf("the specification declares %d closed fields, this sweep sets %d", closed, len(set))
	}
}
