package contextfabric

// The capture axis (capture_decision, capture_skip_reason, and their two
// siblings anchor_agreement/applied_anchor_basis on the SAME confirmed-need
// ledger line) already has a real producer for every one of
// captureSkipReasons()'s 15 members, distributed across
// chaos5844_capture_skip_reason_test.go (10), chaos5788_committed_anchor_carry_test.go
// (3: FrameGateRefused/GraphNotProjected/ResolutionError) and
// chaos4234_regime_a_offers_test.go (1: WindowConfirmationGatedDiscard) --
// none of them certify the emitted line against eventspec.ConfirmedNeedLedger.
// This file reuses each one's OWN rig verbatim (never a struct literal) and
// returns the raw production JSON, so
// confirmed_need_ledger_capture_axis_real_producer_eventspec_certify_test.go
// can certify the ACTUAL emitted line from each real exit, the same split
// FrameValidationRealProducerScenarios already established for the
// frame-validation line.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// jsonSlogLoggerForTest builds the production JSON handler writing into
// buf, matching jsonLedgerTelemetry's own construction, for a scenario that
// swaps an already-built engine's telemetry rather than constructing one
// fresh (chaos5788_committed_anchor_carry.go's engine builders return a
// *recordingTelemetry with no JSON-sink variant of their own).
func jsonSlogLoggerForTest(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// CaptureSkipReasonRealProducerScenarios names every CaptureSkipReason this
// file can run, in CaptureSkipReasonVocabulary's own declared order, so a
// member added there reaches this list without a second, independently
// maintained one.
func CaptureSkipReasonRealProducerScenarios() []string {
	names := make([]string, 0, len(CaptureSkipReasonVocabulary()))
	for _, reason := range CaptureSkipReasonVocabulary() {
		names = append(names, string(reason))
	}
	return names
}

// RunCaptureSkipReasonRealProducerScenarioForTest drives ONE named
// CaptureSkipReason scenario through its own already-established real
// producer and returns the production slog JSON bytes it wrote.
func RunCaptureSkipReasonRealProducerScenarioForTest(t *testing.T, scenario string) (log []byte, orgID string) {
	t.Helper()
	orgID = "org_acceptance"

	switch CaptureSkipReason(scenario) {
	case CaptureSkipReasonNotApplicable:
		// statedWindow=true: an explicit confirmed window avoids the
		// CHAOS-4234 class-default window gate (which a bare
		// needTurnRequest(..., false) turn hits BEFORE the capture check
		// ever runs, the SAME window_confirmation_gated_discard exit the
		// scenario below this one names) -- capture_decision must carry a
		// real decision here, not another skip reason.
		h := newNeedTurnHarness(t, nil)
		buf := swapToJSONLedgerTelemetry(h)
		h.turn(needTurnRequest("request_5802_not_applicable", true), committingNeedResponse())
		return buf.Bytes(), orgID

	case CaptureSkipReasonWindowVetoed:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5802_window_veto_one", false), committingNeedResponse())
		buf := swapToJSONLedgerTelemetry(h)
		request := continuingNeedTurn(needTurnRequest("request_5802_window_veto_two", false), one.result.ResultID)
		request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "winr_5802missing001"}}
		if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonWindowConfirmationRequired:
		h := newNeedTurnHarness(t, nil)
		buf := swapToJSONLedgerTelemetry(h)
		request := needTurnRequest("request_5802_window_confirmation_required", false)
		request.Consumer = ConsumerInfo{Name: "test", Version: "1.0.0", Surface: "mcp"}
		request.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
		if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonStructureVetoed:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5802_structure_veto_one", false), committingNeedResponse())
		buf := swapToJSONLedgerTelemetry(h)
		request := continuingNeedTurn(needTurnRequest("request_5802_structure_veto_two", false), one.result.ResultID)
		request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "kindr_5802missing01"}}
		if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonWindowAxisConflict:
		h := newNeedTurnHarness(t, nil)
		one, option := windowTurnOne(t, h, "request_5802_axis_conflict_one")
		h.historical = true
		buf := swapToJSONLedgerTelemetry(h)
		request := needTurnRequest("request_5802_axis_conflict_two", false)
		request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
		// A changed question: the confirmation speaks for turn one's question only.
		request.Question += " Include the drivers."
		if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonInterpretationFailed:
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
		}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_5802_interp" }})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err == nil {
			t.Fatal("Investigate() error = nil, want the interpreter's own error")
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonInterpretedTimeUnanswerable:
		now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
		zero := time.Time{}
		telemetry, buf := jsonLedgerTelemetry()
		engine, _ := mustHistoricalEngineWithTelemetry(t, TimeContext{Axis: TemporalValidTime, AsOf: &zero}, now, telemetry)
		if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: orgID}, validInvestigationRequest()); err != nil {
			t.Fatalf("Investigate() error = %v, want a terminal refusal with err == nil", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonContinuationRefused:
		base := validInvestigationRequest().Question
		telemetry, buf := jsonLedgerTelemetry()
		prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
		store := newRefusalStore(withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: staleEpoch()}))
		engine, _ := newRefusalEngine(t, store, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, telemetry)
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), continuationRequest(base)); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonReuseServed:
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
		}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_5802_reuse_served" }})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return buf.Bytes(), reusePrincipal().OrgID

	case CaptureSkipReasonReuseBudgetRefused:
		project, candidate := reusableCandidate()
		stored, err := contractsv1.MeasureContextFabricResponse(candidate)
		if err != nil {
			t.Fatalf("measure stored row: %v", err)
		}
		maxBytes := stored.Bytes / 2
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
			NewResultID: func() string { return "result_5802_reuse_budget" }, MaxSerializedBytes: maxBytes,
		})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err == nil {
			t.Fatal("Investigate() error = nil, want an AnswerBudgetRefusal from the reuse re-validation")
		}
		return buf.Bytes(), reusePrincipal().OrgID

	case CaptureSkipReasonReuseValidationError:
		principal, request, stored, current := tupleReuseFixture(t)
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
		return buf.Bytes(), principal.OrgID

	case CaptureSkipReasonWindowConfirmationGatedDiscard:
		interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
		graph := chaos4234GatedGraph()
		store := &staticResultStore{results: map[string]InvestigationResult{}}
		telemetry, buf := jsonLedgerTelemetry()
		engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err != nil {
			t.Fatalf("Investigate() error = %v, want a confirmation-required result", err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonFrameGateRefused:
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_framegate_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		two := needTurnRequest("request_5802_framegate_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		engine.interpreter = twoTurnInterpreter{byRequestID: map[string]twoTurnInterpretation{
			one.RequestID: {
				interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
				outcome: QuestionFamilyOutcome{
					Frame: committedAnchorFrame(), FrameObligations: committedAnchorFrame().Obligations,
					Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
				},
			},
			two.RequestID: {
				interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
				outcome: QuestionFamilyOutcome{
					Frame: committedAnchorFrame(), FrameObligations: committedAnchorFrame().Obligations,
					Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
					Gate: FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam},
				},
			},
		}}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), two); err != nil {
			t.Fatalf("Investigate(%s) error = %v", two.RequestID, err)
		}
		return buf.Bytes(), orgID

	case CaptureSkipReasonGraphNotProjected:
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_notprojected_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		two := needTurnRequest("request_5802_notprojected_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		twoResponse := needTurnResponse{err: fmt.Errorf("query context graph: %w", ErrGraphNotProjected)}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		committedAnchorTurn(t, engine, graph, store, two, twoResponse)
		return buf.Bytes(), orgID

	case CaptureSkipReasonResolutionError:
		engine, graph, store := buildCommittedAnchorEngine(t)
		one := needTurnRequest("request_5802_resolutionerror_one", true)
		oneResponse := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{committedAnchorRepo}}, bases: provenCommitBases(committedAnchorRepo)}
		oneResult, _ := committedAnchorTurn(t, engine, graph, store, one, oneResponse)

		two := needTurnRequest("request_5802_resolutionerror_two", true)
		two.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		two = continuingNeedTurn(two, oneResult.ResultID)
		graph.response = needTurnResponse{err: fmt.Errorf("authorization scope lookup failed")}
		var buf bytes.Buffer
		engine.telemetry = NewSlogEngineTelemetry(jsonSlogLoggerForTest(&buf))
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), two); err == nil {
			t.Fatal("Investigate() error = nil, want the resolve-subjects error this fixture is built to trigger")
		}
		return buf.Bytes(), orgID

	default:
		t.Fatalf("unknown CaptureSkipReasonRealProducer scenario %q", scenario)
		return nil, ""
	}
}
