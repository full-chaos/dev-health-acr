package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Codex round-1 F4, per the orchestrator's ruling: the ENGINE folds a
// request-scoped retrieval-degradation marker into the answer. The graph
// adapter reports it on the resolution; nothing invents a path from
// ResolveSubjects into the adapter's own Coverage construction.
func engineForDegradation(t *testing.T, degraded bool) (*Engine, InvestigationRequest) {
	t.Helper()
	return engineForDegradationWithLimitations(t, degraded, []string{})
}

func engineForDegradationWithLimitations(t *testing.T, degraded bool, limitations []string) (*Engine, InvestigationRequest) {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	resolution := SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project},
		RetrievalDegraded: degraded,
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: resolution,
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
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
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is not ready to ship.",
				CurrentState: "Release-readiness blockers remain.", StrongestPressures: []string{},
				Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: limitations,
				EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ask Dev is not ready to ship because release-readiness blockers remain.",
				Warnings:            []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID:    func() string { return "result_12345678" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	request := validInvestigationRequest()
	request.Question = "why is the auth work stuck?"
	return engine, request
}

func TestF4_EngineFoldsRetrievalDegradationIntoTheAnswer(t *testing.T) {
	engine, request := engineForDegradation(t, true)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if !result.Coverage.Partial {
		t.Fatal("a degraded retrieval must mark the answer's coverage partial")
	}
	if len(result.Limitations) != 1 {
		t.Fatalf("expected exactly one limitation, got %#v", result.Limitations)
	}
	// A freshly produced answer carries the CURRENT spelling, exactly.
	if result.Limitations[0] != retrievalDegradedLimitation {
		t.Fatalf("a fresh answer must carry the current limitation wording, got %q", result.Limitations[0])
	}
	// The phrasing must describe the ANSWER'S provenance, not the current
	// request -- that is what makes it read correctly when a reused answer
	// carries it forward verbatim.
	if !strings.Contains(retrievalDegradedLimitation, "when this answer was produced") {
		t.Fatalf("the limitation must be provenance-phrased, got %q", retrievalDegradedLimitation)
	}
}

// Leak-absence lives beside the constants it guards and is asserted against
// BOTH spellings, so the string and its guard cannot drift apart (the
// anchor/alias class). Every limitation form that can reach a reader --
// including the legacy one still present on stored answers -- must be free of
// retrieval internals.
func TestF4_NeitherLimitationSpellingLeaksRetrievalInternals(t *testing.T) {
	for _, limitation := range []string{retrievalDegradedLimitation, retrievalDegradedLimitationLegacy} {
		for _, leak := range []string{"vector", "embed", "model", "timeout", "error", "falkor", "nomic", "index", "lexical"} {
			if strings.Contains(strings.ToLower(limitation), leak) {
				t.Fatalf("limitation leaks a retrieval internal (%q): %q", leak, limitation)
			}
		}
	}
}

// RIDER 1: a stored answer written before the rewording keeps the LEGACY
// string verbatim, and nothing may treat it as malformed, unrecognized, or in
// need of correction. An InvestigationResult is immutable and answer reuse
// keys on its stored bytes.
func TestF4_LegacyLimitationSpellingIsStillRecognized(t *testing.T) {
	if !isRetrievalDegradedLimitation(retrievalDegradedLimitationLegacy) {
		t.Fatal("the legacy spelling must still be recognized as the degradation limitation")
	}
	if !isRetrievalDegradedLimitation(retrievalDegradedLimitation) {
		t.Fatal("the current spelling must be recognized")
	}
	if isRetrievalDegradedLimitation("Readiness evaluation was unavailable for this investigation.") {
		t.Fatal("an unrelated limitation must not be recognized as the degradation one")
	}
	if !hasRetrievalDegradedLimitation([]string{"something else", retrievalDegradedLimitationLegacy}) {
		t.Fatal("a stored answer carrying the legacy spelling must be recognized")
	}
	if hasRetrievalDegradedLimitation([]string{"something else"}) {
		t.Fatal("an answer with no degradation limitation must not be recognized as having one")
	}
	// The two spellings must be genuinely different, or this whole guard is
	// vacuous.
	if retrievalDegradedLimitation == retrievalDegradedLimitationLegacy {
		t.Fatal("the constants are identical; this test proves nothing")
	}
}

// RIDER 1, at the fold: a draft already carrying EITHER spelling must not gain
// a second, differently worded copy of the same statement.
func TestF4_FoldDoesNotDuplicateAnExistingDegradationLimitation(t *testing.T) {
	for _, existing := range []string{retrievalDegradedLimitation, retrievalDegradedLimitationLegacy} {
		engine, request := engineForDegradationWithLimitations(t, true, []string{existing})
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
		if err != nil {
			t.Fatalf("Investigate: %v", err)
		}
		count := 0
		for _, limitation := range result.Limitations {
			if isRetrievalDegradedLimitation(limitation) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected exactly one degradation limitation, got %d in %#v", count, result.Limitations)
		}
	}
}

func TestF4_EngineAddsNoLimitationWhenRetrievalIsHealthy(t *testing.T) {
	engine, request := engineForDegradation(t, false)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if result.Coverage.Partial {
		t.Fatal("a healthy retrieval must not mark the answer partial")
	}
	if len(result.Limitations) != 0 {
		t.Fatalf("a healthy retrieval must add no limitation, got %#v", result.Limitations)
	}
}
