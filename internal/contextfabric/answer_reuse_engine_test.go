package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// reuseGateFunc is a fake AnswerReuseGate driven by a plain function, so
// each test can express exactly the candidate (or miss) it needs without a
// stateful fake.
type reuseGateFunc func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error)

func (f reuseGateFunc) FindReusable(ctx context.Context, principal storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
	return f(ctx, principal, key)
}

// failingModelRuntime fails the test immediately if either method is
// called. Used to prove a code path makes zero model calls (AC-3782-1):
// wiring this into RuntimeQuestionInterpreter/RuntimeAnswerSynthesizer
// means Engine calling either would fail the test, not just leave a
// counter at zero -- so it also can never pass by accident if Engine
// happens to skip incrementing something.
type failingModelRuntime struct{ t *testing.T }

func (f failingModelRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	f.t.Helper()
	f.t.Fatal("InterpretQuestion must not be called on an answer-reuse hit (AC-3782-1)")
	return InterpretedQuestion{}, ModelExecutionReceipt{}, nil
}

func (f failingModelRuntime) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	f.t.Helper()
	f.t.Fatal("SynthesizeAnswer must not be called on an answer-reuse hit (AC-3782-1)")
	return SynthesisDraft{}, ModelExecutionReceipt{}, nil
}

// countingModelReceiptSink is the counting fake CHAOS-3782's design note
// promised: AC-3782-1 binds against ModelReceiptSink directly (lane-3775's
// durable sink is a separate, concurrently-built implementation of the
// same interface; this test does not depend on it).
type countingModelReceiptSink struct {
	calls int
}

func (s *countingModelReceiptSink) RecordModelExecution(context.Context, storage.Principal, ModelExecutionReceipt) error {
	s.calls++
	return nil
}

// failingFactReader fails the test if ReadFacts is ever called -- a reuse
// hit must skip canonical fact reads entirely, not just model calls.
type failingFactReader struct{ t *testing.T }

func (f failingFactReader) ReadFacts(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
	f.t.Helper()
	f.t.Fatal("ReadFacts must not be called on an answer-reuse hit")
	return CanonicalFactBundle{}, nil
}

func reusePrincipal() storage.Principal { return storage.Principal{OrgID: "org_reuse"} }

// freshTestResultID is what mustReuseTestEngine's NewResultID always
// returns. Engine.Investigate overwrites whatever ResultID a Synthesizer
// fake sets on its returned result (result.ResultID = e.newResultID()),
// so a fresh-path assertion in this file must compare against THIS
// constant, never against a value a test fixture happened to set.
const freshTestResultID = "result_fresh_00001"

// reusableCandidate returns a stored InvestigationResult that will pass
// the condition-6 authorization recheck against a graphReaderStub whose
// resolution.Committed is exactly []SubjectRef{project} -- EvidenceRefIDs
// is left empty so the evidence leg is trivially satisfied, keeping each
// test focused on the one thing it means to exercise.
func reusableCandidate() (SubjectRef, InvestigationResult) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	candidate := validInvestigationResult()
	candidate.ResultID = "result_reused_0001"
	candidate.RequestID = "request_original_01"
	candidate.GeneratedAt = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
	candidate.EvidenceRefIDs = []string{}
	return project, candidate
}

func mustReuseTestEngine(t *testing.T, deps EngineDependencies) *Engine {
	t.Helper()
	if deps.Interpreter == nil {
		deps.Interpreter = interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached")
			return InterpretedQuestion{}, nil
		})
	}
	if deps.Facts == nil {
		deps.Facts = failingFactReader{t: t}
	}
	if deps.Synthesizer == nil {
		deps.Synthesizer = synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached")
			return InvestigationResult{}, nil
		})
	}
	engine, err := NewEngine(deps, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID: func() string { return "result_fresh_00001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestAC_3782_1_ReuseHitMakesZeroModelCalls binds AC-3782-1 literally: the
// same question, same organization, watermark unchanged, inside the
// staleness window, produces zero model calls -- asserted by a counting
// ModelReceiptSink wired through the real RuntimeQuestionInterpreter/
// RuntimeAnswerSynthesizer adapters (not a bypassed fake), so this proves
// Engine never even calls into the model runtime, not merely that it
// forgot to record a receipt.
func TestAC_3782_1_ReuseHitMakesZeroModelCalls(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	sink := &countingModelReceiptSink{}
	runtime := failingModelRuntime{t: t}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime, Sink: sink},
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Sink: sink},
		Graph:       graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results:     &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("Investigate() result.Reused = false, want true on a reuse hit")
	}
	if sink.calls != 0 {
		t.Fatalf("model-execution receipt count = %d, want 0 (AC-3782-1)", sink.calls)
	}
}

// TestAC_3782_2_ReuseMarksResponseWithReusedResultIdentifierAndGenerationTime
// binds AC-3782-2: the response marks reuse explicitly (Reused=true), and
// ResultID/GeneratedAt name the REUSED result's own identifier and
// generation time -- not this call's fresh request/clock, which
// mustReuseTestEngine deliberately configures to produce different values
// (result_fresh_00001 / Unix(200,0)) so a test bug that let the fresh path
// leak through cannot pass by coincidence.
func TestAC_3782_2_ReuseMarksResponseWithReusedResultIdentifierAndGenerationTime(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want true")
	}
	if result.ResultID != candidate.ResultID {
		t.Errorf("result.ResultID = %q, want the reused result's own ID %q", result.ResultID, candidate.ResultID)
	}
	if !result.GeneratedAt.Equal(candidate.GeneratedAt) {
		t.Errorf("result.GeneratedAt = %v, want the reused result's own generation time %v", result.GeneratedAt, candidate.GeneratedAt)
	}
}

// TestAC_3782_6_LostSubjectAuthorizationFailsReuseRecheck binds AC-3782-6's
// subject leg: a caller who has lost access to a subject in the stored
// result does not receive that result -- ResolveSubjects no longer
// committing the candidate's subject must force a fresh investigation,
// not serve the stale, now-unauthorized candidate.
func TestAC_3782_6_LostSubjectAuthorizationFailsReuseRecheck(t *testing.T) {
	t.Parallel()

	_, candidate := reusableCandidate()
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		// The subject no longer resolves -- authorization narrowed since
		// the candidate was generated.
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want a fresh investigation once the candidate's subject no longer authorizes")
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want the fresh result %q", result.ResultID, freshTestResultID)
	}
}

// TestAC_3782_6_LostEvidenceVisibilityFailsReuseRecheck binds AC-3782-6's
// evidence-ref leg: a stored result whose evidence reference is no longer
// present in a freshly discovered evidence set must not be served either,
// even though its subject still resolves.
func TestAC_3782_6_LostEvidenceVisibilityFailsReuseRecheck(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	candidate.EvidenceRefIDs = []string{"acr:v1:evidence:0000000001"}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			// The fresh evidence set does NOT contain the candidate's
			// evidence ref -- visibility narrowed.
			context: GraphContext{EvidenceRefIDs: []string{}},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want a fresh investigation once the candidate's evidence reference is no longer visible")
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want the fresh result %q", result.ResultID, freshTestResultID)
	}
}

// TestAC_3782_8_RecordsReuseOutcomeForHitAndMiss binds AC-3782-8: the
// reuse rate and the saved model-call count are both derived from
// EngineTelemetry.RecordAnswerReuse's outcome stream (see that method's
// doc comment) -- so this asserts the stream itself is correct for a hit
// and for the AnswerReuseMissNoCandidate miss reason specifically (the
// gate itself reported no candidate). The authorization/containment miss
// reasons are asserted directly in TestAC_3782_6_LostSubjectAuthorizationFailsReuseRecheck
// and TestAC_3782_6_LostEvidenceVisibilityFailsReuseRecheck below.
func TestAC_3782_8_RecordsReuseOutcomeForHitAndMiss(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	freshResult := validInvestigationResult()

	telemetry := &recordingTelemetry{}
	hit := mustReuseTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})
	if _, err := hit.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() (hit) error = %v", err)
	}

	miss := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil
		}),
	})
	if _, err := miss.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() (miss) error = %v", err)
	}

	want := []AnswerReuseOutcome{AnswerReuseHit, AnswerReuseMissNoCandidate}
	if got := telemetry.answerReuseOutcomes; !reuseOutcomeSlicesEqual(got, want) {
		t.Fatalf("answerReuseOutcomes = %v, want %v", got, want)
	}
}

// TestAC_3782_8_DistinguishesAuthorizationFromContainmentMissReasons binds
// the review correction to AC-3782-8: a subject-authorization miss and an
// evidence-containment miss must record DIFFERENT outcome labels, not
// collapse into one generic "miss", so a cratered reuse rate is
// diagnosable from telemetry alone.
func TestAC_3782_8_DistinguishesAuthorizationFromContainmentMissReasons(t *testing.T) {
	t.Parallel()

	project, authCandidate := reusableCandidate()
	_, evidenceCandidate := reusableCandidate()
	evidenceCandidate.EvidenceRefIDs = []string{"acr:v1:evidence:0000000002"}
	freshResult := validInvestigationResult()

	freshDeps := func(telemetry *recordingTelemetry) EngineDependencies {
		return EngineDependencies{
			Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return CanonicalFactBundle{}, nil
			}),
			Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				return freshResult, nil
			}),
			Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
			}),
			Results:   &resultStoreStub{},
			Telemetry: telemetry,
		}
	}

	authTelemetry := &recordingTelemetry{}
	authDeps := freshDeps(authTelemetry)
	authDeps.Graph = graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}
	authDeps.ReuseGate = reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
		return authCandidate, true, nil
	})
	authEngine := mustReuseTestEngine(t, authDeps)
	if _, err := authEngine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() (authorization miss) error = %v", err)
	}
	if want := []AnswerReuseOutcome{AnswerReuseMissAuthorization}; !reuseOutcomeSlicesEqual(authTelemetry.answerReuseOutcomes, want) {
		t.Fatalf("authorization-miss outcomes = %v, want %v", authTelemetry.answerReuseOutcomes, want)
	}

	evidenceTelemetry := &recordingTelemetry{}
	evidenceDeps := freshDeps(evidenceTelemetry)
	evidenceDeps.Graph = graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    GraphContext{EvidenceRefIDs: []string{}},
	}
	evidenceDeps.ReuseGate = reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
		return evidenceCandidate, true, nil
	})
	evidenceEngine := mustReuseTestEngine(t, evidenceDeps)
	if _, err := evidenceEngine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() (containment miss) error = %v", err)
	}
	if want := []AnswerReuseOutcome{AnswerReuseMissEvidenceContainment}; !reuseOutcomeSlicesEqual(evidenceTelemetry.answerReuseOutcomes, want) {
		t.Fatalf("containment-miss outcomes = %v, want %v", evidenceTelemetry.answerReuseOutcomes, want)
	}
}

func reuseOutcomeSlicesEqual(a, b []AnswerReuseOutcome) bool {
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

// TestEngineFallsThroughToFreshInvestigationWhenReuseGateErrors proves a
// ReuseGate error degrades to an ordinary fresh investigation rather than
// failing the whole request -- TRD §19.7.3 fails closed, but "closed"
// means "run fresh", never "error the caller".
func TestEngineFallsThroughToFreshInvestigationWhenReuseGateErrors(t *testing.T) {
	t.Parallel()

	freshResult := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, errors.New("boom")
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want the gate error to degrade to a fresh investigation, not fail the request", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want false")
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want %q", result.ResultID, freshTestResultID)
	}
}
