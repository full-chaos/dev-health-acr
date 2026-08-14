package contextfabric

import (
	"context"
	"errors"
	"slices"
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

// CHAOS-3810 codex round-1 P2. A no_match result reached WITH candidates
// attached (the caller disallowed clarification) must not claim that nothing
// matched -- the candidates it names are in the same payload. The status
// ruling is unchanged; only the prose is keyed on candidates-present.
func TestNoMatchProseDoesNotClaimAbsenceWhenCandidatesArePresent(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{resolution: ambiguousResolution(""), context: emptyGraphContext()}
	engine := buildTerminalEngine(t, graph, nil)
	request := validInvestigationRequest()
	request.Options.AllowClarification = false

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match (the status ruling is unchanged)", result.Status)
	}
	if len(result.SubjectResolution.Candidates) == 0 {
		t.Fatal("the honesty valve is missing: candidates must stay attached")
	}
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitation {
			t.Fatalf("Limitations = %#v, want the ambiguous wording, not the absence wording, while %d candidates are attached", result.Limitations, len(result.SubjectResolution.Candidates))
		}
	}
	if !slices.Contains(result.Limitations, ambiguousNoClarificationLimitation) {
		t.Fatalf("Limitations = %#v, want the ambiguous-and-clarification-unavailable wording", result.Limitations)
	}
	if strings.Contains(result.DeterministicAnswer, "No investigation subject could be resolved") {
		t.Fatalf("DeterministicAnswer = %q, want it not to claim absence while candidates are attached", result.DeterministicAnswer)
	}
	if !strings.Contains(result.DeterministicAnswer, "more than one authorized subject matched") {
		t.Fatalf("DeterministicAnswer = %q, want it to state what actually happened", result.DeterministicAnswer)
	}
}

// The other direction: genuine absence must keep the absence wording. A fix
// that made every no_match say "several matched" would be the same defect
// mirrored.
func TestNoMatchProseKeepsAbsenceWordingWhenNothingMatched(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context:    emptyGraphContext(),
	}
	engine := buildTerminalEngine(t, graph, nil)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !slices.Contains(result.Limitations, noMatchLimitation) {
		t.Fatalf("Limitations = %#v, want the absence wording when nothing matched", result.Limitations)
	}
	if !strings.Contains(result.DeterministicAnswer, "No investigation subject could be resolved") {
		t.Fatalf("DeterministicAnswer = %q, want the absence sentence", result.DeterministicAnswer)
	}
}

// The synthesized (model) path shares statusSentence, so it inherits every
// rule below. no_match is FOUR states, not one (codex rounds 1-2), and the
// sentence must name the one that actually happened without guessing a cause.
func TestStatusSentenceNoMatchDescribesTheActualResolutionState(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_x", Label: "X"}
	candidate := func(id string) SubjectCandidate {
		return SubjectCandidate{ReceiptID: "receipt_" + id, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, State: ResolutionAmbiguous, Confidence: 0.5}
	}
	cases := []struct {
		name       string
		resolution SubjectResolution
		want       string
		reject     []string
	}{
		{
			name:       "nothing matched",
			resolution: SubjectResolution{},
			want:       "No investigation subject could be resolved",
		},
		{
			name:       "one uncommitted candidate",
			resolution: SubjectResolution{Candidates: []SubjectCandidate{candidate("a")}},
			want:       "One authorized subject matched",
			reject:     []string{"more than one", "No investigation subject could be resolved"},
		},
		{
			name:       "several uncommitted candidates",
			resolution: SubjectResolution{Candidates: []SubjectCandidate{candidate("a"), candidate("b")}},
			want:       "more than one authorized subject matched",
			reject:     []string{"No investigation subject could be resolved"},
		},
		{
			// Contract-legal and shipped: the acceptance corpus's no-data
			// case commits a subject, reads facts that return no rows, and
			// takes no_match. Absence prose there is false twice over.
			name:       "committed subject with no canonical data",
			resolution: SubjectResolution{Committed: []SubjectRef{subject}, Candidates: []SubjectCandidate{}},
			want:       "no canonical data was found",
			reject:     []string{"No investigation subject could be resolved", "more than one", "One authorized subject matched"},
		},
		{
			// Multi-commit is ROUTINE, not exotic: FinalizeExactResolution
			// commits every resolved caller hint, so two hints commit two
			// subjects and the candidate list holds those same subjects.
			// The sentence must be count-neutral -- "the subject" would be
			// the round-2 F1 plurality defect on the branch that fixed F2.
			name: "several committed subjects with no canonical data",
			resolution: SubjectResolution{
				Committed:  []SubjectRef{subject, {Kind: SubjectProject, CanonicalID: "project_y", Label: "Y"}},
				Candidates: []SubjectCandidate{candidate("x"), candidate("y")},
			},
			want:   "no canonical data was found",
			reject: []string{"The subject of this question", "No investigation subject could be resolved", "more than one authorized subject matched", "One authorized subject matched"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := statusSentence(InvestigationNoMatch, testCase.resolution)
			if !strings.Contains(got, testCase.want) {
				t.Fatalf("statusSentence(no_match, %s) = %q, want it to contain %q", testCase.name, got, testCase.want)
			}
			for _, rejected := range testCase.reject {
				if strings.Contains(got, rejected) {
					t.Fatalf("statusSentence(no_match, %s) = %q, must not contain %q", testCase.name, got, rejected)
				}
			}
			// Cause-free on the shared path in every branch: only the
			// engine's terminal path knows a cause.
			if strings.Contains(got, "clarification") {
				t.Fatalf("statusSentence(no_match, %s) = %q, want no asserted cause on the shared path", testCase.name, got)
			}
		})
	}
}

// CHAOS-3810 codex round-2 F1. Exactly ONE uncommitted candidate is a
// reachable state -- a lone candidate that misses the 0.72 gate is left
// uncommitted by ResolveFromMergedCandidates -- and prose claiming "more than
// one" beside a single listed candidate contradicts the payload it travels
// with, the same defect class as claiming absence beside candidates.
func singleCandidateResolution(prompt string) SubjectResolution {
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_lonely", Label: "Ask Dev (Platform)"}
	return SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_single00001", Subject: subject, State: ResolutionAmbiguous,
			MatchReasons: []string{"Matched the subject term below the commit threshold."}, Confidence: 0.6, EvidenceRefIDs: []string{},
		}},
		Committed:           []SubjectRef{},
		ClarificationPrompt: prompt,
	}
}

func TestSingleUncommittedCandidateProseIsNotPlural(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name               string
		allowClarification bool
		wantStatus         InvestigationStatus
		wantLimitation     string
	}{
		{name: "clarification allowed", allowClarification: true, wantStatus: InvestigationClarificationRequired, wantLimitation: clarificationRequiredLimitationOne},
		{name: "clarification disallowed", allowClarification: false, wantStatus: InvestigationNoMatch, wantLimitation: ambiguousNoClarificationLimitationOne},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			graph := &acceptanceGraphReader{resolution: singleCandidateResolution(""), context: emptyGraphContext()}
			engine := buildTerminalEngine(t, graph, nil)
			request := validInvestigationRequest()
			request.Options.AllowClarification = testCase.allowClarification

			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != testCase.wantStatus {
				t.Fatalf("Status = %q, want %q", result.Status, testCase.wantStatus)
			}
			if len(result.SubjectResolution.Candidates) != 1 {
				t.Fatalf("Candidates = %#v, want exactly one", result.SubjectResolution.Candidates)
			}
			if !slices.Contains(result.Limitations, testCase.wantLimitation) {
				t.Fatalf("Limitations = %#v, want the single-candidate wording", result.Limitations)
			}
			for _, limitation := range result.Limitations {
				if strings.Contains(limitation, "more than one") {
					t.Fatalf("Limitations = %#v, claim plurality with exactly one candidate attached", result.Limitations)
				}
			}
			if strings.Contains(result.DeterministicAnswer, "more than one") {
				t.Fatalf("DeterministicAnswer = %q, claims plurality with exactly one candidate attached", result.DeterministicAnswer)
			}
			// The prompt the caller is asked to act on must agree too.
			if strings.Contains(result.SubjectResolution.ClarificationPrompt, "Several") {
				t.Fatalf("ClarificationPrompt = %q, claims plurality with exactly one candidate", result.SubjectResolution.ClarificationPrompt)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("result.Validate() = %v", err)
			}
		})
	}
}
