package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The investigation scenarios of CHAOS-11910: one engine over test doubles,
// with each fault of the event stream's fault list injectable. This file uses
// nothing CHAOS-11910 added, on purpose: its golden test was run on the tree
// BEFORE the engine gained stage events, to record what Investigate served
// then, and the same test on this tree proves the sites changed nothing a
// caller without a sink receives.
//
// To regenerate (only for a deliberate change to what these scenarios serve):
//
//	ACR_WRITE_STAGE_SCENARIO_GOLDENS=1 go test ./internal/contextfabric -run TestStageScenarioAnswersAreTheGoldens

var errStageScenarioSave = errors.New("stage scenario: store refused the write")

type stageScenarioFaults struct {
	interpretErr error
	// unresolved commits no subject: the subjectless terminal, served
	// without a fact read or a synthesis.
	unresolved bool
	factErr    error
	// factWaitsForDeadline makes the fact read wait for the request deadline
	// and return its error, the way a slow canonical read ends.
	factWaitsForDeadline bool
	synthErr             error
	synthPanic           bool
	saveErr              error
}

type stageScenarioStore struct {
	err   error
	saved int
}

func (s *stageScenarioStore) Save(context.Context, storage.Principal, InvestigationResult, SourceWatermarkSnapshot, RebuildEpoch, string, ReuseRetrievalIdentity, ReusePromptVersions, ReuseVersionAuthorities, int64, string, SemanticStateWrite) error {
	if s.err != nil {
		return s.err
	}
	s.saved++
	return nil
}

func (s *stageScenarioStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return StoredInvestigationResult{}, nil
}

func stageScenarioEngine(t *testing.T, faults stageScenarioFaults) (*Engine, *stageScenarioStore) {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
	if faults.unresolved {
		resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	}
	store := &stageScenarioStore{err: faults.saveErr}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			if faults.interpretErr != nil {
				return InterpretedQuestion{}, faults.interpretErr
			}
			return InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers", TimeContext: TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactReadiness}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: resolution,
			context: GraphContext{
				Paths:            []RelationshipPath{},
				DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{{Kind: FactBlockers}, {Kind: FactReadiness}},
				EvidenceRefIDs:   []string{"evidence_project_status"},
				Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(ctx context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			if faults.factWaitsForDeadline {
				<-ctx.Done()
				return CanonicalFactBundle{}, ctx.Err()
			}
			if faults.factErr != nil {
				return CanonicalFactBundle{}, faults.factErr
			}
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			if faults.synthPanic {
				panic("stage scenario: synthesizer panics")
			}
			if faults.synthErr != nil {
				return InvestigationResult{}, faults.synthErr
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is not ready to ship.",
				CurrentState: "Release-readiness blockers remain.", StrongestPressures: []string{},
				Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{},
				EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ask Dev is not ready to ship because release-readiness blockers remain.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, store
}

func stageScenarioPrincipal() storage.Principal { return storage.Principal{OrgID: "org_1"} }

// stageScenarioAnswers are the scenarios that serve an answer, keyed by golden
// file stem.
var stageScenarioAnswers = map[string]stageScenarioFaults{
	"answered":   {},
	"unresolved": {unresolved: true},
}

func stageScenarioGoldenPath(stem string) string {
	return filepath.Join("testdata", "investigation_stage_scenarios", stem+".json")
}

// encodeStageScenarioAnswer encodes a served answer the way the route encodes
// its JSON body (marshalContextFabricResponse is json.Marshal).
func encodeStageScenarioAnswer(t *testing.T, result InvestigationResult) []byte {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

// TestStageScenarioAnswersAreTheGoldens holds what Investigate serves, with no
// sink installed, byte-equal to the goldens recorded before the stage sites
// existed.
func TestStageScenarioAnswersAreTheGoldens(t *testing.T) {
	for stem, faults := range stageScenarioAnswers {
		t.Run(stem, func(t *testing.T) {
			engine, _ := stageScenarioEngine(t, faults)
			result, err := engine.Investigate(context.Background(), stageScenarioPrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			encoded := encodeStageScenarioAnswer(t, result)
			path := stageScenarioGoldenPath(stem)
			if os.Getenv("ACR_WRITE_STAGE_SCENARIO_GOLDENS") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, encoded, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s (%d bytes)", path, len(encoded))
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, want) {
				t.Fatalf("%s: served answer differs from the golden recorded before the stage sites (%d bytes, golden %d)", stem, len(encoded), len(want))
			}
			if strings.TrimSpace(string(want)) == "" {
				t.Fatal("empty golden")
			}
		})
	}
}
