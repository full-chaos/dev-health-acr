package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// reuseGateFunc is a fake AnswerReuseGate driven by a plain function, so
// each test can express exactly the candidate (or miss) it needs without a
// stateful fake.
type reuseGateFunc func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error)

func (f reuseGateFunc) FindReusable(ctx context.Context, principal storage.Principal, key ReuseKey) (InvestigationResult, bool, ReuseMissReason, error) {
	result, ok, err := f(ctx, principal, key)
	// CHAOS-3898 v4.1 F5: every existing test closure here predates the
	// typed miss reason and returns only (result, ok, error) -- default a
	// miss to ReuseMissNoCandidate, the ordinary case, unless a specific
	// test constructs a fake that needs ReuseMissStaleGraphEpoch (those
	// tests use their own dedicated fake instead of this adapter).
	reason := ReuseMissReason("")
	if !ok {
		reason = ReuseMissNoCandidate
	}
	return result, ok, reason, err
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

// bindingOnlyGraphReader resolves a fixed ResolvedGraphBinding (CHAOS-3898
// §2.1) -- the ONE graph call every investigation makes now, reuse hit or
// not, since tryReuse's own §2.3 lookup needs the epoch -- but fails the
// test if ResolveSubjects/DiscoverContext are ever reached, preserving
// every existing reuse-hit test's "the graph is never actually READ" proof
// (AC-3782-1's guarantee was always specifically ZERO MODEL calls, never
// zero graph calls; binding resolution is the one graph-adjacent call a
// hit always makes).
type bindingOnlyGraphReader struct{ t *testing.T }

func (g bindingOnlyGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "binding-only-key", Epoch: 0}, nil
}

func (g bindingOnlyGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, error) {
	g.t.Fatal("ResolveSubjects should not be reached on a reuse hit")
	// CHAOS-4085: nil CommitBasisSet -- every commit this double returns reads
	// back as CommitBasisUnknown, the strict (must-be-affirmed) treatment.
	return SubjectResolution{}, StructureOfferMaterial{}, nil, nil
}

func (g bindingOnlyGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.t.Fatal("DiscoverContext should not be reached on a reuse hit")
	return GraphContext{}, nil
}

func mustReuseTestEngine(t *testing.T, deps EngineDependencies) *Engine {
	t.Helper()
	if deps.Graph == nil {
		deps.Graph = bindingOnlyGraphReader{t: t}
	}
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

// TestCHAOS4077_GraphNotProjectedCandidateAlwaysMissesReuseWithoutReadingTheGraph
// pins the fix for the reuse-staleness gap codex xhigh review round 1
// found (confirmed real): a stored never-projected-org no_match makes NO
// positive claims (zero subjects, zero evidence), so
// reuseAuthorizationStillHolds' own "nothing to recheck, trivially still
// valid" shortcut would otherwise treat it as an automatic hit forever --
// even after the org's graph gets projected for the first time and a
// fresh resolution could find real candidates. The fix must reject this
// shape BEFORE ever calling e.graph.ResolveSubjects, not merely return the
// right answer after reading it -- bindingOnlyGraphReader here fails the
// test outright if ResolveSubjects is reached at all, exactly the same
// discipline this file's own reuse-hit tests use to prove a hit never
// touches the graph.
func TestCHAOS4077_GraphNotProjectedCandidateAlwaysMissesReuseWithoutReadingTheGraph(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{Graph: bindingOnlyGraphReader{t: t}})

	candidate := validInvestigationResult()
	candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, GraphNotProjected: true}
	candidate.EvidenceRefIDs = []string{}

	holds, outcome := engine.reuseAuthorizationStillHolds(context.Background(), reusePrincipal(), validInvestigationRequest(), candidate, ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
	if holds {
		t.Fatal("reuseAuthorizationStillHolds() = true, want false: a graph-not-projected candidate must never be trivially reused")
	}
	if outcome != AnswerReuseMissGraphNotProjected {
		t.Fatalf("outcome = %q, want %q", outcome, AnswerReuseMissGraphNotProjected)
	}
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

// TestAC_3782_6_LostEdgeLevelEvidenceVisibilityFailsReuseRecheck is the
// Codex round-1 F4 regression: a stored result whose evidence is cited
// ONLY at the relationship-edge level (never in the path's own
// EvidenceRefIDs, nor anywhere else) must still be rechecked -- before
// the fix, reuseEvidenceRefsToRecheck walked Paths[].EvidenceRefIDs but
// never Paths[].Edges[].EvidenceRefIDs, so a candidate shaped exactly
// like this one skipped the containment check entirely and would have
// been (wrongly) served.
func TestAC_3782_6_LostEdgeLevelEvidenceVisibilityFailsReuseRecheck(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	other := SubjectRef{Kind: SubjectProject, CanonicalID: "project_other", Label: "Other"}
	candidate.EvidenceRefIDs = []string{} // nothing at the top level
	candidate.Paths = []RelationshipPath{{
		PathID:      "path_edgeonly_0001",
		Nodes:       []SubjectRef{project, other},
		WhyRelevant: "connected work",
		Edges: []RelationshipEdge{{
			Type: RelationshipType("RELATED_TO"), From: project, To: other,
			Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
			EvidenceRefIDs: []string{"acr:v1:evidence:edge_only_0001"}, // ONLY here
		}},
		EvidenceRefIDs: []string{}, // deliberately empty at path level
	}}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			// The fresh evidence set does NOT contain the edge-only ref.
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
		t.Fatal("result.Reused = true, want a fresh investigation once the candidate's edge-level evidence reference is no longer visible")
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

// reuseModelIdentityResolverFunc is a fake ReuseModelIdentityResolver
// driven by a plain function, mirroring reuseGateFunc above. Returns the
// CURRENT effective chain (CHAOS-3786): primary first, then fallback (if
// any) -- never a single static identity.
type reuseModelIdentityResolverFunc func(context.Context, string) ([]string, error)

func (f reuseModelIdentityResolverFunc) ResolveReuseModelIdentity(ctx context.Context, orgID string) ([]string, error) {
	return f(ctx, orgID)
}

// containsIdentity reports whether identities contains want -- the
// CHAOS-3786 chain-membership predicate ReuseKey.ModelIdentities exists
// for, mirrored here so fake ReuseGates can reproduce
// pginvestigation.Store.FindReusable's `model_identity = ANY(...)`
// semantics without a database.
func containsIdentity(identities []string, want string) bool {
	for _, identity := range identities {
		if identity == want {
			return true
		}
	}
	return false
}

// TestAC_3782_7_ReuseKeyUsesCurrentOrgEffectiveModelIdentityNotAStaticOne is
// the probe for Codex round-2 finding #3: two otherwise-identical
// Investigate calls, with the organization's EFFECTIVE model identity
// flipped between them (simulating a CHAOS-3775 BYO reconfiguration),
// must NOT both reuse the same stored candidate. Before the fix, Engine's
// lookup key came from a single value fixed at engine-construction time
// (EngineOptions.ReuseModelIdentities), so a per-organization config change
// between calls was invisible to it and both calls would have kept
// matching -- and reusing -- the same stale-model row.
func TestAC_3782_7_ReuseKeyUsesCurrentOrgEffectiveModelIdentityNotAStaticOne(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	freshResult := validInvestigationResult()

	currentIdentity := "org-a/model-v1"
	var capturedKeys []ReuseKey
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
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
		ReuseModelIdentityResolver: reuseModelIdentityResolverFunc(func(context.Context, string) ([]string, error) {
			return []string{currentIdentity}, nil
		}),
		// The candidate was saved under "org-a/model-v1" -- a real store's
		// FindReusable only returns it when the LOOKUP key's chain still
		// contains that identity (its own WHERE model_identity =
		// ANY($N)), which this fake reproduces directly.
		ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
			capturedKeys = append(capturedKeys, key)
			if containsIdentity(key.ModelIdentities, "org-a/model-v1") {
				return candidate, true, nil
			}
			return InvestigationResult{}, false, nil
		}),
	})

	first, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() (first) error = %v", err)
	}
	if !first.Reused {
		t.Fatal("first Investigate(): result.Reused = false, want true (org identity unchanged)")
	}

	// The organization reconfigures its model between the two calls.
	currentIdentity = "org-a/model-v2"

	second, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() (second) error = %v", err)
	}
	if second.Reused {
		t.Fatal("second Investigate(): result.Reused = true, want false -- a reconfigured organization must not reuse a stale-model answer")
	}
	if len(capturedKeys) != 2 {
		t.Fatalf("FindReusable called %d times, want 2", len(capturedKeys))
	}
	if !reflect.DeepEqual(capturedKeys[0].ModelIdentities, []string{"org-a/model-v1"}) || !reflect.DeepEqual(capturedKeys[1].ModelIdentities, []string{"org-a/model-v2"}) {
		t.Fatalf("captured ModelIdentities per call = [%v, %v], want [%v, %v]",
			capturedKeys[0].ModelIdentities, capturedKeys[1].ModelIdentities, []string{"org-a/model-v1"}, []string{"org-a/model-v2"})
	}
	want := []AnswerReuseOutcome{AnswerReuseHit, AnswerReuseMissNoCandidate}
	if got := telemetry.answerReuseOutcomes; !reuseOutcomeSlicesEqual(got, want) {
		t.Fatalf("answerReuseOutcomes = %v, want %v", got, want)
	}
}

// TestReuseModelIdentityResolverErrorFailsClosed proves a resolver error
// (e.g. an organization's BYO configuration exists but no longer
// decrypts) degrades to an ordinary fresh investigation -- it must never
// fall back to a different identity as a substitute, and must never call
// FindReusable with a guessed key.
func TestReuseModelIdentityResolverErrorFailsClosed(t *testing.T) {
	t.Parallel()

	freshResult := validInvestigationResult()
	telemetry := &recordingTelemetry{}
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
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseModelIdentityResolver: reuseModelIdentityResolverFunc(func(context.Context, string) ([]string, error) {
			return nil, errors.New("credential no longer decrypts")
		}),
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("FindReusable must not be called when the model identity could not be resolved")
			return InvestigationResult{}, false, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want false")
	}
	want := []AnswerReuseOutcome{AnswerReuseMissNoCandidate}
	if got := telemetry.answerReuseOutcomes; !reuseOutcomeSlicesEqual(got, want) {
		t.Fatalf("answerReuseOutcomes = %v, want %v", got, want)
	}
}

// TestChaos3786_ReuseHitsOnACandidateProducedByTheFallbackModel is the
// hit-rate probe for CHAOS-3786: a candidate whose stored identity is the
// FALLBACK model's (not the primary's) must still be reusable, as long as
// the fallback is still a member of the org's CURRENT effective chain. Pre-
// fix, ReuseKey carried a single ModelIdentity built from the primary alone
// -- a fallback-produced candidate's own identity never matched it, and
// this call would always fall through to a fresh (avoidable) investigation.
func TestChaos3786_ReuseHitsOnACandidateProducedByTheFallbackModel(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	candidate.Versions.ModelIdentity = "org-a/model-fallback"

	var capturedKeys []ReuseKey
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		ReuseModelIdentityResolver: reuseModelIdentityResolverFunc(func(context.Context, string) ([]string, error) {
			// The org's current chain: primary first, fallback second --
			// mirroring what contextFabricReuseModelIdentityResolver
			// resolves from ResolvedOrgModelConfig{Model, FallbackModel}.
			return []string{"org-a/model-primary", "org-a/model-fallback"}, nil
		}),
		ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
			capturedKeys = append(capturedKeys, key)
			// Reproduces pginvestigation.Store.FindReusable's
			// `model_identity = ANY($N)`: the candidate's single stored
			// identity need only be A MEMBER of the looked-up chain.
			if containsIdentity(key.ModelIdentities, candidate.Versions.ModelIdentity) {
				return candidate, true, nil
			}
			return InvestigationResult{}, false, nil
		}),
		Telemetry: telemetry,
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want true: a fallback-produced candidate must be reusable while the fallback is still in the org's current chain")
	}
	if len(capturedKeys) != 1 || !reflect.DeepEqual(capturedKeys[0].ModelIdentities, []string{"org-a/model-primary", "org-a/model-fallback"}) {
		t.Fatalf("captured ModelIdentities = %v, want exactly one call with [org-a/model-primary org-a/model-fallback]", capturedKeys)
	}
}

// TestChaos3786_StaleFallbackCandidateMissesOnceTheChainNoLongerNamesIt is
// the correctness probe for CHAOS-3786 defect (b): a candidate produced by
// an OLD fallback model must stop being reusable once the org's current
// chain no longer includes that identity (the fallback was reconfigured to
// a different model, or removed) -- even though the PRIMARY identity is
// completely unchanged. Before this fix, the reuse key was blind to the
// fallback dimension entirely, so a chain-only change like this would never
// have invalidated anything, silently serving a candidate whose answering
// model the org's chain can no longer produce.
func TestChaos3786_StaleFallbackCandidateMissesOnceTheChainNoLongerNamesIt(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	candidate.Versions.ModelIdentity = "org-a/model-fallback-old"
	freshResult := validInvestigationResult()

	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
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
		ReuseModelIdentityResolver: reuseModelIdentityResolverFunc(func(context.Context, string) ([]string, error) {
			// Primary unchanged; fallback reconfigured to a different
			// model. "org-a/model-fallback-old" is no longer a member.
			return []string{"org-a/model-primary", "org-a/model-fallback-new"}, nil
		}),
		ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
			if containsIdentity(key.ModelIdentities, candidate.Versions.ModelIdentity) {
				return candidate, true, nil
			}
			return InvestigationResult{}, false, nil
		}),
		Telemetry: telemetry,
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want false: the candidate's producing model is no longer in the org's current chain")
	}
	want := []AnswerReuseOutcome{AnswerReuseMissNoCandidate}
	if got := telemetry.answerReuseOutcomes; !reuseOutcomeSlicesEqual(got, want) {
		t.Fatalf("answerReuseOutcomes = %v, want %v", got, want)
	}
}

// TestPunctuationOnlyQuestionNeverAttemptsReuseLookup is the probe for
// Codex round-2 finding #4: CanonicalizeQuestion("?!?") ==
// CanonicalizeQuestion("!!") == "" -- every punctuation-only question would
// share ONE hash if tryReuse ever looked one up, even though "?!?" and "!!"
// are unrelated questions. This asserts the stronger, fail-closed property
// tryReuse actually implements: such a question never even reaches
// ReuseGate.FindReusable, proven by a gate fake that fails the test if
// called at all (not merely a gate that returns no candidate, which
// wouldn't distinguish "never looked up" from "looked up and missed").
func TestPunctuationOnlyQuestionNeverAttemptsReuseLookup(t *testing.T) {
	t.Parallel()

	freshResult := validInvestigationResult()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("FindReusable must not be called for a punctuation-only question (Codex round-2 finding #4)")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.Question = "?!?"
	if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	want := []AnswerReuseOutcome{AnswerReuseMissNoCandidate}
	if got := telemetry.answerReuseOutcomes; !reuseOutcomeSlicesEqual(got, want) {
		t.Fatalf("answerReuseOutcomes = %v, want %v", got, want)
	}
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

// TestAC_3782_2_FreshInvestigationAlwaysOverridesReusedToFalse is the
// Codex round-1 F8 regression: a Synthesizer that (incorrectly) returns
// Reused=true on its draft must not have that survive into a genuinely
// fresh result -- Reused=true is valid ONLY on the exact object tryReuse
// itself returns. Engine.Investigate must set it explicitly, not rely on
// the synthesizer never setting it.
func TestAC_3782_2_FreshInvestigationAlwaysOverridesReusedToFalse(t *testing.T) {
	t.Parallel()

	misbehavingResult := validInvestigationResult()
	misbehavingResult.ResultID = "result_should_be_overwritten"
	misbehavingResult.Reused = true // a buggy/malicious Synthesizer's draft

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return misbehavingResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: &resultStoreStub{},
		// No ReuseGate -- this is a plain fresh-investigation path,
		// isolating the assertion to Engine's own post-synthesis
		// override rather than anything reuse-gate-related.
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want Engine to have overridden the synthesizer's draft to false")
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want %q", result.ResultID, freshTestResultID)
	}
}

// orderTrackingSnapshotter is a fake SourceWatermarkSnapshotter that
// appends "snapshot" to a shared, ordered event log and returns a fixed
// snapshot -- used with orderTrackingGraphReader to prove CALL ORDER
// (Codex round-1 F1), not just that both were called.
type orderTrackingSnapshotter struct {
	events   *[]string
	snapshot SourceWatermarkSnapshot
}

func (s orderTrackingSnapshotter) SnapshotSourceWatermarks(context.Context, string) (SourceWatermarkSnapshot, error) {
	*s.events = append(*s.events, "snapshot")
	return s.snapshot, nil
}

// orderTrackingEpochSnapshotter is a fake RebuildEpochSnapshotter that
// appends "epoch_snapshot" to a shared, ordered event log and returns a
// fixed epoch -- the Codex round-2 finding #7 analog of
// orderTrackingSnapshotter, used to prove Engine captures the epoch at
// the same point it captures the watermark snapshot: BEFORE the graph is
// read.
type orderTrackingEpochSnapshotter struct {
	events *[]string
	epoch  int64
}

func (s orderTrackingEpochSnapshotter) SnapshotRebuildEpoch(context.Context, string) (int64, error) {
	*s.events = append(*s.events, "epoch_snapshot")
	return s.epoch, nil
}

// orderTrackingGraphReader appends "resolve_subjects" to the SAME shared
// event log on ResolveSubjects -- the first actual graph read in
// Investigate's flow.
type orderTrackingGraphReader struct {
	events     *[]string
	resolution SubjectResolution
}

func (g orderTrackingGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "order-tracking-key", Epoch: 0}, nil
}

func (g orderTrackingGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, error) {
	*g.events = append(*g.events, "resolve_subjects")
	// CHAOS-4085: nil CommitBasisSet -- every commit this double returns reads
	// back as CommitBasisUnknown, the strict (must-be-affirmed) treatment.
	return g.resolution, StructureOfferMaterial{}, nil, nil
}

func (g orderTrackingGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return GraphContext{Coverage: Coverage{Sources: []SourceObservation{}}}, nil
}

// snapshotCapturingResultStore is a fake InvestigationResultStore that
// records the exact SourceWatermarkSnapshot Save was called with -- the
// explicit fourth parameter, not anything threaded through context
// (team-lead vetoed context-smuggling this: load-bearing data belongs in
// the signature, where a forgetful caller fails to compile).
type snapshotCapturingResultStore struct {
	saveCalled    bool
	savedSnapshot SourceWatermarkSnapshot
	savedEpoch    RebuildEpoch
}

func (s *snapshotCapturingResultStore) Save(_ context.Context, _ storage.Principal, _ InvestigationResult, reuseSnapshot SourceWatermarkSnapshot, reuseEpoch RebuildEpoch, _ string, _ ReuseRetrievalIdentity, _ ReusePromptVersions, _ ReuseVersionAuthorities, _ int64) error {
	s.saveCalled = true
	s.savedSnapshot = reuseSnapshot
	s.savedEpoch = reuseEpoch
	return nil
}

func (s *snapshotCapturingResultStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return StoredInvestigationResult{}, nil
}

// TestF1_SnapshotCapturedBeforeGraphReadAndPassedExplicitlyToSave is the
// Codex round-1 F1 regression, proved directly rather than inferred: the
// watermark snapshot must be captured BEFORE the graph is read (not
// later, at Save, which could describe data fresher than what the graph
// read actually used), and the exact snapshot captured must be the one
// Save receives as its explicit SourceWatermarkSnapshot parameter.
func TestF1_SnapshotCapturedBeforeGraphReadAndPassedExplicitlyToSave(t *testing.T) {
	t.Parallel()

	var events []string
	snapshot := SourceWatermarkSnapshot{"linear": "wm-1", "github": "wm-7"}
	store := &snapshotCapturingResultStore{}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: orderTrackingGraphReader{events: &events, resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:          store,
		ReuseSnapshotter: orderTrackingSnapshotter{events: &events, snapshot: snapshot},
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if want := []string{"snapshot", "resolve_subjects"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("call order = %v, want %v (snapshot must be captured BEFORE the graph is read)", events, want)
	}
	if !store.saveCalled {
		t.Fatal("Save was never called")
	}
	if !reflect.DeepEqual(store.savedSnapshot, snapshot) {
		t.Errorf("snapshot passed to Save = %v, want %v", store.savedSnapshot, snapshot)
	}
}

// TestF1_NilSnapshotPassedToSaveWhenReuseSnapshotterIsNil proves the safe
// default: with no ReuseSnapshotter configured, Save is called with a nil
// SourceWatermarkSnapshot -- never a stale/empty one substituted in its
// place.
func TestF1_NilSnapshotPassedToSaveWhenReuseSnapshotterIsNil(t *testing.T) {
	t.Parallel()

	store := &snapshotCapturingResultStore{}
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
		Results: store,
		// ReuseSnapshotter left nil.
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !store.saveCalled {
		t.Fatal("Save was never called")
	}
	if store.savedSnapshot != nil {
		t.Errorf("snapshot passed to Save = %v, want nil", store.savedSnapshot)
	}
}

// TestCodex2F7_EpochCapturedBeforeGraphReadAndPassedExplicitlyToSave is
// the Codex round-2 finding #7 analog of
// TestF1_SnapshotCapturedBeforeGraphReadAndPassedExplicitlyToSave: the
// rebuild-invalidation epoch must be captured at the SAME point as the
// watermark snapshot -- BEFORE the graph is read, never later at Save --
// and the exact value captured must be the one Save receives as its
// explicit RebuildEpoch parameter.
func TestCodex2F7_EpochCapturedBeforeGraphReadAndPassedExplicitlyToSave(t *testing.T) {
	t.Parallel()

	var events []string
	snapshot := SourceWatermarkSnapshot{"linear": "wm-1"}
	store := &snapshotCapturingResultStore{}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: orderTrackingGraphReader{events: &events, resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:               store,
		ReuseSnapshotter:      orderTrackingSnapshotter{events: &events, snapshot: snapshot},
		ReuseEpochSnapshotter: orderTrackingEpochSnapshotter{events: &events, epoch: 7},
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// Both snapshot-time reads must happen before resolve_subjects; their
	// own relative order to EACH OTHER is not asserted (Engine's own
	// code performs them sequentially, but nothing about the contract
	// requires one before the other -- only that both precede the graph
	// read).
	if len(events) != 3 || events[2] != "resolve_subjects" {
		t.Fatalf("call order = %v, want [snapshot, epoch_snapshot (either order)] followed by resolve_subjects", events)
	}
	if !store.saveCalled {
		t.Fatal("Save was never called")
	}
	if store.savedEpoch == nil {
		t.Fatal("epoch passed to Save = nil, want the captured epoch")
	}
	if *store.savedEpoch != 7 {
		t.Errorf("epoch passed to Save = %d, want 7", *store.savedEpoch)
	}
}

// TestCodex2F7_NilEpochPassedToSaveWhenReuseEpochSnapshotterIsNil proves
// the safe default: with no ReuseEpochSnapshotter configured, Save is
// called with a nil RebuildEpoch -- never a guessed or zero-value epoch
// substituted in its place (zero is a legitimate epoch value for a
// never-invalidated organization, so it cannot double as a sentinel).
func TestCodex2F7_NilEpochPassedToSaveWhenReuseEpochSnapshotterIsNil(t *testing.T) {
	t.Parallel()

	store := &snapshotCapturingResultStore{}
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
		Results: store,
		// ReuseEpochSnapshotter left nil.
	})

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !store.saveCalled {
		t.Fatal("Save was never called")
	}
	if store.savedEpoch != nil {
		t.Errorf("epoch passed to Save = %v, want nil", store.savedEpoch)
	}
}

// keyRecordingResultStore records the time-axis key Save was given, so a
// test can assert lookup and save agree on it.
type keyRecordingResultStore struct {
	saved    InvestigationResult
	savedKey string
}

func (s *keyRecordingResultStore) Save(_ context.Context, _ storage.Principal, result InvestigationResult, _ SourceWatermarkSnapshot, _ RebuildEpoch, timeAxisKey string, _ ReuseRetrievalIdentity, _ ReusePromptVersions, _ ReuseVersionAuthorities, _ int64) error {
	s.saved = result
	s.savedKey = timeAxisKey
	return nil
}

func (s *keyRecordingResultStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return StoredInvestigationResult{}, nil
}

// clampReuseEngine builds an engine whose clock the test controls, with a
// reuse gate that serves only what Save actually stored -- so lookup/save
// key agreement is what decides a hit, which is the invariant under test.
func clampReuseEngine(t *testing.T, now func() time.Time, store *keyRecordingResultStore, lookupKeys *[]string) *Engine {
	t.Helper()
	gate := reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
		*lookupKeys = append(*lookupKeys, key.TimeAxisKey)
		if store.savedKey != "" && key.TimeAxisKey == store.savedKey {
			return store.saved, true, nil
		}
		return InvestigationResult{}, false, nil
	})
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "status",
				TimeContext:      request.TimeContext,
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
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
				Status: InvestigationComplete, DirectJudgment: "It was fine then.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "It was fine then.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, ReuseGate: gate,
	}, EngineOptions{ServiceVersion: "acr-test", Now: now, NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestF1_ClampedRequestDoesNotServeAnEarlierEffectiveAnswer is CHAOS-3781
// round-3 F1, red-green: the two-arrival scenario end to end.
//
// Round-1 F6 keyed reuse on the WIRE request, on the premise that
// identical wire requests should key identically regardless of arrival.
// That premise is false when clamping is time-dependent. A request for
// as_of 12:00:30 arriving at 12:00:00 is clamped to 12:00:00 and answered
// for that instant. The SAME wire request arriving at or after 12:00:30 is
// no longer future, so it is answered for 12:00:30 -- a different question
// with a legitimately different answer. Under wire keying the second
// request hit the first's row and was served the 12:00:00 answer.
//
// The arrival must be >= as_of. At, say, +1s the as_of is still in the
// future and still clamps, so the two keys would coincide and the test
// would pass without exercising the defect at all.
func TestF1_ClampedRequestDoesNotServeAnEarlierEffectiveAnswer(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 8, 13, 12, 0, 30, 0, time.UTC)
	firstArrival := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)   // as_of is future -> clamps to 12:00:00
	secondArrival := time.Date(2026, 8, 13, 12, 0, 45, 0, time.UTC) // as_of is past -> no clamp, means 12:00:30

	arrival := firstArrival
	store := &keyRecordingResultStore{}
	var lookupKeys []string
	engine := clampReuseEngine(t, func() time.Time { return arrival }, store, &lookupKeys)

	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("first Investigate() error = %v", err)
	}
	clampedFirst := TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &firstArrival})
	if store.savedKey != clampedFirst {
		t.Fatalf("Save keyed %q, want the CLAMPED effective key %q", store.savedKey, clampedFirst)
	}

	arrival = secondArrival
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("second Investigate() error = %v", err)
	}
	if result.Reused {
		t.Fatal("a request meaning 12:00:30 was served the answer that meant 12:00:00; the key must describe the effective time, not the wire time")
	}
	// And it looked up under its OWN effective time, which by now is the
	// unclamped as_of.
	wantSecond := TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &asOf})
	if lookupKeys[len(lookupKeys)-1] != wantSecond {
		t.Fatalf("second lookup keyed %q, want %q", lookupKeys[len(lookupKeys)-1], wantSecond)
	}
}

// TestF1_LookupAndSaveKeyIdentically preserves round-2 F2's invariant on
// the new semantics: whatever the key is derived from, both sides must
// derive it the same way, or a saved row is unreachable.
func TestF1_LookupAndSaveKeyIdentically(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	insideTolerance := now.Add(30 * time.Second)

	store := &keyRecordingResultStore{}
	var lookupKeys []string
	engine := clampReuseEngine(t, func() time.Time { return now }, store, &lookupKeys)

	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &insideTolerance}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if lookupKeys[0] != store.savedKey {
		t.Fatalf("lookup keyed %q but Save keyed %q; a saved row would be unreachable", lookupKeys[0], store.savedKey)
	}
	// Both on the clamped value, not the wire one.
	if want := TimeAxisKeyFor(TimeContext{Axis: TemporalValidTime, AsOf: &now}); store.savedKey != want {
		t.Fatalf("keyed %q, want the clamped %q", store.savedKey, want)
	}
}

// TestF1_UnclampedRequestsKeyIdenticallyAcrossArrivals is consequence (b):
// the overwhelming majority of requests never clamp, so their keys are
// unchanged and their reuse is unaffected. Without this, "key on the
// effective time" could quietly have cost reuse everywhere rather than
// only in the narrow future-dated class.
func TestF1_UnclampedRequestsKeyIdenticallyAcrossArrivals(t *testing.T) {
	t.Parallel()
	pastAsOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	firstArrival := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	laterArrival := time.Date(2026, 8, 13, 18, 30, 0, 0, time.UTC)

	arrival := firstArrival
	store := &keyRecordingResultStore{}
	var lookupKeys []string
	engine := clampReuseEngine(t, func() time.Time { return arrival }, store, &lookupKeys)

	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &pastAsOf}
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("first Investigate() error = %v", err)
	}

	arrival = laterArrival
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("second Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("an ordinary historical request stopped reusing across arrivals; clamping never fires for a past as_of, so its key must not move")
	}
}

// retrievalRecordingResultStore records the CHAOS-3833 retrieval identity,
// the CHAOS-3862 round-1 prompt versions, and the CHAOS-3862 round-2
// version authorities Save was given, so a test can assert lookup and save
// carry the same deployment-current pair for any of these dimensions.
type retrievalRecordingResultStore struct {
	saveCalled            bool
	savedRetrieval        ReuseRetrievalIdentity
	savedPromptVersion    ReusePromptVersions
	savedVersionAuthority ReuseVersionAuthorities
}

func (s *retrievalRecordingResultStore) Save(_ context.Context, _ storage.Principal, _ InvestigationResult, _ SourceWatermarkSnapshot, _ RebuildEpoch, _ string, retrieval ReuseRetrievalIdentity, promptVersions ReusePromptVersions, versionAuthorities ReuseVersionAuthorities, _ int64) error {
	s.saveCalled = true
	s.savedRetrieval = retrieval
	s.savedPromptVersion = promptVersions
	s.savedVersionAuthority = versionAuthorities
	return nil
}

func (s *retrievalRecordingResultStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return StoredInvestigationResult{}, nil
}

// TestCHAOS3833_RetrievalIdentityFlowsToBothLookupAndSave proves the
// one-options-field symmetry the P1-2 closure depends on: the SAME
// EngineOptions.ReuseRetrievalIdentity value appears in every lookup's
// ReuseKey (as the two conjunctive dimensions) AND as Save's explicit
// retrieval parameter -- so the persisted columns and the compared
// predicates cannot drift within one process. A fresh investigation is
// driven end to end so both sides actually run.
func TestCHAOS3833_RetrievalIdentityFlowsToBothLookupAndSave(t *testing.T) {
	t.Parallel()

	retrieval := ReuseRetrievalIdentity{
		EmbedRetrievalIdentity: "lmstudio/nomic-embed-text#t1:r2000:b0:pnone",
		RetrievalPolicyVersion: "rp1",
	}
	store := &retrievalRecordingResultStore{}
	var lookupKeys []ReuseKey
	gate := reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
		lookupKeys = append(lookupKeys, key)
		return InvestigationResult{}, false, nil
	})
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "status",
				TimeContext:      request.TimeContext,
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
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
		Results: store, ReuseGate: gate,
	}, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:            func() string { return "result_retrieval01" },
		ReuseRetrievalIdentity: retrieval,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(lookupKeys) != 1 {
		t.Fatalf("FindReusable called %d times, want 1", len(lookupKeys))
	}
	if lookupKeys[0].EmbedRetrievalIdentity != retrieval.EmbedRetrievalIdentity {
		t.Errorf("lookup key EmbedRetrievalIdentity = %q, want %q", lookupKeys[0].EmbedRetrievalIdentity, retrieval.EmbedRetrievalIdentity)
	}
	if lookupKeys[0].RetrievalPolicyVersion != retrieval.RetrievalPolicyVersion {
		t.Errorf("lookup key RetrievalPolicyVersion = %q, want %q", lookupKeys[0].RetrievalPolicyVersion, retrieval.RetrievalPolicyVersion)
	}
	if !store.saveCalled {
		t.Fatal("Save was never called")
	}
	if store.savedRetrieval != retrieval {
		t.Errorf("Save retrieval = %+v, want %+v", store.savedRetrieval, retrieval)
	}
}

// TestCHAOS3862_PromptVersionsFlowToBothLookupAndSave is the prompt-version
// twin of TestCHAOS3833_RetrievalIdentityFlowsToBothLookupAndSave: the SAME
// EngineOptions.ReusePromptVersions value must appear in every lookup's
// ReuseKey (as the two conjunctive dimensions) AND as Save's explicit
// promptVersions parameter, so the persisted columns and the compared
// predicates cannot drift within one process. A fresh investigation is
// driven end to end so both sides actually run.
func TestCHAOS3862_PromptVersionsFlowToBothLookupAndSave(t *testing.T) {
	t.Parallel()

	promptVersions := ReusePromptVersions{
		InterpretationPromptVersion: "context-fabric-interpretation.v7",
		SynthesisPromptVersion:      "context-fabric-synthesis.v9",
	}
	store := &retrievalRecordingResultStore{}
	var lookupKeys []ReuseKey
	gate := reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
		lookupKeys = append(lookupKeys, key)
		return InvestigationResult{}, false, nil
	})
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "status",
				TimeContext:      request.TimeContext,
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
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
		Results: store, ReuseGate: gate,
	}, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:         func() string { return "result_promptver01" },
		ReusePromptVersions: promptVersions,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(lookupKeys) != 1 {
		t.Fatalf("FindReusable called %d times, want 1", len(lookupKeys))
	}
	if lookupKeys[0].InterpretationPromptVersion != promptVersions.InterpretationPromptVersion {
		t.Errorf("lookup key InterpretationPromptVersion = %q, want %q", lookupKeys[0].InterpretationPromptVersion, promptVersions.InterpretationPromptVersion)
	}
	if lookupKeys[0].SynthesisPromptVersion != promptVersions.SynthesisPromptVersion {
		t.Errorf("lookup key SynthesisPromptVersion = %q, want %q", lookupKeys[0].SynthesisPromptVersion, promptVersions.SynthesisPromptVersion)
	}
	if !store.saveCalled {
		t.Fatal("Save was never called")
	}
	if store.savedPromptVersion != promptVersions {
		t.Errorf("Save promptVersions = %+v, want %+v", store.savedPromptVersion, promptVersions)
	}
}

// storedUnderPolicyVersionGate simulates ONE previously-stored answer, saved
// under savedPolicyVersion, and reports a hit only when a lookup's
// ReuseKey.RetrievalPolicyVersion matches it exactly -- the conjunctive
// equality predicate migration 0014 adds (store.go's
// `AND retrieval_policy_version = $m`). Used below to prove that a
// RetrievalPolicyVersion change, by itself, turns a would-have-been-a-hit
// into a miss.
func storedUnderPolicyVersionGate(savedPolicyVersion string, candidate InvestigationResult) reuseGateFunc {
	return func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
		if key.RetrievalPolicyVersion != savedPolicyVersion {
			return InvestigationResult{}, false, nil
		}
		return candidate, true, nil
	}
}

// TestCHAOS3834_RetrievalPolicyVersionChangeInvalidatesStoredAnswerReuse is
// CHAOS-3834's fail-pre proof that RetrievalPolicyVersion PARTICIPATES in
// the reuse key: with every other request property held identical, only
// changing the deployment's current RetrievalPolicyVersion turns a stored
// answer from reusable into a miss. This is exactly the mechanism a
// tau/K/HNSW default change (spec §4 R3) depends on -- a policy bump like
// CHAOS-3834's own rp1->rp2 must not let organizations keep serving
// answers derived under the OLD policy. Run this test against a build that
// dropped RetrievalPolicyVersion from ReuseKey (or stopped comparing it)
// and the "changed version" subtest fails: the stale candidate would still
// come back as a hit.
func TestCHAOS3834_RetrievalPolicyVersionChangeInvalidatesStoredAnswerReuse(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	// Simulates a row Save persisted while the deployment ran RetrievalPolicyVersion "rp1".
	gate := storedUnderPolicyVersionGate("rp1", candidate)

	buildEngine := func(t *testing.T, currentPolicyVersion string) *Engine {
		t.Helper()
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
			NewResultID: func() string { return "result_fresh_00001" },
			ReuseRetrievalIdentity: ReuseRetrievalIdentity{
				EmbedRetrievalIdentity: "openai/text-embedding-3-large#t2:r2000:b0:pnone",
				RetrievalPolicyVersion: currentPolicyVersion,
			},
		})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		return engine
	}

	t.Run("unchanged version still reuses the stored answer", func(t *testing.T) {
		t.Parallel()
		engine := buildEngine(t, "rp1")
		result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if !result.Reused {
			t.Fatal("Investigate() result.Reused = false, want true when the current RetrievalPolicyVersion matches the stored row's")
		}
	})

	t.Run("changed version misses -- the stale answer is not reused", func(t *testing.T) {
		t.Parallel()
		engine := buildEngine(t, "rp2")
		result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Reused {
			t.Fatal("Investigate() result.Reused = true, want false: a RetrievalPolicyVersion change must invalidate reuse, never silently serve a pre-policy answer")
		}
	})
}

// storedUnderPromptVersionsGate simulates ONE previously-stored answer,
// saved while the deployment ran (savedInterpretationVersion,
// savedSynthesisVersion), and reports a hit only when a lookup's
// ReuseKey carries BOTH values exactly -- the two conjunctive equality
// predicates migration 0015 adds (store.go's `AND
// interpretation_prompt_version = $10 AND synthesis_prompt_version =
// $11`). Used below to prove that EITHER prompt version changing, by
// itself, turns a would-have-been-a-hit into a miss.
func storedUnderPromptVersionsGate(savedInterpretationVersion, savedSynthesisVersion string, candidate InvestigationResult) reuseGateFunc {
	return func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
		if key.InterpretationPromptVersion != savedInterpretationVersion || key.SynthesisPromptVersion != savedSynthesisVersion {
			return InvestigationResult{}, false, nil
		}
		return candidate, true, nil
	}
}

// TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse is
// CHAOS-3862's fail-pre proof that InterpretationPromptVersion and
// SynthesisPromptVersion each PARTICIPATE in the reuse key: with every
// other request property held identical, changing EITHER the
// deployment's current interpretation prompt version or its current
// synthesis prompt version -- alone -- turns a stored answer from
// reusable into a miss. This is exactly the mechanism a prompt deploy
// (e.g. interpretation v6->v7) depends on: a stale-prompt answer must not
// keep serving from reuse for the rest of its staleness window. Run this
// test against a build that dropped either field from ReuseKey (or
// stopped comparing it) and the corresponding "changed version" subtest
// fails: the stale candidate would still come back as a hit.
func TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	// Simulates a row Save persisted while the deployment ran
	// interpretation v6 and synthesis v8.
	gate := storedUnderPromptVersionsGate("context-fabric-interpretation.v6", "context-fabric-synthesis.v8", candidate)

	buildEngine := func(t *testing.T, currentInterpretationVersion, currentSynthesisVersion string) *Engine {
		t.Helper()
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
			NewResultID: func() string { return "result_fresh_00002" },
			ReusePromptVersions: ReusePromptVersions{
				InterpretationPromptVersion: currentInterpretationVersion,
				SynthesisPromptVersion:      currentSynthesisVersion,
			},
		})
		if err != nil {
			t.Fatalf("NewEngine() error = %v", err)
		}
		return engine
	}

	t.Run("unchanged versions still reuse the stored answer", func(t *testing.T) {
		t.Parallel()
		engine := buildEngine(t, "context-fabric-interpretation.v6", "context-fabric-synthesis.v8")
		result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if !result.Reused {
			t.Fatal("Investigate() result.Reused = false, want true when the current prompt versions match the stored row's")
		}
	})

	t.Run("changed interpretation version misses -- the stale answer is not reused", func(t *testing.T) {
		t.Parallel()
		engine := buildEngine(t, "context-fabric-interpretation.v7", "context-fabric-synthesis.v8")
		result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Reused {
			t.Fatal("Investigate() result.Reused = true, want false: an interpretation prompt bump must invalidate reuse, never silently serve a stale-prompt answer")
		}
	})

	t.Run("changed synthesis version misses -- the stale answer is not reused", func(t *testing.T) {
		t.Parallel()
		engine := buildEngine(t, "context-fabric-interpretation.v6", "context-fabric-synthesis.v9")
		result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Reused {
			t.Fatal("Investigate() result.Reused = true, want false: a synthesis prompt bump must invalidate reuse, never silently serve a stale-prompt answer")
		}
	})
}
