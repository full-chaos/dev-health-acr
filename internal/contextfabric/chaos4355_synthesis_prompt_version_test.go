package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS4355_SynthesisPromptVersionBumpInvalidatesPreFixStoredAnswers is
// codex R1's regression guard (P2 finding, fixed in the same PR):
// genkitruntime.DefaultSynthesisPromptVersion moved v12 -> v13 because
// modelFacingFacts changed what synthesisInputFromDomain sends the model
// (excludes Rows-shaped canonical fields) -- a genuine, measured change to
// the bytes this call sends, not a per-request data variation. Per this
// package's own TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse
// mechanism, that change is worthless unless the version bump actually
// takes an org's PRE-fix stored answer (saved under the old
// "context-fabric-synthesis.v12" value, back when the model could still
// see Rows-shaped fields) out of reuse eligibility. This pins the LITERAL
// old value this ticket bumped away from against the LITERAL new one, so a
// revert of the bump above (with no fresh version of its own) starts
// failing this test by asserting an actual hit where it must not.
func TestCHAOS4355_SynthesisPromptVersionBumpInvalidatesPreFixStoredAnswers(t *testing.T) {
	t.Parallel()

	const (
		preFixSynthesisVersion  = "context-fabric-synthesis.v12"
		postFixSynthesisVersion = "context-fabric-synthesis.v13"
		unchangedInterpretation = "context-fabric-interpretation.v7"
	)

	project, candidate := reusableCandidate()
	// Simulates a row Save persisted while the deployment ran the PRE-fix
	// synthesis prompt (v12) -- i.e. before modelFacingFacts existed.
	gate := storedUnderPromptVersionsGate(unchangedInterpretation, preFixSynthesisVersion, candidate)

	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "status",
				TimeContext:      request.TimeContext,
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context: GraphContext{
				DriverCandidates: []DriverJudgment{}, EvidenceRefIDs: []string{}, FactRequirements: []FactRequirement{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Fine.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: &resultStoreStub{}, ReuseGate: gate,
	}, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID: func() string { return "result_fresh_chaos4355" },
		// The deployment's CURRENT synthesis version is the POST-fix one --
		// exactly what genkitruntime.DefaultSynthesisPromptVersion is after
		// this ticket's bump (open.go wires the real constant here in
		// production; this test uses the literal value deliberately, the
		// same convention TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse
		// already follows for prompt-version fixtures).
		ReusePromptVersions: ReusePromptVersions{
			InterpretationPromptVersion: unchangedInterpretation,
			SynthesisPromptVersion:      postFixSynthesisVersion,
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("Investigate() result.Reused = true, want false: a row stored under the PRE-CHAOS-4355-follow-up synthesis prompt version must not be served as reusable once the deployment runs the POST-fix version -- the model saw a different canonical_facts shape either way")
	}
}
