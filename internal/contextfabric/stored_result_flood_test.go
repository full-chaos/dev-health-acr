package contextfabric

import (
	"bytes"
	"context"
	"os"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func floodTestServiceLimitations(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, contractsv1.ContextFabricWorkItemAuthorizationGapLimitation(false, 10+i, 50+i, false))
	}
	return out
}

// The flood rule has two clauses, a list at the cap and no model-authored
// entry in it; flipping either one must change the verdict.
func TestStoredResultFloodWithheldClauses(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricLimitationsMaxCount
	for _, tc := range []struct {
		name        string
		limitations []string
		want        bool
	}{
		{"cap, all service disclosures", floodTestServiceLimitations(max), true},
		{"one short of the cap", floodTestServiceLimitations(max - 1), false},
		{"cap, one model-authored entry", append(floodTestServiceLimitations(max-1), "The provider returned a stale status."), false},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := validInvestigationResult()
			result.Limitations = tc.limitations
			if got := StoredResultFloodWithheld(result); got != tc.want {
				t.Fatalf("StoredResultFloodWithheld() = %v, want %v for %d limitations", got, tc.want, len(tc.limitations))
			}
		})
	}
}

// A reuse candidate of any kind with a flooded limitation list is not served:
// the question is answered afresh, and the lookup is recorded as a miss.
func TestAnswerReuseWithholdsAFloodedCandidate(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricLimitationsMaxCount
	for _, tc := range []struct {
		name        string
		limitations []string
		wantReused  bool
		wantOutcome AnswerReuseOutcome
	}{
		{"flooded", floodTestServiceLimitations(max), false, AnswerReuseMissNoCandidate},
		{"one short of the cap", floodTestServiceLimitations(max - 1), true, AnswerReuseHit},
		{"cap with a model-authored entry", append(floodTestServiceLimitations(max-1), "The provider returned a stale status."), true, AnswerReuseHit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, candidate := reusableCandidate()
			candidate.Limitations = tc.limitations
			fresh := validInvestigationResult()
			telemetry := &recordingTelemetry{}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return fresh, nil
				}),
				Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
				}),
				Results:   &resultStoreStub{},
				Telemetry: telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			})
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Reused != tc.wantReused {
				t.Fatalf("Reused = %v, want %v", result.Reused, tc.wantReused)
			}
			if got := telemetry.answerReuseOutcomes; !reuseOutcomeSlicesEqual(got, []AnswerReuseOutcome{tc.wantOutcome}) {
				t.Fatalf("answerReuseOutcomes = %v, want [%s]", got, tc.wantOutcome)
			}
		})
	}
}

// Every stored-result surface is either one that serves a stored row to a
// caller, which must call the flood helper, or one that does not and says why.
// A new surface fails here until it is classified.
func TestStoredResultSurfacesServingToACallerCallTheFloodHelper(t *testing.T) {
	t.Parallel()
	servesToCaller := map[StoredResultSurface]string{
		StoredResultSurfaceResultByID:        "../api/context_fabric_result_routes.go",
		StoredResultSurfaceAnswerReuse:       "answer_reuse.go",
		StoredResultSurfaceEvidenceExpansion: "evidence_expansion.go",
	}
	notServed := map[StoredResultSurface]string{
		StoredResultSurfacePriorResult:     "out of scope: the engine reads a prior result to carry state into a new turn; the row is never returned to the caller",
		StoredResultSurfaceFactRootRecheck: "out of scope: re-checks the committed root subjects of a turn before the fact read; the row is never returned to the caller",
		StoredResultSurfaceThread:          "out of scope: the thread read names a turn's result id, or withholds it, and never serves result content",
	}
	for _, surface := range StoredResultSurfaceVocabulary() {
		file, serves := servesToCaller[surface]
		if _, skipped := notServed[surface]; !serves && !skipped {
			t.Fatalf("stored-result surface %q is classified neither as serving to a caller nor as out of scope", surface)
		}
		if !serves {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("surface %q: read %s: %v", surface, file, err)
		}
		if !bytes.Contains(source, []byte("StoredResultFloodWithheld(")) {
			t.Fatalf("surface %q serves a stored result to a caller but %s does not call StoredResultFloodWithheld", surface, file)
		}
	}
}
