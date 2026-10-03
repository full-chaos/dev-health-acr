package contextfabric

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func newCardinalityTurnEngine(t *testing.T, client bool) (*Engine, *clientRuntime) {
	t.Helper()
	engine, runtime, _ := newFrameTurnEngine(t, client, countingFrame(SubjectTeam), false)
	return engine, runtime
}

func newFrameTurnEngine(t *testing.T, client bool, frame *QuestionFrame, symmetric bool) (*Engine, *clientRuntime, *resultStoreStub) {
	t.Helper()
	anchor := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}
	runtime := &clientRuntime{draft: SynthesisDraft{
		Status: InvestigationComplete, DirectJudgment: "Counted.", CurrentState: "Nominal.",
		StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
		ReadinessGaps: []Finding{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts: []ClaimedFact{}, DeterministicAnswer: "Counted.", Warnings: []string{},
	}}
	store := &resultStoreStub{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: runtime,
		Options: RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
	}
	if client {
		synthesizer.ClientSynthesis = &ClientSynthesisAssembly{
			PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")),
			Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
		}
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "count",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{
				Frame: frame, FrameObligations: frame.Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
			},
		},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{scopeAnchorMatch(anchor)}, Committed: []SubjectRef{anchor}},
			bases:      provenCommitBases(anchor),
			context: GraphContext{
				Cohort: countingCohort(SubjectTeam, 3), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{{
					Kind: FactHealth, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:COUNTED_A", Label: "Counted A"},
					Fields: map[string]FactValue{"severity": StringFactValue("high")}, EvidenceRefIDs: []string{"evidence_counted_a"},
				}},
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer:  synthesizer,
		Results:      store,
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_50210002" },

		ServerCompletenessAuthoritySymmetricEnabled: symmetric,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, runtime, store
}

func runCardinalityTurn(t *testing.T, engine *Engine, client bool) InvestigationResult {
	result, _ := runTurnWithInput(t, engine, client)
	return result
}

func runTurnWithInput(t *testing.T, engine *Engine, client bool) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput) {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_50210002"
	request.Question = "count question"
	if client {
		request.SynthesisMode = SynthesisModeClient
	}
	ctx, collected := WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, collected()
}

func cardinalityClaimPresent(result InvestigationResult) bool {
	for _, claim := range result.ClaimedFacts {
		if strings.HasPrefix(claim.ClaimID, cardinalityClaimIDPrefix) {
			return true
		}
	}
	return false
}

func TestClientSynthesisCountQuestionKeepsTheFixedAnswerAndTheCountClaim(t *testing.T) {
	t.Parallel()
	engine, runtime := newCardinalityTurnEngine(t, true)
	result := runCardinalityTurn(t, engine, true)
	if runtime.synthCalls != 0 {
		t.Fatalf("synthesize calls = %d, want 0", runtime.synthCalls)
	}
	if result.Status != InvestigationPartial || result.Versions.SynthesisSource != SynthesisSourceClient {
		t.Fatalf("status = %q synthesis_source = %q, want partial and client", result.Status, result.Versions.SynthesisSource)
	}
	for name, got := range map[string]string{"direct_judgment": result.DirectJudgment, "current_state": result.CurrentState, "deterministic_answer": result.DeterministicAnswer} {
		if got != contractsv1.ContextFabricClientSynthesisAnswer {
			t.Errorf("%s = %q, want the fixed client synthesis answer", name, got)
		}
	}
	if !cardinalityClaimPresent(result) {
		t.Error("the count claim is missing from claimed_facts")
	}
}

func TestServerSynthesisCountQuestionStillAppendsTheCountSentence(t *testing.T) {
	t.Parallel()
	engine, _ := newCardinalityTurnEngine(t, false)
	result := runCardinalityTurn(t, engine, false)
	want := cardinalityAnswerSentence(MembershipCardinality{Resolved: true, Kind: SubjectTeam, Served: 3, Declared: 3})
	if !strings.Contains(result.DeterministicAnswer, want) {
		t.Errorf("deterministic_answer = %q, want it to carry %q", result.DeterministicAnswer, want)
	}
	if !cardinalityClaimPresent(result) {
		t.Error("the count claim is missing from claimed_facts")
	}
}
