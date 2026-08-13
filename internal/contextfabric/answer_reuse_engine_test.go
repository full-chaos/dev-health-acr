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

func (g orderTrackingGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion) (SubjectResolution, error) {
	*g.events = append(*g.events, "resolve_subjects")
	return g.resolution, nil
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

func (s *snapshotCapturingResultStore) Save(_ context.Context, _ storage.Principal, _ InvestigationResult, reuseSnapshot SourceWatermarkSnapshot, reuseEpoch RebuildEpoch, _ string) error {
	s.saveCalled = true
	s.savedSnapshot = reuseSnapshot
	s.savedEpoch = reuseEpoch
	return nil
}

func (s *snapshotCapturingResultStore) Get(context.Context, storage.Principal, string) (InvestigationResult, error) {
	return InvestigationResult{}, nil
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
