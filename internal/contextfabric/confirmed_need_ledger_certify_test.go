package contextfabric_test

// The confirmed-need-ledger line, certified against its eventspec
// declaration from the bytes the production slog JSON handler wrote:
// identity, level, every declared field's presence and closed vocabulary,
// and that the declared keys and the emitted keys are the SAME set, in
// both directions, on a real production emission -- the same discipline
// frame_validation_certify_test.go and completeness_authority_certify_test.go
// already apply to their own lines. This file builds the event by struct
// literal (proving the declaration and the certifier agree with each
// other); confirmed_need_ledger_real_producer_eventspec_certify_test.go
// certifies the SAME declaration against real Engine.Investigate turns.

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

// confirmedNeedLedgerTestRequestID derives a wire-valid request id from
// seed, matching frameValidationTestRequestID's own reasoning.
func confirmedNeedLedgerTestRequestID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "req_" + hex.EncodeToString(sum[:])[:32]
}

// recordConfirmedNeedLedgerJSON emits event through the real production
// sink and returns the raw JSON bytes.
func recordConfirmedNeedLedgerJSON(requestID string, event contextfabric.ConfirmedNeedLedgerEvent) []byte {
	ctx := observability.WithRequestID(context.Background(), requestID)
	var buf bytes.Buffer
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		RecordConfirmedNeedLedger(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

// baseConfirmedNeedLedgerEvent is a non-trivial hit line: every field this
// sweep does not itself vary carries a real, non-zero value where one
// exists, so a setter that forgets to touch a field is not masked by
// every other field already being at its zero value. CaptureSkipReason
// and AnchorAgreement are set explicitly to their own named
// "nothing happened yet" members -- unlike the kind/basis/disposition/
// decision fields below, their Go zero value ("") is not itself a
// declared member (RecordConfirmedNeedLedger's production call sites
// never leave them at the raw zero value either -- engine.go initializes
// both to their NotApplicable member before the deferred emit can ever
// fire on an early exit).
func baseConfirmedNeedLedgerEvent() contextfabric.ConfirmedNeedLedgerEvent {
	return contextfabric.ConfirmedNeedLedgerEvent{
		Outcome:                     contextfabric.ConfirmedNeedLedgerHit,
		SourceResultID:              "result_parent_1",
		AppliedMembers:              []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AppliedAnchorKind:           contractsv1.ContextFabricSubjectRepository,
		AppliedAnchorValueHash:      "abc123",
		AppliedAnchorBasis:          contextfabric.ConfirmedNeedBasisEngineCommitted,
		Dropped:                     nil,
		AnchorAgreement:             contextfabric.ConfirmedAnchorAgreementAgree,
		AnchorDisposition:           contractsv1.ContextFabricStructureDispositionApplied,
		CaptureDecision:             contextfabric.CountPopulationScopeAnchorCommitted,
		CaptureSkipReason:           contextfabric.CaptureSkipReasonNotApplicable,
		SubstitutionGuard:           contextfabric.SubjectSubstitutionSameSubject,
		SubstitutionOrigin:          contextfabric.SubjectSubstitutionOriginResolver,
		SubstitutionParentKind:      contractsv1.ContextFabricSubjectRepository,
		SubstitutionParentID:        "repository:parent",
		SubstitutionCommittedIDs:    []string{"repository:repository:parent"},
		SubstitutionRememberedCheck: contextfabric.SubjectSubstitutionRememberedNotChecked,
	}
}

// TestTheConfirmedNeedLedgerLineDeclaresExactlyTheEmittedKeys pins that
// every declared key reaches the line and every emitted key is declared,
// in both directions, on a real production emission.
func TestTheConfirmedNeedLedgerLineDeclaresExactlyTheEmittedKeys(t *testing.T) {
	t.Parallel()
	requestID := confirmedNeedLedgerTestRequestID("declared_keys")
	raw := recordConfirmedNeedLedgerJSON(requestID, baseConfirmedNeedLedgerEvent())
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	lines := log.LinesWithMsg(eventspec.ConfirmedNeedLedger.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d confirmed need ledger lines, want 1", len(lines))
	}
	line := lines[0]
	if line["request_id"] != requestID {
		t.Fatalf("request_id = %v, want %s", line["request_id"], requestID)
	}
	declared := map[string]bool{}
	for _, field := range eventspec.ConfirmedNeedLedger.Fields {
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
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.ConfirmedNeedLedger.ID)
		}
	}
}

// TestEveryClosedValueOnTheConfirmedNeedLedgerLineCertifies sweeps every
// member of every closed string vocabulary on the line through the real
// production sink -- including members no production call site exercises
// today -- the same discipline
// TestEveryClosedValueOnTheFrameValidationLineCertifies already applies to
// its own line.
func TestEveryClosedValueOnTheConfirmedNeedLedgerLineCertifies(t *testing.T) {
	t.Parallel()
	base := baseConfirmedNeedLedgerEvent()

	set := map[string]func(*contextfabric.ConfirmedNeedLedgerEvent, string){
		"outcome": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.Outcome = contextfabric.ConfirmedNeedLedgerOutcome(v)
		},
		"applied_expected_kind": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AppliedExpectedKind = contractsv1.ContextFabricSubjectKind(v)
		},
		"applied_anchor_kind": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AppliedAnchorKind = contractsv1.ContextFabricSubjectKind(v)
		},
		"applied_anchor_basis": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AppliedAnchorBasis = contextfabric.ConfirmedNeedBasis(v)
		},
		"applied_candidate_kind": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AppliedCandidateKind = contractsv1.ContextFabricSubjectKind(v)
		},
		"applied_handle_kind": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AppliedHandleKind = contractsv1.ContextFabricSubjectKind(v)
		},
		"anchor_agreement": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AnchorAgreement = contextfabric.ConfirmedAnchorAgreement(v)
		},
		"anchor_disposition": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.AnchorDisposition = contractsv1.ContextFabricStructureDisposition(v)
		},
		"capture_decision": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.CaptureDecision = contextfabric.CountPopulationScopeDecision(v)
		},
		"capture_skip_reason": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.CaptureSkipReason = contextfabric.CaptureSkipReason(v)
		},
		"substitution_guard": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.SubstitutionGuard = contextfabric.SubjectSubstitutionOutcome(v)
		},
		"substitution_origin": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.SubstitutionOrigin = contextfabric.SubjectSubstitutionOrigin(v)
		},
		"substitution_remembered_check": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.SubstitutionRememberedCheck = contextfabric.SubjectSubstitutionRememberedCheck(v)
		},
		"substitution_parent_kind": func(e *contextfabric.ConfirmedNeedLedgerEvent, v string) {
			e.SubstitutionParentKind = contractsv1.ContextFabricSubjectKind(v)
		},
	}

	closed := 0
	for _, field := range eventspec.ConfirmedNeedLedger.Fields {
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
			requestID := confirmedNeedLedgerTestRequestID("sweep_" + field.Key + "_" + member)
			raw := recordConfirmedNeedLedgerJSON(requestID, event)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.ConfirmedNeedLedger,
				Want:  map[string]any{"org_id": "org_1", field.Key: member},
			}); err != nil {
				t.Fatalf("%s=%q: the certifier refused a real production line: %v", field.Key, member, err)
			}
		}
	}
	if closed != len(set) {
		t.Fatalf("the specification declares %d swept closed fields, this sweep sets %d", closed, len(set))
	}
}
