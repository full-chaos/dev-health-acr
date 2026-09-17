package contextfabric

// One test per exit class: every return in Engine.Investigate between the
// confirmed-need ledger's own resolution and the capture check
// (engineCommittedAnchorForCapture) gets its own CaptureSkipReason. Each test
// drives the real Engine.Investigate through the production JSON sink
// (NewSlogEngineTelemetry, the exact
// construction internal/runtime/hosted's composition root uses) and asserts
// the emitted "context fabric confirmed need ledger" line's
// capture_skip_reason key -- the same class of pin
// TestRecordConfirmedNeedLedger_CaptureSkipReasonEmittedLines applies to the
// emitter directly, applied here to the real exit site that assigns it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// jsonLedgerTelemetry builds the production JSON sink and a buffer to read
// it back from.
func jsonLedgerTelemetry() (EngineTelemetry, *bytes.Buffer) {
	var buf bytes.Buffer
	return NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))), &buf
}

// assertCaptureSkipReasonJSON reads every captured line, keeps the LAST one
// whose msg is the confirmed-need ledger line (a multi-turn fixture emits one
// per turn; the turn under test is always the last), and asserts its
// capture_skip_reason at production Info level.
func assertCaptureSkipReasonJSON(t *testing.T, buf *bytes.Buffer, want CaptureSkipReason) {
	t.Helper()
	var line map[string]any
	found := false
	for _, raw := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("captured line is not JSON: %v -- line: %s", err, raw)
		}
		if rec["msg"] == "context fabric confirmed need ledger" {
			line, found = rec, true
		}
	}
	if !found {
		t.Fatalf("no confirmed need ledger JSON line captured; buffer:\n%s", buf.String())
	}
	if level, _ := line["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO -- the reason must survive the production log level", level)
	}
	got, _ := line["capture_skip_reason"].(string)
	if got != string(want) {
		t.Errorf("capture_skip_reason = %q, want %q -- the emitted production JSON line", got, want)
	}
	// The closed-vocabulary guard itself, not merely incidental to matching
	// `want`: this exit's REAL, driven-through-Investigate emission must be a
	// declared member, the same assertion captureSkipReasons() exists to make
	// checkable. A mutation that swaps this exit's assignment for a valid-Go,
	// undeclared CaptureSkipReason value fails HERE independently of the
	// equality check above.
	if !ValidCaptureSkipReason(CaptureSkipReason(got)) {
		t.Errorf("capture_skip_reason = %q is not a declared member of captureSkipReasons() -- this real exit emitted an undeclared value", got)
	}
}

// swapToJSONLedgerTelemetry replaces a needTurnHarness engine's telemetry
// with the production JSON sink -- called AFTER any setup turns the fixture
// needs (those still record through the harness's own recordingTelemetry so
// h.turn()'s bookkeeping keeps working), and BEFORE the one turn under test,
// driven directly through h.engine.Investigate.
func swapToJSONLedgerTelemetry(h *needTurnHarness) *bytes.Buffer {
	telemetry, buf := jsonLedgerTelemetry()
	h.engine.telemetry = telemetry
	return buf
}

// TestCaptureSkipReasonWindowVetoed: a request-side window receipt that
// fails pre-Interpret canonicalization short-circuits above tryReuse and
// Interpret -- no subject resolution of any kind is ever attempted.
func TestCaptureSkipReasonWindowVetoed(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5844_window_veto_one", false), committingNeedResponse())
	buf := swapToJSONLedgerTelemetry(h)
	request := continuingNeedTurn(needTurnRequest("request_5844_window_veto_two", false), one.result.ResultID)
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "winr_5844missing001"}}
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonWindowVetoed)
}

// TestCaptureSkipReasonWindowConfirmationRequired: an MCP caller's bare
// explicit evidence_window field is gated before tryReuse and Interpret --
// no subject resolution of any kind is ever attempted.
func TestCaptureSkipReasonWindowConfirmationRequired(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	buf := swapToJSONLedgerTelemetry(h)
	request := needTurnRequest("request_5844_window_confirmation_required", false)
	request.Consumer = ConsumerInfo{Name: "test", Version: "1.0.0", Surface: "mcp"}
	request.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonWindowConfirmationRequired)
}

// TestCaptureSkipReasonStructureVetoed: a structure receipt that fails
// pre-Interpret canonicalization short-circuits above tryReuse and
// Interpret -- no subject resolution of any kind is ever attempted.
func TestCaptureSkipReasonStructureVetoed(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5844_structure_veto_one", false), committingNeedResponse())
	buf := swapToJSONLedgerTelemetry(h)
	request := continuingNeedTurn(needTurnRequest("request_5844_structure_veto_two", false), one.result.ResultID)
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "kindr_5844missing01"}}
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonStructureVetoed)
}

// TestCaptureSkipReasonWindowAxisConflict: a confirmed evidence window no
// longer applies once Interpret moves the question to a non-current axis --
// distinct from the pre-Interpret window veto above, this veto fires AFTER
// Interpret and planning and can only be reached once they have run, but
// this turn's own subject resolution still never runs.
func TestCaptureSkipReasonWindowAxisConflict(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one, option := windowTurnOne(t, h, "request_5844_axis_conflict_one")
	h.historical = true
	buf := swapToJSONLedgerTelemetry(h)
	request := needTurnRequest("request_5844_axis_conflict_two", false)
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
	// A parent reference takes this turn outside the window-only continuation
	// shape, whose carried axis would otherwise override the moved one.
	request.ParentResultID = one.result.ResultID
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonWindowAxisConflict)
}

// TestCaptureSkipReasonInterpretationFailed: the QuestionInterpreter
// returned an error -- there is no interpreted question for ResolveSubjects
// to run against, so it never ran.
func TestCaptureSkipReasonInterpretationFailed(t *testing.T) {
	t.Parallel()
	telemetry, buf := jsonLedgerTelemetry()
	engine, err := NewEngine(EngineDependencies{
		Graph: bindingOnlyGraphReader{t: t},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{}, errors.New("interpreter failure (fixture)")
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached")
			return InvestigationResult{}, nil
		}),
		Results:   &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}},
		Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_5844_interp" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err == nil {
		t.Fatal("Investigate() error = nil, want the interpreter's own error")
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonInterpretationFailed)
}

// TestCaptureSkipReasonInterpretedTimeUnanswerable: the SECOND time-bound
// check (on the INTERPRETED question, after Interpret) finds a bound this
// engine will not answer -- this turn returns before ResolveSubjects,
// DiscoverContext, ReadFacts and Synthesize all run.
func TestCaptureSkipReasonInterpretedTimeUnanswerable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	zero := time.Time{}
	telemetry, buf := jsonLedgerTelemetry()
	engine, _ := mustHistoricalEngineWithTelemetry(t, TimeContext{Axis: TemporalValidTime, AsOf: &zero}, now, telemetry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a terminal refusal with err == nil", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonInterpretedTimeUnanswerable)
}

// TestCaptureSkipReasonContinuationRefused: an admitted window continuation
// whose carrier could not be composed into a valid frame refuses the turn
// above planning and every retrieval -- its own resolution never ran.
func TestCaptureSkipReasonContinuationRefused(t *testing.T) {
	t.Parallel()
	base := validInvestigationRequest().Question
	telemetry, buf := jsonLedgerTelemetry()
	prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
	store := newRefusalStore(withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: staleEpoch()}))
	engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, telemetry)
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), continuationRequest(base)); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonContinuationRefused)
}

// TestCaptureSkipReasonReuseServed: a stored candidate matched, passed
// re-validation, and was served unchanged -- this turn's own resolution
// never ran because no fresh investigation ran at all.
func TestCaptureSkipReasonReuseServed(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	telemetry, buf := jsonLedgerTelemetry()
	engine, err := NewEngine(EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached on a reuse hit")
			return InterpretedQuestion{}, nil
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached on a reuse hit")
			return InvestigationResult{}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_5844_reuse_served" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want true on a reuse hit")
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonReuseServed)
}

// TestCaptureSkipReasonReuseBudgetRefused: a stored candidate matched, but
// re-validation against the CURRENT response budget refused it (chris's
// promise of record) -- this turn's own resolution never ran; the row it
// would have served instead is discarded, not this turn's own committed
// subject.
func TestCaptureSkipReasonReuseBudgetRefused(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	stored, err := contractsv1.MeasureContextFabricResponse(candidate)
	if err != nil {
		t.Fatalf("measure stored row: %v", err)
	}
	maxBytes := stored.Bytes / 2
	if maxBytes < 1 || maxBytes >= stored.Bytes {
		t.Fatalf("premise: stored row is %d bytes, computed ceiling %d -- cannot straddle", stored.Bytes, maxBytes)
	}
	telemetry, buf := jsonLedgerTelemetry()
	engine, err := NewEngine(EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached on a reuse hit")
			return InterpretedQuestion{}, nil
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached on a reuse hit")
			return InvestigationResult{}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID: func() string { return "result_5844_reuse_budget" }, MaxSerializedBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err == nil {
		t.Fatal("Investigate() error = nil, want an AnswerBudgetRefusal from the reuse re-validation")
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonReuseBudgetRefused)
}

// TestCaptureSkipReasonReuseValidationError: a matched work-item-tuple reuse
// candidate whose stored coverage fails validation cannot be served -- a
// hard error distinct from an ordinary reuse miss (every ordinary miss fails
// closed to a fresh investigation with no error at all). This turn is served
// from neither the stored row nor its own resolution, which never ran.
func TestCaptureSkipReasonReuseValidationError(t *testing.T) {
	t.Parallel()
	principal, request, stored, current := tupleReuseFixture(t)
	// Coverage.Sources == nil violates v1 bounds (validateWorkItemStoredCoverage's
	// only failure trigger) -- the fixture otherwise builds a fully eligible,
	// authorized, membership-agreeing work-item-tuple reuse candidate.
	stored.Result.Coverage.Sources = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, owner := NewWorkItemResponseOwnerContext(ctx)
	defer owner.Complete()
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	telemetry, buf := jsonLedgerTelemetry()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results:   &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}},
		ReuseGate: tupleReuseGate{stored},
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, CandidateVerificationValid
		},
		WorkItemMembership: tupleMembershipFunc(func(c context.Context, p storage.Principal, r WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			lease, err := gate.Acquire(c)
			if err != nil {
				return nil, WorkItemMembershipResult{}, err
			}
			if err := owner.Retain(lease); err != nil {
				return nil, WorkItemMembershipResult{}, err
			}
			return lease, current, nil
		}),
		Telemetry: telemetry,
	})
	if _, err := engine.Investigate(ctx, principal, request); err == nil {
		t.Fatal("Investigate() error = nil, want the stored coverage validation error")
	}
	assertCaptureSkipReasonJSON(t, buf, CaptureSkipReasonReuseValidationError)
}
