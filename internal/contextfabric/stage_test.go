package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3811. Two properties, both invisible from outside the engine before
// this change: every failure carries the STAGE it failed at, and no wrap on
// the investigation path flattens a sentinel out of the chain.

func stageTestEngine(t *testing.T, graph GraphReader, facts CanonicalFactReader, synthesizer AnswerSynthesizer, results InvestigationResultStore) *Engine {
	t.Helper()
	if facts == nil {
		facts = factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		})
	}
	if synthesizer == nil {
		synthesizer = synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, nil
		})
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}},
		Graph:       graph, Facts: facts, Synthesizer: synthesizer, Results: results,
	}, EngineOptions{
		ServiceVersion: "stage-test",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_stage000001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func committedGraphReader() *acceptanceGraphReader {
	project := acceptanceProject()
	return &acceptanceGraphReader{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{ReceiptID: "receipt_stage000001", Subject: project, State: ResolutionCommitted, MatchReasons: []string{"Exact label match."}, Confidence: 1, EvidenceRefIDs: []string{}}},
			Committed:  []SubjectRef{project},
		},
		context: bootstrapGraphContext(project),
	}
}

// A relationship type outside the closed v1 vocabulary makes
// InvestigationResult.Validate fail with contractsv1's OWN named sentinel.
// The engine used to wrap that with %v, flattening it into prose: the route
// saw an error matching nothing at all and answered 500 internal_error with
// no classification. This is the ticket's proven-red case.
func TestInvestigateValidationFailurePreservesTheContractSentinel(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	other := SubjectRef{Kind: SubjectProject, CanonicalID: "project_other", Label: "Other"}
	synthesizer := synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
		return InvestigationResult{
			Status: InvestigationComplete, DirectJudgment: "Ask Dev is progressing.", CurrentState: "In progress.",
			StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
			Paths: []RelationshipPath{{
				PathID: "path_stage00000001", Nodes: []SubjectRef{project, other},
				Edges: []RelationshipEdge{{
					Type: "NOT_A_REAL_TYPE", From: project, To: other,
					Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicInferred,
					EvidenceRefIDs: []string{"evidence_12345678"},
				}},
				WhyRelevant: "The graph associated these subjects.", EvidenceRefIDs: []string{"evidence_12345678"},
			}},
			Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{"evidence_12345678"},
			ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			Versions: VersionSet{
				ServiceVersion: "stage-test", ContractVersion: InvestigationResultSchemaV1, Backend: "graph",
				ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
				SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1",
			},
			DeterministicAnswer: "Ask Dev is progressing.", Warnings: []string{},
		}, nil
	})
	engine := stageTestEngine(t, committedGraphReader(), nil, synthesizer, nil)

	_, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err == nil {
		t.Fatal("Investigate() error = nil, want the result validation to fail")
	}
	if !errors.Is(err, contractsv1.ErrContextFabricUnknownRelationshipType) {
		t.Fatalf("Investigate() error = %v, want errors.Is(err, ErrContextFabricUnknownRelationshipType) through the full engine chain", err)
	}
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("Investigate() error = %v, want it to still wrap ErrInvalidResult", err)
	}
	if stage, ok := FailureStage(err); !ok || stage != StageValidation {
		t.Fatalf("FailureStage() = %q, %v, want %q, true", stage, ok, StageValidation)
	}
}

func TestInvestigateTagsTheFailingStage(t *testing.T) {
	t.Parallel()
	boom := errors.New("dependency exploded")
	cases := []struct {
		name   string
		engine func(*testing.T) *Engine
		stage  InvestigationStage
	}{
		{
			name:  "resolution",
			stage: StageResolution,
			engine: func(t *testing.T) *Engine {
				return stageTestEngine(t, &acceptanceGraphReader{err: boom}, nil, nil, nil)
			},
		},
		{
			name:  "fact_read",
			stage: StageFactRead,
			engine: func(t *testing.T) *Engine {
				return stageTestEngine(t, committedGraphReader(), factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, boom
				}), nil, nil)
			},
		},
		{
			name:  "synthesis",
			stage: StageSynthesis,
			engine: func(t *testing.T) *Engine {
				return stageTestEngine(t, committedGraphReader(), nil, synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return InvestigationResult{}, boom
				}), nil)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := testCase.engine(t).Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
			if !errors.Is(err, boom) {
				t.Fatalf("Investigate() error = %v, want it to wrap the dependency's own error", err)
			}
			stage, ok := FailureStage(err)
			if !ok || stage != testCase.stage {
				t.Fatalf("FailureStage() = %q, %v, want %q, true", stage, ok, testCase.stage)
			}
		})
	}
}

// The engine's own invariant breach, if a future edit ever reaches the fact
// read with no subjects, must be classified AND staged.
func TestInvestigateNoSubjectsAssertionIsStagedAndClassified(t *testing.T) {
	t.Parallel()
	// The registry rejection is the same condition reached from the other
	// side (a fact reader that receives a subjectless request).
	engine := stageTestEngine(t, committedGraphReader(), factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return CanonicalFactBundle{}, ErrNoInvestigationSubjects
	}), nil, nil)

	_, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if !errors.Is(err, ErrNoInvestigationSubjects) {
		t.Fatalf("Investigate() error = %v, want errors.Is(err, ErrNoInvestigationSubjects)", err)
	}
	if stage, ok := FailureStage(err); !ok || stage != StageFactRead {
		t.Fatalf("FailureStage() = %q, %v, want %q, true", stage, ok, StageFactRead)
	}
}

func TestFailureStageReportsUnknownForAnUnstagedError(t *testing.T) {
	t.Parallel()
	stage, ok := FailureStage(errors.New("no stage here"))
	if ok || stage != StageUnknown {
		t.Fatalf("FailureStage() = %q, %v, want %q, false", stage, ok, StageUnknown)
	}
	if !ValidInvestigationStage(stage) {
		t.Fatalf("FailureStage() returned %q, which is outside the closed enum a log may emit", stage)
	}
}
