package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4690: sweep tests proving applyCoverageDisplayLabels (and, for the
// decisive path, MergeCoverage's own Details half) actually runs at EVERY
// fresh-result exit from Investigate -- engine.go's decisive path,
// unresolved.go's terminalResult, window.go's windowVetoResult and
// windowConfirmationRequiredResult, and structure.go's structureVetoResult
// -- never on tryReuse's reuse path. Mirrors the CHAOS-4636 "sweep 2"
// discipline the brief names: enumerate every exit, not just the ones a
// bug report happened to name.
//
// These are also the RED-FIRST proofs for the brief's "details present on
// the merged result coverage for a fact degradation and a graph
// degradation" requirement: on parent ca02a246 neither
// FactCapabilityRegistry/falkorgraph.Adapter nor mergeCoverage populate
// Coverage.Details at all, so result.Coverage.Details is empty on that
// commit for both scenarios below -- red there, green on this tip.

// buildAcceptanceEngineWithTelemetry mirrors acceptance_test.go's own
// buildAcceptanceEngine exactly, adding an EngineTelemetry so a test can
// assert RecordEvidenceLabelFallback fired. Not added to
// buildAcceptanceEngine itself to avoid touching every existing call site.
func buildAcceptanceEngineWithTelemetry(t *testing.T, graph GraphReader, facts CanonicalFactReader, interpretation InterpretedQuestion, draft SynthesisDraft, results InvestigationResultStore, telemetry EngineTelemetry) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: interpretation, draft: draft, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Results:     results,
		Telemetry:   telemetry,
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_acceptance01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// --- Decisive path: fact degradation ---

func TestChaos4690_DecisiveResult_FactDegradationCarriesCoverageDetailsAndLabels(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	raw := "readiness: canonical fact capability timed out"
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return CanonicalFactBundle{
			Facts: []CanonicalFact{{Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")}, EvidenceRefIDs: []string{"evidence_status_0001"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"}},
			Coverage: Coverage{
				Sources:         []SourceObservation{{Source: "canonical_fact:status", State: SourceAvailable}, {Source: "canonical_fact:readiness", State: SourceUnavailable, Reason: "canonical fact capability timed out"}},
				Partial:         true,
				DegradedReasons: []string{raw},
				Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactReadFailed, "canonical_fact:readiness", true, raw, func(d *CoverageDetail) {
					d.FactKind = FactReadiness
					d.SourceState = SourceUnavailable
				})},
			},
			Version: "ops-v1",
		}, nil
	})
	draft := SynthesisDraft{
		Status: InvestigationPartial, DirectJudgment: "Ask Dev status is in progress; readiness could not be evaluated.",
		CurrentState: "Readiness data is unavailable.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
		RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations:    []string{"Readiness evaluation was unavailable for this investigation."},
		EvidenceRefIDs: []string{"evidence_status_0001"}, ClaimedFacts: []ClaimedFact{},
		DeterministicAnswer: "placeholder", Warnings: []string{},
	}
	telemetry := &recordingTelemetry{}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), draft, nil, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.Coverage.Details) != 1 {
		t.Fatalf("Coverage.Details = %#v, want exactly the one paired fact-degradation detail carried through the merge", result.Coverage.Details)
	}
	if result.Coverage.Details[0].Raw != raw || !result.Coverage.Details[0].Degrading {
		t.Fatalf("Coverage.Details[0] = %#v, want it degrading with Raw = %q", result.Coverage.Details[0], raw)
	}
	if result.Coverage.Details[0].Label == "" {
		t.Fatal("Coverage.Details[0].Label is empty, want the composed display label")
	}
	for _, source := range result.Coverage.Sources {
		if source.Label == "" || source.StateLabel == "" {
			t.Fatalf("source %#v carries no Label/StateLabel", source)
		}
	}
	if result.EvidenceRefLabels == nil {
		t.Fatal("EvidenceRefLabels is nil on a fresh decisive result, want a non-nil map")
	}
}

// --- Decisive path: graph degradation ---

func TestChaos4690_DecisiveResult_GraphDegradationCarriesCoverageDetailsAndLabels(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	raw := "endpoint_lookup_failed:2"
	count := 2
	graphContext := bootstrapGraphContext(project)
	graphContext.Coverage = Coverage{
		Sources:         []SourceObservation{},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, raw, func(d *CoverageDetail) {
			d.Count = &count
		})},
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    graphContext,
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	draft := bootstrapDraft(project)
	draft.Status = InvestigationPartial
	telemetry := &recordingTelemetry{}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), draft, nil, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.Coverage.Details) != 1 || result.Coverage.Details[0].Raw != raw {
		t.Fatalf("Coverage.Details = %#v, want exactly the one paired graph-degradation detail carried through the merge", result.Coverage.Details)
	}
	if !result.Coverage.Details[0].Degrading || result.Coverage.Details[0].Code != contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed {
		t.Fatalf("Coverage.Details[0] = %#v, want the graph endpoint-lookup-failed code preserved", result.Coverage.Details[0])
	}
}

// --- Decisive path: evidence-label fallback telemetry ---

func TestChaos4690_DecisiveResult_RecordsEvidenceLabelFallbackTelemetry(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	draft := bootstrapDraft(project)
	// "evidence_readiness_0001" is not acr:v1:<type>:<id> shaped -- an
	// unknown-shape ref, so its label falls back and the fallback counter
	// must fire exactly once.
	telemetry := &recordingTelemetry{}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), draft, nil, telemetry)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.EvidenceRefLabels) == 0 {
		t.Fatal("EvidenceRefLabels is empty, want the driver's own evidence ref labeled (even generically)")
	}
	if len(telemetry.evidenceLabelFallbacks) != 1 || telemetry.evidenceLabelFallbacks[0] < 1 {
		t.Fatalf("telemetry.evidenceLabelFallbacks = %#v, want exactly one call reporting >=1 fallback", telemetry.evidenceLabelFallbacks)
	}
}

// --- Terminal path (unresolved.go's terminalResult) ---

func TestChaos4690_TerminalResult_StampsSourceLabelsAndEvidenceRefLabels(t *testing.T) {
	t.Parallel()
	const prompt = "Which subject did you mean: Ask Dev (Platform), Ask Dev (Growth)?"
	graphCtx := emptyGraphContext()
	graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
	graph := &acceptanceGraphReader{resolution: ambiguousResolution(prompt), context: graphCtx}
	engine := buildTerminalEngine(t, graph, newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required (sanity check on the terminal path taken)", result.Status)
	}
	if len(result.Coverage.Sources) != 1 || result.Coverage.Sources[0].Label == "" || result.Coverage.Sources[0].StateLabel == "" {
		t.Fatalf("Coverage.Sources = %#v, want the terminal's OWN (never-merged) source labeled too", result.Coverage.Sources)
	}
	if result.EvidenceRefLabels == nil {
		t.Fatal("EvidenceRefLabels is nil on a fresh terminal result, want a non-nil (empty) map")
	}
}

// --- Window veto (window.go's windowVetoResult) ---

func TestChaos4690_WindowVetoResult_StampsEvidenceRefLabels(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})
	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match (sanity check on the veto path taken)", result.Status)
	}
	if result.EvidenceRefLabels == nil {
		t.Fatal("EvidenceRefLabels is nil on a fresh window-veto result, want a non-nil (empty) map")
	}
}

// --- Structure veto (structure.go's structureVetoResult) ---

func TestChaos4690_StructureVetoResult_StampsEvidenceRefLabels(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})
	request := validInvestigationRequest()
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "ancr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match (sanity check on the veto path taken)", result.Status)
	}
	if result.EvidenceRefLabels == nil {
		t.Fatal("EvidenceRefLabels is nil on a fresh structure-veto result, want a non-nil (empty) map")
	}
}

// --- Window confirmation required (window.go's windowConfirmationRequiredResult, gate 1) ---

func TestChaos4690_WindowConfirmationRequiredResult_StampsEvidenceRefLabels(t *testing.T) {
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
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required (sanity check on the gate path taken)", result.Status)
	}
	if result.EvidenceRefLabels == nil {
		t.Fatal("EvidenceRefLabels is nil on a fresh window-confirmation-required result, want a non-nil (empty) map")
	}
}
