package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestSubjectlessTerminalReason is the direct, engine-free proof of
// CHAOS-3888's terminal_reason classification: an authz-filtered-to-empty
// resolution must be distinguishable from a true empty pool, and neither is
// conflated with the ambiguous (one-or-more-uncommitted-candidates) case.
func TestSubjectlessTerminalReason(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	cases := []struct {
		name                          string
		resolution                    SubjectResolution
		subjectCandidatesAuthzDropped int
		want                          string
	}{
		{
			name:       "empty pool, nothing authz-dropped",
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			want:       "empty_pool",
		},
		{
			name:                          "empty pool, something authz-dropped",
			resolution:                    SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			subjectCandidatesAuthzDropped: 1,
			want:                          "authz_filtered_to_empty",
		},
		{
			name: "non-empty pool is ambiguous regardless of authz drops",
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{{Subject: project, Confidence: 0.5}},
				Committed:  []SubjectRef{},
			},
			subjectCandidatesAuthzDropped: 3,
			want:                          "ambiguous",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subjectlessTerminalReason(tc.resolution, tc.subjectCandidatesAuthzDropped); got != tc.want {
				t.Fatalf("subjectlessTerminalReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// authzDroppingGraphReader is a GraphReader stub that, when configured,
// calls RecordSubjectCandidatesAuthzDropped on the SAME ctx ResolveSubjects
// received -- exactly the shape a real GraphReader implementation
// (falkorgraph.Adapter) uses, so this test exercises the actual ctx-recorder
// plumbing Engine.Investigate wires around the ResolveSubjects call, not
// just the pure classification function.
type authzDroppingGraphReader struct {
	resolution SubjectResolution
	dropped    int
}

func (g authzDroppingGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "authz-dropping-key", Epoch: 0}, nil
}

func (g authzDroppingGraphReader) ResolveSubjects(ctx context.Context, _ storage.Principal, _ InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, error) {
	if g.dropped > 0 {
		RecordSubjectCandidatesAuthzDropped(ctx, g.dropped)
	}
	return g.resolution, StructureOfferMaterial{}, nil
}

func (g authzDroppingGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return GraphContext{Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}, nil
}

func mustEngineForTerminalReasonTest(t *testing.T, graph GraphReader, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called on the subjectless terminal path")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called on the subjectless terminal path")
			return InvestigationResult{}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_terminal_01" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestEngineInvestigate_DistinguishesAuthzFilteredToEmptyFromTrueEmptyPool
// is the end-to-end proof CHAOS-3888 requires: the SAME empty
// SubjectResolution, once with a GraphReader that reported an
// authorization-dropped candidate and once without, must produce
// DIFFERENT RecordSubjectlessTerminal reasons -- proving the ctx-recorder
// seam (withSubjectCandidatesAuthzDroppedRecorder /
// RecordSubjectCandidatesAuthzDropped) actually carries the signal from
// GraphReader.ResolveSubjects through to Engine's own terminal-result
// telemetry, not just that the pure classification function is correct in
// isolation.
func TestEngineInvestigate_DistinguishesAuthzFilteredToEmptyFromTrueEmptyPool(t *testing.T) {
	t.Parallel()
	emptyResolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}

	truePoolTelemetry := &recordingTelemetry{}
	trueEmptyEngine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: emptyResolution}, truePoolTelemetry)
	if _, err := trueEmptyEngine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if want := []string{"empty_pool"}; !stringSlicesEqual(truePoolTelemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v", truePoolTelemetry.subjectlessTerminalReasons, want)
	}

	authzFilteredTelemetry := &recordingTelemetry{}
	authzFilteredEngine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: emptyResolution, dropped: 2}, authzFilteredTelemetry)
	if _, err := authzFilteredEngine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if want := []string{"authz_filtered_to_empty"}; !stringSlicesEqual(authzFilteredTelemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v", authzFilteredTelemetry.subjectlessTerminalReasons, want)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAC_3888_ReuseHitRecordsBothServedAndCurrentRequestIDs proves the
// answer_reuse.go fix: on a reuse hit, telemetry must carry BOTH the served
// (stored) request id and whether it differs from the CURRENT call's own
// request id -- without touching the response body, which (AC-3782-2,
// unchanged by this ticket) still serves the stored RequestID verbatim.
func TestAC_3888_ReuseHitRecordsBothServedAndCurrentRequestIDs(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		Telemetry: telemetry,
	})

	request := validInvestigationRequest()
	if request.RequestID == candidate.RequestID {
		t.Fatalf("request.RequestID = %q, want a DIFFERENT id than the stored candidate's %q (otherwise this test cannot distinguish served from current)", request.RequestID, candidate.RequestID)
	}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// The response body contract is untouched: RequestID is the STORED
	// (served) one, never this call's own -- AC-3782-2, unchanged.
	if result.RequestID != candidate.RequestID {
		t.Fatalf("result.RequestID = %q, want the served/stored id %q (AC-3782-2 must not change)", result.RequestID, candidate.RequestID)
	}
	if len(telemetry.answerReuseServedRequestIDs) != 1 {
		t.Fatalf("answerReuseServedRequestIDs = %#v, want exactly one recorded call", telemetry.answerReuseServedRequestIDs)
	}
	got := telemetry.answerReuseServedRequestIDs[0]
	if got.servedRequestID != candidate.RequestID {
		t.Fatalf("servedRequestID = %q, want the stored candidate's own request id %q", got.servedRequestID, candidate.RequestID)
	}
	if !got.requestIDMismatch {
		t.Fatalf("requestIDMismatch = false, want true: served id %q and current request id %q differ", got.servedRequestID, request.RequestID)
	}
}
