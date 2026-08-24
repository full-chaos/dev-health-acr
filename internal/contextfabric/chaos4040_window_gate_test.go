package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	// CHAOS-4118: gate 1 shares windowConfirmationRequiredResult with gate 2
	// (chaos4040_window_gate_test.go's own class-default test asserts this
	// in detail) -- the window-only StructureNeeds fix applies uniformly to
	// both call sites.
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil, want a window-only disclosure mirroring WindowClarification (CHAOS-4118)")
	}
	if len(result.StructureNeeds.Missing) != 1 || result.StructureNeeds.Missing[0] != contractsv1.ContextFabricStructureNeedWindow {
		t.Fatalf("StructureNeeds.Missing = %#v, want exactly [window]", result.StructureNeeds.Missing)
	}
	if !reflect.DeepEqual(result.StructureNeeds.WindowOptions, result.WindowClarification.Options) {
		t.Fatalf("StructureNeeds.WindowOptions = %#v, want the identical option set as WindowClarification.Options = %#v", result.StructureNeeds.WindowOptions, result.WindowClarification.Options)
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
//
// This scenario is also CHAOS-3742's own D0 control shape (a case whose
// subject the graph would never resolve -- the fake graph's own
// resolution field is never even consulted here, resolveCalls stays 0,
// proving the gate does not care what resolution would have found) --
// team-lead's own pre-PR design check (2026-08-21): since gate 2 fires
// BEFORE ResolveSubjects, a candidate-based kind/anchor/handle disclosure
// (composeStructureNeeds's own call site, unresolved.go, takes
// StructureOfferMaterial -- ResolveSubjects' own second return value) is
// structurally IMPOSSIBLE to populate here -- CHAOS-4119 tracks that
// separately, out of this ticket's scope.
//
// CHAOS-4118: what IS possible here is the window member's own disclosure
// -- composeWindowClarification (called above, unconditionally) already
// derives it in full from `effective`, with no dependency on resolution.
// Before this ticket, that offer reached ONLY the legacy WindowClarification
// field; windowConfirmationRequiredResult now ALSO composes a window-only
// StructureNeeds (Missing=[window], WindowOptions mirroring
// WindowClarification.Options) so a caller reading the modern CHAOS-3900 W2
// unified surface sees the SAME offer every other non-decisive terminal
// discloses through it, instead of nothing. The two-turn harness's own
// disclosure-presence check (chaos3742_two_turn_confirmation_test.go) is
// `turn1.StructureNeeds != nil || turn1.WindowClarification != nil` --
// already OR'd and satisfied either way, so this asserts the STRONGER,
// now-true claim: both fields are non-nil, and StructureNeeds.WindowOptions
// carries the identical option set WindowClarification.Options does.
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
	// CHAOS-4234: the gate now runs ONE offers-only resolve (its commit
	// output discarded) and still stops before DiscoverContext -- the
	// gate fires REGARDLESS of what that resolve found, including a
	// D0-control-shaped case whose subject would never resolve.
	if graph.resolveCalls != 1 || graph.discoverCalls != 0 {
		t.Fatalf("graph calls (resolve=%d, discover=%d), want 1/0: exactly one offers-only resolve under the gate, nothing past it", graph.resolveCalls, graph.discoverCalls)
	}
	if !OffersOnlyResolution(graph.resolveCtxs[0]) {
		t.Fatal("the gated ResolveSubjects call did not carry the offers-only mark -- the gate must never run a decisive resolve")
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required", result.Status)
	}
	if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowInferredDefault {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want a disclosed inferred_default window", result.EffectiveEvidenceWindow)
	}
	if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
		t.Fatal("WindowClarification is nil or empty, want real receipt-bound options -- alone sufficient to satisfy the harness's OR'd disclosure-presence check")
	}
	// CHAOS-4118: the window member's own StructureNeeds disclosure IS
	// possible here (unlike kind/anchor/handle, which stay structurally
	// undisclosable at this gate -- see this test's own doc comment) --
	// windowConfirmationRequiredResult composes it from the SAME `effective`
	// window composeWindowClarification already used, so it must carry
	// exactly one Missing entry (window) and the identical option set.
	if result.StructureNeeds == nil {
		t.Fatal("StructureNeeds is nil, want a window-only disclosure: CHAOS-4118 composes it from the effective window, which needs no resolution")
	}
	if len(result.StructureNeeds.Missing) != 1 || result.StructureNeeds.Missing[0] != contractsv1.ContextFabricStructureNeedWindow {
		t.Fatalf("StructureNeeds.Missing = %#v, want exactly [window]", result.StructureNeeds.Missing)
	}
	if len(result.StructureNeeds.KindOptions) != 0 || len(result.StructureNeeds.AnchorOptions) != 0 || len(result.StructureNeeds.HandleOptions) != 0 {
		t.Fatalf("StructureNeeds kind/anchor/handle options = %#v/%#v/%#v, want all empty: this fake's offers-only resolve returned empty material, so the gate reduces to CHAOS-4118's window-only disclosure", result.StructureNeeds.KindOptions, result.StructureNeeds.AnchorOptions, result.StructureNeeds.HandleOptions)
	}
	if !reflect.DeepEqual(result.StructureNeeds.WindowOptions, result.WindowClarification.Options) {
		t.Fatalf("StructureNeeds.WindowOptions = %#v, want the identical option set as WindowClarification.Options = %#v", result.StructureNeeds.WindowOptions, result.WindowClarification.Options)
	}
}

// TestWindowGate_ClassDefault_NudgeModeAppendsWarning pins codex round 2's
// own confirmed finding: windowConfirmationRequiredResult must apply the
// SAME ContextFabricWindowConfirmationNudge handling the decisive/terminal
// paths already apply (engine.go, terminalResult) -- a caller that asked
// to be nudged must see the fixed nudge sentence in Warnings on the
// confirmation-required terminal too, not only on an answer that no
// longer exists once CHAOS-4040 gates inferred windows out of every
// decisive terminal.
func TestWindowGate_ClassDefault_NudgeModeAppendsWarning(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, interpreter, graph, newMapResultStore())

	request := validInvestigationRequest() // no EvidenceWindow -- class-default gate applies
	request.Options.WindowConfirmationMode = contractsv1.ContextFabricWindowConfirmationNudge

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result, not an error", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required", result.Status)
	}
	found := false
	for _, w := range result.Warnings {
		if w == windowConfirmationNudgeSentence {
			found = true
		}
	}
	if !found {
		t.Fatalf("Warnings = %#v, want the nudge sentence: nudge mode must still be honored on the confirmation-required terminal", result.Warnings)
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
	// CHAOS-4118 negative control: a caller that declined clarification gets
	// NO offer through either surface -- StructureNeeds must stay nil in
	// lockstep with WindowClarification, never disclose a window option the
	// caller explicitly said it does not want asked about.
	if result.StructureNeeds != nil {
		t.Fatalf("StructureNeeds = %#v, want nil: the caller declined the only thing a prompt could ask for, and this surface carries the SAME offer WindowClarification would have", result.StructureNeeds)
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
	// CHAOS-4234: gate 2 now runs one offers-only resolve, so the graph
	// double must tolerate exactly that call and nothing past it.
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
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
	if graph.resolveCalls != 1 || !OffersOnlyResolution(graph.resolveCtxs[0]) || graph.discoverCalls != 0 {
		t.Fatalf("graph calls (resolve=%d, discover=%d), want exactly one offers-only resolve and no discovery: the gate holds after the rejected reuse", graph.resolveCalls, graph.discoverCalls)
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
	// CHAOS-4234: the confirmed kind reaches the gate's offers-only resolve
	// (so the offers it composes are already kind-scoped), while the gate
	// itself still holds.
	if graph.resolveCalls != 1 || !OffersOnlyResolution(graph.resolveCtxs[0]) {
		t.Fatalf("graph.resolveCalls = %d, want exactly 1 offers-only resolve under the gate", graph.resolveCalls)
	}
	if graph.lastConfirmedKind == nil || graph.lastConfirmedKind.Kind != SubjectPullRequest {
		t.Fatalf("confirmed kind reaching the offers-only resolve = %#v, want pull_request", graph.lastConfirmedKind)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1: the kind confirmation must survive the window gate's own terminal, not be silently dropped", len(result.ConfirmedStructure))
	}
	if result.ConfirmedStructure[0].AppliedValue != string(SubjectPullRequest) {
		t.Fatalf("ConfirmedStructure[0] = %#v, want the confirmed kind pull_request", result.ConfirmedStructure[0])
	}
	// Codex round-2 (confirmed): the test's own name promises "AndPersists"
	// -- prove it, not just that Investigate returned no error. store.saved
	// is nil until Save records something.
	if store.saved == nil {
		t.Fatal("store.saved is nil, want the gated result to have been passed to Save")
	}
	if store.saved.ResultID != result.ResultID {
		t.Fatalf("store.saved.ResultID = %q, want %q: the persisted row must be the SAME result Investigate returned", store.saved.ResultID, result.ResultID)
	}
	if len(store.saved.ConfirmedStructure) != 1 || store.saved.ConfirmedStructure[0].AppliedValue != string(SubjectPullRequest) {
		t.Fatalf("store.saved.ConfirmedStructure = %#v, want the same confirmed kind echo the returned result carries", store.saved.ConfirmedStructure)
	}
}

// TestWindowGate_ClassDefault_RecordsStructureNeedsTelemetry pins CHAOS-4118's
// own telemetry requirement (AGENTS.md: no new outcome-affecting disclosure
// ships without decision-basis telemetry in the same change): the window-only
// StructureNeeds windowConfirmationRequiredResult now composes must report
// through the SAME recordStructureNeedsTelemetry helper terminalResult's own
// kind/anchor/handle disclosure already uses, not a silent new field. This
// pins the actual observable defect a missing wire-up would produce: the
// disclosure appears on the result but the operator-facing counters never
// see it (see structure.go's own recordStructureNeedsTelemetry, which the
// WindowOptions loop was added to alongside this test).
func TestWindowGate_ClassDefault_RecordsStructureNeedsTelemetry(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	telemetry := &recordingTelemetry{}
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
		Results:   newMapResultStore(),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "window-gate-telemetry-test",
		Now:            func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_windowgatetelemetry01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest() // no EvidenceWindow -- class-default gate applies

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result, not an error", err)
	}
	if result.StructureNeeds == nil || len(result.StructureNeeds.WindowOptions) == 0 {
		t.Fatalf("result.StructureNeeds = %#v, want a non-nil window-only disclosure with options", result.StructureNeeds)
	}
	if len(telemetry.structureNeedsDisclosed) != 1 || telemetry.structureNeedsDisclosed[0] != contractsv1.ContextFabricStructureNeedWindow {
		t.Fatalf("structureNeedsDisclosed = %#v, want exactly one cf_structure_needs_disclosed{member=window} call", telemetry.structureNeedsDisclosed)
	}
	// codex xhigh review round 1 (low, addressed): assert the EXACT record
	// set, not merely "at least one match" -- a duplicate or spurious extra
	// record for another member/source would pass the weaker check.
	wantCount := len(result.StructureNeeds.WindowOptions)
	want := structureOfferCountRecord{member: contractsv1.ContextFabricStructureNeedWindow, source: contractsv1.ContextFabricStructureOfferEngine, count: wantCount}
	if len(telemetry.structureOfferCounts) != 1 || telemetry.structureOfferCounts[0] != want {
		t.Fatalf("structureOfferCounts = %#v, want exactly [%#v]", telemetry.structureOfferCounts, want)
	}
}
