package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3810. Every one of these tests fails on the pre-fix engine with
// "read canonical facts: canonical fact request requires discovered subjects
// or a cohort" -- the unclassified error the route turned into a 500
// internal_error, retryable=false, for what the contract has always had a
// status for.

// ambiguousResolution is the shape a real corpus produces: several ranked,
// receipt-bound, authorization-checked candidates and nothing committed.
func ambiguousResolution(prompt string) SubjectResolution {
	first := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev_a", Label: "Ask Dev (Platform)"}
	second := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev_b", Label: "Ask Dev (Growth)"}
	return SubjectResolution{
		Candidates: []SubjectCandidate{
			{ReceiptID: "receipt_ambiguous_a1", Subject: first, State: ResolutionAmbiguous, MatchReasons: []string{"Label matched two authorized projects."}, Confidence: 0.5, EvidenceRefIDs: []string{}},
			{ReceiptID: "receipt_ambiguous_b1", Subject: second, State: ResolutionAmbiguous, MatchReasons: []string{"Label matched two authorized projects."}, Confidence: 0.5, EvidenceRefIDs: []string{}},
		},
		Committed:           []SubjectRef{},
		ClarificationPrompt: prompt,
	}
}

func emptyGraphContext() GraphContext {
	return GraphContext{
		DriverCandidates: []DriverJudgment{}, EvidenceRefIDs: []string{}, FactRequirements: []FactRequirement{},
		Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
	}
}

// buildTerminalEngine wires an Engine whose fact reader AND synthesizer both
// fail the test if they are called at all. That is the point of the fix: an
// investigation with no committed subject must terminate in the engine,
// before the fact read, and without a model call -- so an ambiguous question
// stays answerable even when the model runtime is down.
func buildTerminalEngine(t *testing.T, graph GraphReader, results InvestigationResultStore) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- an investigation with no committed subject must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a terminal clarification/no_match result must be composed without a model call")
			return InvestigationResult{}, nil
		}),
		Results: results,
	}, EngineOptions{
		ServiceVersion: "terminal-test",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_terminal0001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func TestInvestigateConvertsAmbiguousResolutionToClarificationRequired(t *testing.T) {
	t.Parallel()
	const prompt = "Which subject did you mean: Ask Dev (Platform), Ask Dev (Growth)?"
	graph := &acceptanceGraphReader{resolution: ambiguousResolution(prompt), context: emptyGraphContext()}
	results := newMapResultStore()
	engine := buildTerminalEngine(t, graph, results)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a clarification_required result, not an error", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required", result.Status)
	}
	if result.SubjectResolution.ClarificationPrompt != prompt {
		t.Fatalf("ClarificationPrompt = %q, want the backend's own prompt %q", result.SubjectResolution.ClarificationPrompt, prompt)
	}
	if len(result.SubjectResolution.Candidates) != 2 {
		t.Fatalf("Candidates = %#v, want both ranked candidates attached so the caller can choose", result.SubjectResolution.Candidates)
	}
	if result.DeterministicAnswer == "" || !strings.Contains(result.DeterministicAnswer, prompt) {
		t.Fatalf("DeterministicAnswer = %q, want it to carry the clarification prompt", result.DeterministicAnswer)
	}
	if len(result.ClaimedFacts) != 0 || result.DirectJudgment != "" {
		t.Fatalf("result = %#v, want no claimed facts and no judgment: nothing was read", result)
	}
	// Persisted, and persisted with the candidate receipts intact -- the
	// clarification loop only closes if the caller can bind one of them back
	// through PriorSubjectReceipts on the follow-up turn.
	stored, err := results.Get(context.Background(), acceptancePrincipal(), result.ResultID)
	if err != nil {
		t.Fatalf("results.Get() error = %v, want the clarification result persisted for the follow-up turn", err)
	}
	if len(stored.SubjectResolution.Candidates) != 2 || stored.SubjectResolution.Candidates[0].ReceiptID != "receipt_ambiguous_a1" {
		t.Fatalf("stored candidates = %#v, want the receipt-bound candidates retrievable", stored.SubjectResolution.Candidates)
	}
}

func TestInvestigateAmbiguousResolutionWithoutClarificationAllowedIsNoMatch(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{resolution: ambiguousResolution(""), context: emptyGraphContext()}
	engine := buildTerminalEngine(t, graph, nil)
	request := validInvestigationRequest()
	request.Options.AllowClarification = false

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a no_match result, not an error", err)
	}
	// no_match, not clarification_required and not a refusal: the v1 contract
	// rejects a clarification result with no prompt, and a caller that set
	// AllowClarification=false has declined the only thing a prompt asks for.
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match when the caller disallowed clarification", result.Status)
	}
	if result.SubjectResolution.ClarificationPrompt != "" {
		t.Fatalf("ClarificationPrompt = %q, want none: the caller disallowed clarification", result.SubjectResolution.ClarificationPrompt)
	}
	if len(result.SubjectResolution.Candidates) != 2 {
		t.Fatalf("Candidates = %#v, want the ranked candidates still attached: they remain receipt-bindable on a follow-up", result.SubjectResolution.Candidates)
	}
}

func TestInvestigateEmptyResolutionIsNoMatch(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context:    emptyGraphContext(),
	}
	engine := buildTerminalEngine(t, graph, nil)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a no_match result, not an error", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match for a genuinely empty resolution", result.Status)
	}
	if result.DeterministicAnswer == "" {
		t.Fatal("DeterministicAnswer is empty for a no_match result")
	}
}

// A GraphReader that marks a resolution ambiguous but supplies no prompt must
// not silently downgrade to no_match -- that would tell the caller nothing
// matched when several things did.
func TestInvestigateSuppliesAClarificationPromptWhenTheBackendLeftItEmpty(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{resolution: ambiguousResolution(""), context: emptyGraphContext()}
	engine := buildTerminalEngine(t, graph, nil)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required even though the backend supplied no prompt", result.Status)
	}
	if strings.TrimSpace(result.SubjectResolution.ClarificationPrompt) == "" {
		t.Fatal("ClarificationPrompt is empty: the contract requires a prompt on a clarification_required result")
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() = %v, want a contract-valid clarification result", err)
	}
}

// A subjectless cohort discovery commits nothing yet has real subjects to
// read facts for. It must NOT be diverted into a terminal result -- the guard
// keys on the investigation subject list, not on Committed.
func TestInvestigateStillReadsFactsForASubjectlessCohort(t *testing.T) {
	t.Parallel()
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_cohort_1", Label: "Cohort Member"}
	graphContext := emptyGraphContext()
	graphContext.Cohort = &Cohort{
		Kind: SubjectProject, Rationale: "Every project in the organization.",
		Members: []CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"in scope"}, EvidenceRefIDs: []string{}}},
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context:    graphContext,
	}
	read := false
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			read = true
			if len(request.Subjects) != 1 || request.Subjects[0] != member {
				t.Fatalf("fact request subjects = %#v, want the cohort member", request.Subjects)
			}
			return CanonicalFactBundle{}, errors.New("stop here: the fact read is what this test is asserting")
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, nil
		}),
	}, EngineOptions{
		ServiceVersion: "terminal-test", Now: func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string { return "result_terminal0002" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err == nil {
		t.Fatal("Investigate() error = nil, want the fact reader's own error")
	}
	if !read {
		t.Fatal("a subjectless cohort discovery was diverted into a terminal result instead of reading facts for its members")
	}
}

// The invariant, from the other side: a fact request that reaches the
// registry with no subjects fails as a NAMED, classifiable condition.
func TestReadFactsWithoutSubjectsIsClassifiedNotBare(t *testing.T) {
	t.Parallel()
	registry, err := NewFactCapabilityRegistry(nil, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	_, err = registry.ReadFacts(context.Background(), acceptancePrincipal(), CanonicalFactRequest{
		Question:     bootstrapInterpretation(),
		Requirements: []FactRequirement{{Kind: FactStatus}},
	})
	if !errors.Is(err, ErrNoInvestigationSubjects) {
		t.Fatalf("ReadFacts() error = %v, want errors.Is(err, ErrNoInvestigationSubjects) so the route can classify it instead of falling through to a 500", err)
	}
}

// CHAOS-3810 codex round-1 P1: a terminal result composed with a real
// RuntimeAnswerSynthesizer must carry that synthesizer's static versions,
// not the placeholder. Only the receipt-derived fields -- which no model
// produced, because no model ran -- may read "unwired".
func TestTerminalResultCarriesTheSynthesizersStaticVersions(t *testing.T) {
	t.Parallel()
	synthesizer := RuntimeAnswerSynthesizer{Options: RuntimeAnswerSynthesizerOptions{
		ServiceVersion: "acr-test-1.2.3", Backend: "graph", BackendVersion: "falkor-1",
		ProjectionVersion: "projection-v9", QueryVersion: "query-v9", CanonicalServiceVersion: "ops-v9",
	}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}},
		Graph:       &acceptanceGraphReader{resolution: ambiguousResolution("Which one?"), context: emptyGraphContext()},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not run for a terminal result")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizer,
	}, EngineOptions{
		ServiceVersion: "engine-fallback",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_terminal0003" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	versions := result.Versions
	if versions.ServiceVersion != "acr-test-1.2.3" || versions.Backend != "graph" || versions.BackendVersion != "falkor-1" ||
		versions.ProjectionVersion != "projection-v9" || versions.QueryVersion != "query-v9" || versions.CanonicalServiceVersion != "ops-v9" {
		t.Fatalf("Versions = %#v, want the synthesizer's static versions verbatim", versions)
	}
	// Receipt-derived only: no model ran, so these are honestly unwired.
	if versions.InterpretationVersion != "unwired" || versions.SynthesisVersion != "unwired" || versions.ModelIdentity != "unwired" {
		t.Fatalf("Versions = %#v, want the receipt-derived fields to read \"unwired\"", versions)
	}
}

// The fallback stays: a synthesizer that does not implement
// ResultVersionProvider still produces a contract-valid terminal result, with
// the placeholder standing in for what nothing could report.
func TestTerminalResultFallsBackToUnwiredWithoutAVersionProvider(t *testing.T) {
	t.Parallel()
	engine := buildTerminalEngine(t, &acceptanceGraphReader{resolution: ambiguousResolution("Which one?"), context: emptyGraphContext()}, nil)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Versions.Backend != "unwired" || result.Versions.QueryVersion != "unwired" || result.Versions.CanonicalServiceVersion != "unwired" {
		t.Fatalf("Versions = %#v, want the placeholder when the synthesizer reports no static versions", result.Versions)
	}
	if result.Versions.ServiceVersion != "terminal-test" {
		t.Fatalf("ServiceVersion = %q, want Engine's own service version as the fallback", result.Versions.ServiceVersion)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() = %v", err)
	}
}
