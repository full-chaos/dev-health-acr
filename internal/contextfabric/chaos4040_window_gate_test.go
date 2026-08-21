package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4040 (sol-max ruling 2026-08-21, "GATE ALL INFERRED WINDOWS out of
// decisive terminals"): every inferred (unconfirmed) evidence window --
// both origins -- must be gated to a confirmation-required terminal before
// it can influence a decisive answer. These tests pin the two gate call
// sites (engine.go) and the reuse-side rejection (answer_reuse.go)
// directly, at the contextfabric-package level, independent of the
// two-turn harness's own broader acceptance run.

// countingInterpreter counts Interpret calls and returns a fixed
// interpretation -- used to prove explicit-unconfirmed gating (gate 1)
// happens BEFORE Interpret ever runs (calls stays 0), and that
// class-default gating (gate 2) happens AFTER it (calls reaches exactly 1,
// never more).
type countingInterpreter struct {
	interpretation InterpretedQuestion
	calls          int
}

func (c *countingInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
	c.calls++
	return c.interpretation, nil
}

func buildWindowGateEngine(t *testing.T, interpreter QuestionInterpreter, graph *acceptanceGraphReader, results InvestigationResultStore) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- a gated request must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a gated request must never reach synthesis")
			return InvestigationResult{}, nil
		}),
		Results: results,
	}, EngineOptions{
		ServiceVersion: "window-gate-test",
		Now:            func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_windowgate01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestWindowGate_ExplicitUnconfirmed_InterceptsBeforeInterpret pins gate 1
// (engine.go, immediately after canonicalizeEvidenceWindow): an MCP bare
// explicit evidence_window field -- present on the wire, never confirmed --
// must be gated to a confirmation-required terminal BEFORE tryReuse or
// Interpret ever run. CHAOS-4040's own run-3 acceptance bar: "early-gate
// cases show zero interpreter/graph/fact/synthesis calls".
func TestWindowGate_ExplicitUnconfirmed_InterceptsBeforeInterpret(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	results := newMapResultStore()
	engine := buildWindowGateEngine(t, interpreter, graph, results)

	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result, not an error", err)
	}
	if interpreter.calls != 0 {
		t.Fatalf("Interpret called %d times, want 0: an explicit-unconfirmed window must gate before Interpret ever runs", interpreter.calls)
	}
	if graph.resolveCalls != 0 || graph.discoverCalls != 0 {
		t.Fatalf("graph calls (resolve=%d, discover=%d), want 0/0: gated before any graph read", graph.resolveCalls, graph.discoverCalls)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required", result.Status)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %#v, want none: a gated result commits nothing", result.SubjectResolution.Committed)
	}
	if result.Reused {
		t.Fatal("Reused = true, want false: a gated result is never itself a reuse hit")
	}
	if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
		t.Fatal("WindowClarification is nil or empty, want real receipt-bound options")
	}
	if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowInferredDefault {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want a disclosed inferred_default window", result.EffectiveEvidenceWindow)
	}
	if result.DeterministicAnswer == "" {
		t.Fatal("DeterministicAnswer is empty, want the confirmation-required disclosure")
	}
	if result.DirectJudgment != "" || len(result.ClaimedFacts) != 0 {
		t.Fatalf("result = %#v, want no judgment and no claimed facts: nothing was read", result)
	}
	stored, err := results.Get(context.Background(), acceptancePrincipal(), result.ResultID)
	if err != nil {
		t.Fatalf("results.Get() error = %v, want the gated result persisted for winr_ redemption", err)
	}
	if stored.Result.WindowClarification == nil || len(stored.Result.WindowClarification.Options) == 0 {
		t.Fatal("persisted result carries no WindowClarification options -- a follow-up could never redeem a winr_ receipt")
	}
}

// TestWindowGate_ClassDefault_InterceptsAfterInterpretBeforeResolution pins
// gate 2 (engine.go, immediately after the axis-conflict check): a request
// with NO window info at all -- the ordinary "no time stated" case, most
// common in real traffic -- must still be gated once composeEffectiveWindow
// would otherwise pick a class-table default, but only AFTER Interpret has
// run (the gate needs the interpreted Shape) and BEFORE subject resolution.
// CHAOS-4040's own run-3 acceptance bar: "class-default cases
// interpretation-only".
func TestWindowGate_ClassDefault_InterceptsAfterInterpretBeforeResolution(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	results := newMapResultStore()
	engine := buildWindowGateEngine(t, interpreter, graph, results)

	request := validInvestigationRequest() // no EvidenceWindow at all

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result, not an error", err)
	}
	if interpreter.calls != 1 {
		t.Fatalf("Interpret called %d times, want exactly 1: class-default gating needs the interpreted Shape and must not call it twice", interpreter.calls)
	}
	if graph.resolveCalls != 0 || graph.discoverCalls != 0 {
		t.Fatalf("graph calls (resolve=%d, discover=%d), want 0/0: gated before subject resolution", graph.resolveCalls, graph.discoverCalls)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required", result.Status)
	}
	if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowInferredDefault {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want a disclosed inferred_default window", result.EffectiveEvidenceWindow)
	}
	if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
		t.Fatal("WindowClarification is nil or empty, want real receipt-bound options")
	}
}

// TestWindowGate_AllowClarificationFalse_RefusesWithoutOffering pins the
// "nonliteral/unattested refusal, never D0 absence" branch: a caller that
// declined clarification gets the closed vocabulary's no_match (the only
// available non-decisive terminal), but with DISTINCT prose from a genuine
// empty-pool no_match, and no actionable WindowClarification.
func TestWindowGate_AllowClarificationFalse_RefusesWithoutOffering(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, interpreter, graph, newMapResultStore())

	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
	request.Options.AllowClarification = false

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a no_match result, not an error", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match", result.Status)
	}
	if result.WindowClarification != nil {
		t.Fatalf("WindowClarification = %#v, want nil: the caller declined the only thing a prompt could ask for", result.WindowClarification)
	}
	if result.DeterministicAnswer == "" || result.DeterministicAnswer == windowConfirmationRequiredLimitation {
		t.Fatalf("DeterministicAnswer = %q, want the DISTINCT refused-not-absence prose, never the ordinary confirmation-required text", result.DeterministicAnswer)
	}
}

// TestWindowGate_ConfirmedWindowReachesResolution proves the gate is
// SPECIFIC to inferred windows: a question_stated window (this fixture's
// own "workbench" surface) reaches resolution and a real decisive answer,
// unaffected -- neither gate intercepts a confirmed window.
func TestWindowGate_ConfirmedWindowReachesResolution(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a decisive result: a confirmed window must never be gated", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("Status = %q, want complete", result.Status)
	}
	if graph.resolveCalls != 1 {
		t.Fatalf("graph.resolveCalls = %d, want exactly 1: a confirmed window must reach subject resolution", graph.resolveCalls)
	}
	if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowQuestionStated {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want Provenance=question_stated", result.EffectiveEvidenceWindow)
	}
}

// TestWindowGate_ReuseRejectsOldDecisiveInferredWindowCandidate pins
// answer_reuse.go's own CHAOS-4040 addition: a STORED decisive candidate
// carrying an inferred (unconfirmed) window predates this ticket's gate --
// no fresh Save can produce that combination any more -- and must never be
// served, even to a request whose own windowKey is empty (the ordinary
// no-window-stated case, the SAME shape most likely to have matched such a
// row on every OTHER reuse-key dimension, and the one case
// answer_reuse.go's pre-existing windowKey!="" guard never even inspects).
// A rejected candidate falls through to a fresh investigation, which this
// package's own gate 2 (window.go) then correctly intercepts.
func TestWindowGate_ReuseRejectsOldDecisiveInferredWindowCandidate(t *testing.T) {
	t.Parallel()
	_, candidate := reusableCandidate()
	candidate.Status = InvestigationComplete
	candidate.EffectiveEvidenceWindow = &EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowInferredDefault}

	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Interpreter: interpreter,
		Graph:       bindingOnlyGraphReader{t: t}, // fails the test if ResolveSubjects/DiscoverContext are reached
		Results:     &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a fresh gated result, not an error", err)
	}
	if result.Reused {
		t.Fatal("result.Reused = true, want false: a decisive candidate carrying an inferred window must never be served")
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required: the rejected reuse falls through to gate 2", result.Status)
	}
	if interpreter.calls != 1 {
		t.Fatalf("Interpret called %d times, want exactly 1: the reuse miss must fall through to a genuinely fresh (gated) investigation", interpreter.calls)
	}
}

// TestWindowGate_ClassDefault_WithConfirmedStructure_EchoesAndPersists pins
// codex xhigh review round 1's own P1 finding: gate 2 can be reached by a
// request that ALSO confirmed kind/anchor/handle structure via receipt
// (structureCanon.Confirmed non-empty) -- the gate must still echo that
// confirmation on the gated result (ConfirmedStructure), not silently drop
// it just because window is why the request ultimately stalled, mirroring
// terminalResult's own identical discipline for the ordinary subjectless
// terminal.
func TestWindowGate_ClassDefault_WithConfirmedStructure_EchoesAndPersists(t *testing.T) {
	t.Parallel()
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_windowgate_0001"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, interpreter, graph, store)

	request := validInvestigationRequest() // no EvidenceWindow -- class-default gate applies
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result, not an error", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required", result.Status)
	}
	if graph.resolveCalls != 0 {
		t.Fatalf("graph.resolveCalls = %d, want 0: gated before subject resolution even with confirmed structure in play", graph.resolveCalls)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1: the kind confirmation must survive the window gate's own terminal, not be silently dropped", len(result.ConfirmedStructure))
	}
	if result.ConfirmedStructure[0].AppliedValue != string(SubjectPullRequest) {
		t.Fatalf("ConfirmedStructure[0] = %#v, want the confirmed kind pull_request", result.ConfirmedStructure[0])
	}
}
