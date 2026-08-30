package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestEngineRecomposesCohortAnswerNarrativeAfterNarration is CHAOS-4580's
// end-to-end proof: once narrateCohortDriverJudgments (called from
// engine.go, after synthesis) actually narrates at least one member, the
// engine must REPLACE the pre-narration DirectJudgment/DeterministicAnswer
// -- composed once at synthesis time, before any narrated judgment
// existed -- with recomposeCohortAnswerNarrative's one-narrative output,
// and record the decision on the SAME CohortDriverNarrationEvent telemetry
// already emits.
//
// The fake Synthesizer below returns the EXACT shape chris reported: a
// DeterministicAnswer whose "Canonical facts:" clause repeats, byte for
// byte, the same key=value list CurrentState's "Current observed values:"
// clause states, and a DirectJudgment that opens with the identical
// status+principal-driver sentence DeterministicAnswer does. Without this
// ticket's fix, both survive to result verbatim; with it, both are
// replaced before Validate/Save.
func TestEngineRecomposesCohortAnswerNarrativeAfterNarration(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"}
	cohort := &Cohort{
		Kind:      SubjectTeam,
		Rationale: "kind census match",
		Members: []CohortMember{
			{Subject: team, Rank: 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	// This ticket's own bug shape: the SAME key=value list under both
	// "Canonical facts:" (inside DeterministicAnswer) and "Current
	// observed values:" (CurrentState), and the SAME status+principal
	// sentence opening both DirectJudgment and DeterministicAnswer --
	// exactly as a pre-narration synthesis composition would have
	// produced before this fix existed.
	const duplicateFactsClause = "health.severity=high for Fullchaos"
	const statusAndPrincipalOpening = "This investigation is partial: some canonical or graph coverage was unavailable. Principal driver"
	preNarrationDirectJudgment := statusAndPrincipalOpening + ": Fullchaos: health risk."
	preNarrationDeterministicAnswer := statusAndPrincipalOpening + "(s): Fullchaos: health risk. Canonical facts: " + duplicateFactsClause + "."
	preNarrationCurrentState := "Current observed values: " + duplicateFactsClause + "."

	telemetry := &recordingTelemetry{}
	store := &resultStoreStub{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{
					{Kind: FactHealth, Subject: team, Fields: map[string]FactValue{"severity": StringFactValue("high")}},
					investmentFact("CHAOS", balancedThemes(), 0),
				},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status:              InvestigationPartial,
				DirectJudgment:      preNarrationDirectJudgment,
				CurrentState:        preNarrationCurrentState,
				DeterministicAnswer: preNarrationDeterministicAnswer,
				StrongestPressures:  []string{},
				Drivers:             []DriverJudgment{},
				RemainingWork:       []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_45800001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_45800001"
	request.Question = "which teams are struggling?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// Sanity: narration must actually have run and produced a judgment --
	// otherwise this test would trivially pass without exercising the fix.
	if len(telemetry.cohortDriverNarrations) != 1 {
		t.Fatalf("cohortDriverNarrations = %#v, want exactly 1 event", telemetry.cohortDriverNarrations)
	}
	narrationEvent := telemetry.cohortDriverNarrations[0]
	if narrationEvent.JudgmentsEmitted == 0 {
		t.Fatalf("narrationEvent = %+v, want at least one judgment emitted -- test setup did not exercise narration", narrationEvent)
	}

	// CHAOS-4580 item 2 (decision-basis telemetry, same change as the
	// branch it records): the recomposition decision must be on the record.
	if !narrationEvent.AnswerNarrativeRecomposed {
		t.Fatalf("narrationEvent.AnswerNarrativeRecomposed = false, want true (narration emitted a judgment)")
	}

	// CHAOS-4580 item 1: DirectJudgment must no longer restate the
	// principal-driver clause DeterministicAnswer carries -- it is reduced
	// to the bare status sentence (never empty: Validate() requires a
	// non-empty DirectJudgment for an answer-capable status).
	wantDirectJudgment := "This investigation is partial: some canonical or graph coverage was unavailable."
	if result.DirectJudgment != wantDirectJudgment {
		t.Fatalf("result.DirectJudgment = %q, want %q after recomposition", result.DirectJudgment, wantDirectJudgment)
	}

	// The raw key=value facts list must appear in CurrentState (untouched,
	// still exactly the pre-narration text) and NEVER inside
	// DeterministicAnswer's prose.
	if !strings.Contains(result.CurrentState, duplicateFactsClause) {
		t.Fatalf("result.CurrentState = %q, want it to still contain the canonical facts clause (untouched)", result.CurrentState)
	}
	if strings.Contains(result.DeterministicAnswer, "Canonical facts:") || strings.Contains(result.DeterministicAnswer, duplicateFactsClause) {
		t.Fatalf("result.DeterministicAnswer = %q, must never restate the raw facts list CurrentState already carries", result.DeterministicAnswer)
	}

	// The narrated principal driver's own numbered sentence must be the
	// content of DeterministicAnswer's "Principal driver(s):" clause --
	// "numbers inline", not a bare title.
	if !strings.Contains(result.DeterministicAnswer, "attention points.") {
		t.Fatalf("result.DeterministicAnswer = %q, want the narrated driver's own numbered summary sentence", result.DeterministicAnswer)
	}
	if !strings.HasPrefix(result.DeterministicAnswer, "This investigation is partial: some canonical or graph coverage was unavailable.") {
		t.Fatalf("result.DeterministicAnswer = %q, want it to still open with the status sentence", result.DeterministicAnswer)
	}
}
