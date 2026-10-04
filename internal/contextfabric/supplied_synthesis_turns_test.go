package contextfabric

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// suppliedGateInterpreter lets an interpreter double take a supplied
// interpretation: it passes the contract gate and interprets as the double does.
type suppliedGateInterpreter struct{ QuestionInterpreter }

func (suppliedGateInterpreter) CheckSuppliedInterpretation(context.Context, storage.Principal, InvestigationRequest) error {
	return nil
}

type writeBackTurn struct {
	engine    *Engine
	parse     *draftParserStub
	store     *resultStoreStub
	telemetry *recordingTelemetry
}

func writeBackTurnSynthesizer(parse *draftParserStub, telemetry *recordingTelemetry) RuntimeAnswerSynthesizer {
	return RuntimeAnswerSynthesizer{
		Runtime: &clientRuntime{}, Sink: &fakeReceiptSink{}, Telemetry: telemetry,
		Options: RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
		ClientSynthesis: &ClientSynthesisAssembly{
			PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")),
			Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode, ParseDraft: parse.Parse,
		},
	}
}

func countedWriteBackDraft(status InvestigationStatus) SynthesisDraft {
	return SynthesisDraft{
		Status: status, DirectJudgment: "Written by the caller.", CurrentState: "Written by the caller.",
		StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
		ReadinessGaps: []Finding{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts: []ClaimedFact{}, DeterministicAnswer: "Written by the caller.", Warnings: []string{},
	}
}

// newFrameWriteBackTurn is newFrameTurnEngine for a write-back: the same
// graph, facts and frame, a supplied interpretation gate and a parse stub.
func newFrameWriteBackTurn(t *testing.T, frame *QuestionFrame, draft SynthesisDraft, symmetric, authority bool) *writeBackTurn {
	t.Helper()
	anchor := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}
	turn := &writeBackTurn{parse: &draftParserStub{draft: draft}, store: &resultStoreStub{}, telemetry: &recordingTelemetry{}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: suppliedGateInterpreter{familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "count",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
			},
			outcome: QuestionFamilyOutcome{
				Frame: frame, FrameObligations: frame.Obligations,
				Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
			},
		}},
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
		Synthesizer:  writeBackTurnSynthesizer(turn.parse, turn.telemetry),
		Results:      turn.store,
		Telemetry:    turn.telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_50210003" },

		ServerCompletenessAuthorityEnabled:          authority,
		ServerCompletenessAuthoritySymmetricEnabled: symmetric,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	turn.engine = engine
	return turn
}

// run is both calls of the flow on one engine and returns the second's result.
func (w *writeBackTurn) run(t *testing.T, question string) InvestigationResult {
	t.Helper()
	base := func() InvestigationRequest {
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestID = "request_50210003"
		request.Question = question
		request.SynthesisMode = SynthesisModeClient
		request.SuppliedInterpretation = writeBackSuppliedInterpretation()
		return request
	}
	ctx, collected := WithSynthesisInputCollector(context.Background())
	if _, err := w.engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, base()); err != nil {
		t.Fatalf("call 1: Investigate() error = %v", err)
	}
	bundle := collected()
	if bundle == nil {
		t.Fatal("call 1 delivered no synthesis input")
	}
	request := base()
	request.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
		Output: json.RawMessage(`{}`), ModelOutputVersion: bundle.Contract.ModelOutputVersion, PromptVersion: bundle.Contract.PromptVersion,
		SystemSHA256: bundle.Contract.SystemSHA256, InputSHA256: bundle.InputSHA256,
	}
	ctx, _ = WithSynthesisInputCollector(context.Background())
	result, err := w.engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("call 2: Investigate() error = %v", err)
	}
	if w.parse.calls != 1 {
		t.Fatalf("parse calls = %d, want 1", w.parse.calls)
	}
	return result
}

// E7: with an applied draft a count question carries the count sentence, as
// the model path; the no-draft turn keeps its fixed text.
func TestSuppliedSynthesisCountQuestionAppendsTheCountSentenceLikeTheModelPath(t *testing.T) {
	t.Parallel()
	turn := newFrameWriteBackTurn(t, countingFrame(SubjectTeam), countedWriteBackDraft(InvestigationComplete), false, false)
	result := turn.run(t, "count question")
	want := cardinalityAnswerSentence(MembershipCardinality{Resolved: true, Kind: SubjectTeam, Served: 3, Declared: 3})
	if !strings.HasSuffix(result.DeterministicAnswer, want) {
		t.Fatalf("deterministic_answer = %q, want it to end with the count sentence %q", result.DeterministicAnswer, want)
	}
	if result.DeterministicAnswer == contractsv1.ContextFabricClientSynthesisAnswer || !cardinalityClaimPresent(result) {
		t.Fatalf("answer = %q count claim present = %v, want the composed answer and the count claim", result.DeterministicAnswer, cardinalityClaimPresent(result))
	}
	if result.Status != InvestigationComplete || result.Versions.SynthesisSource != SynthesisSourceClient {
		t.Fatalf("status = %q source = %q, want the draft's status from the client", result.Status, result.Versions.SynthesisSource)
	}
}

// E7: with an applied draft the cohort narrative is recomposed from the status
// as on the model path; the no-draft turn keeps its fixed text.
func TestSuppliedSynthesisCohortNarrativeIsRecomposedLikeTheModelPath(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	parse := &draftParserStub{draft: countedWriteBackDraft(InvestigationPartial)}
	telemetry := &recordingTelemetry{}
	store := &resultStoreStub{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: suppliedGateInterpreter{interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		})},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Members: []CohortMember{
					{Subject: team, Rank: 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}},
				}},
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{
					{Kind: FactHealth, Subject: team, Fields: map[string]FactValue{"severity": StringFactValue("high")}},
					investmentFact("CHAOS", balancedThemes(), 0),
				},
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: writeBackTurnSynthesizer(parse, telemetry),
		Results:     store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_45800003" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	turn := &writeBackTurn{engine: engine, parse: parse, store: store, telemetry: telemetry}
	result := turn.run(t, "which teams are struggling?")
	if len(telemetry.cohortDriverNarrations) != 2 || telemetry.cohortDriverNarrations[1].JudgmentsEmitted == 0 {
		t.Fatalf("cohortDriverNarrations = %#v, want a narration with a judgment on call 2: the fixture did not exercise narration", telemetry.cohortDriverNarrations)
	}
	if telemetry.cohortDriverNarrations[0].AnswerNarrativeRecomposed || !telemetry.cohortDriverNarrations[1].AnswerNarrativeRecomposed {
		t.Fatalf("narrative recomposed = %v / %v, want false on the no-draft call and true with a draft",
			telemetry.cohortDriverNarrations[0].AnswerNarrativeRecomposed, telemetry.cohortDriverNarrations[1].AnswerNarrativeRecomposed)
	}
	wantJudgment, wantAnswer := recomposeCohortAnswerNarrative(result.Status, result.SubjectResolution)
	if result.DirectJudgment != wantJudgment || result.DeterministicAnswer != wantAnswer {
		t.Fatalf("direct_judgment = %q deterministic_answer = %q, want the recomposed %q / %q", result.DirectJudgment, result.DeterministicAnswer, wantJudgment, wantAnswer)
	}
	if result.DeterministicAnswer == "Written by the caller." || result.DeterministicAnswer == contractsv1.ContextFabricClientSynthesisAnswer {
		t.Fatalf("deterministic_answer = %q, want neither the draft's text nor the fixed client sentence", result.DeterministicAnswer)
	}
}

// E8: the completeness authority acts on a write-back result.
func TestSuppliedSynthesisResultIsSubjectToTheCompletenessAuthority(t *testing.T) {
	t.Parallel()
	frame := frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))
	cases := []struct {
		name      string
		draft     InvestigationStatus
		symmetric bool
		authority bool
		want      InvestigationStatus
	}{
		{"partial, symmetric flag on", InvestigationPartial, true, false, InvestigationDegraded},
		{"partial, symmetric flag off", InvestigationPartial, false, false, InvestigationPartial},
		{"complete, authority flag on", InvestigationComplete, false, true, InvestigationDegraded},
		{"complete, authority flag off", InvestigationComplete, false, false, InvestigationComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			turn := newFrameWriteBackTurn(t, frame, countedWriteBackDraft(tc.draft), tc.symmetric, tc.authority)
			result := turn.run(t, "count question")
			if len(result.Completeness.Outcomes) == 0 {
				t.Fatal("the turn derived no outcome rows")
			}
			for name, got := range map[string]InvestigationResult{"served": result, "saved": turn.store.saved} {
				if got.Status != tc.want {
					t.Errorf("%s: status = %q, want %q", name, got.Status, tc.want)
				}
				if got.Versions.SynthesisSource != SynthesisSourceClient {
					t.Errorf("%s: synthesis_source = %q, want client", name, got.Versions.SynthesisSource)
				}
			}
		})
	}
}
