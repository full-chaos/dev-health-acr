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
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{},
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
	limitation := result.Limitations[0]
	// The limitation must be FIXED prose: no mechanism name, no provider, no
	// model id, no error text. It is answer-facing, and the operator-facing
	// detail belongs in telemetry.
	for _, leak := range []string{"vector", "embed", "model", "timeout", "error", "falkor", "nomic"} {
		if strings.Contains(strings.ToLower(limitation), leak) {
			t.Fatalf("the limitation leaks a retrieval internal (%q): %q", leak, limitation)
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
