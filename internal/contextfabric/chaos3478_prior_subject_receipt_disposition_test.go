package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3478/CHAOS-3813: binding candidates and prior subjects to the exact
// result they came from was already enforced on the acr side -- every
// PriorSubjectReceipts/PriorKindReceipts/PriorAnchorReceipts/
// PriorHandleReceipts/PriorCandidateReceipts entry carries an explicit
// (ResultID, ReceiptID) pair, and the engine only ever accepts a receipt
// whose named result actually offered it (ContextFabricBoundSubjectReceipt,
// resolvePriorSubjectHints, canonicalizeStructure). What was missing was
// DISCLOSURE for the one receipt kind whose failure mode was silent: a
// well-formed PriorSubjectReceipts entry that did not resolve produced no
// wire signal at all, only a server-side telemetry counter (CHAOS-3813).
// The five per-reason proofs (applied/skipped_unloadable/skipped_no_match/
// skipped_stale_graph_epoch/skipped_failed_reauth) live as assertions added
// to the existing engine_test.go prior-subject-receipt battery, right next
// to the telemetry proof each scenario already had -- this file covers the
// remaining shape questions: the empty case, and the two response paths
// (CHAOS-4077 ErrGraphNotProjected, CHAOS-4234 gated class-default window)
// that return before ResolveSubjects ever produces a real resolution to
// re-verify a matched receipt against.

// TestPriorSubjectReceiptDispositionsNilWhenNoReceiptsSent proves the
// additive-optional convention: a request that never carried
// PriorSubjectReceipts must echo nil, never an empty-but-present array --
// "nil means never attempted or nothing sent", matching every other
// empty-echo field in this package (composeConfirmedStructure's own
// convention, structure.go).
func TestPriorSubjectReceiptDispositionsNilWhenNoReceiptsSent(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	request := validInvestigationRequest() // no PriorSubjectReceipts

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.SubjectResolution.PriorSubjectReceiptDispositions != nil {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want nil when the request carried no PriorSubjectReceipts", result.SubjectResolution.PriorSubjectReceiptDispositions)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 0 {
		t.Fatalf("telemetry.priorSubjectReceiptsSkipped = %#v, want no skip telemetry when nothing was sent", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestPriorSubjectReceiptDispositionsDisclosedWhenGraphNeverProjected is
// CHAOS-4077's own interaction with this ticket: a never-projected org
// short-circuits to a clean terminal via an emptyResolution that never ran
// a real graph read, but a PriorSubjectReceipts entry the caller sent must
// still be disclosed, not silently absorbed into the "nothing here" empty
// terminal the way it would have been before CHAOS-3813.
func TestPriorSubjectReceiptDispositionsDisclosedWhenGraphNeverProjected(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_1"
	priorResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_abc12345", Subject: project, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{project},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_1": priorResult}}
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Graph: neverProjectedGraphReader{t: t},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called on the subjectless terminal path")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called on the subjectless terminal path")
			return InvestigationResult{}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_never_projected_01" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a clean terminal, not a propagated error", err)
	}
	// The receipt matched a real candidate in a real prior result -- its
	// prior-turn choice was real -- but this call never ran a graph read to
	// re-verify it (the graph does not exist), so it reads honestly as
	// skipped_failed_reauth ("not re-verified this call"), never as
	// silently omitted and never mislabeled applied.
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry.priorSubjectReceiptsSkipped = %#v, want exactly one skip of count 1", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestPriorSubjectReceiptDispositionsDisclosedUnderGatedClassDefaultWindow
// is CHAOS-4234's own interaction with this ticket: before this change,
// the gated-class-default window path (windowConfirmationRequiredResult's
// gate 2) never called recordPriorSubjectReceiptSkips at all -- a caller's
// PriorSubjectReceipts went completely unacknowledged on this path, worse
// than CHAOS-3813's original finding (that one at least had a telemetry
// counter). This proves the gap is closed: a receipt sent alongside a
// class-default-gated request is disclosed on the wire, and the gate's own
// non-decisive, offers-only contract is untouched (no committed material,
// same as every other CHAOS-4234 proof in this package).
func TestPriorSubjectReceiptDispositionsDisclosedUnderGatedClassDefaultWindow(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_1"
	priorResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_abc12345", Subject: project, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{project},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_1": priorResult}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	request := validInvestigationRequest() // no EvidenceWindow -- class-default gate applies
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a confirmation-required result", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required: the window gate still holds", result.Status)
	}
	// The CHAOS-4234 non-decisive bar is untouched by this ticket: still no
	// committed material or candidates under the gate.
	if len(result.SubjectResolution.Committed) != 0 || len(result.SubjectResolution.Candidates) != 0 {
		t.Fatalf("SubjectResolution = %#v, want NO committed material and NO candidates under the gate", result.SubjectResolution)
	}
	// The receipt matched a real candidate in a real prior result, but this
	// gate's own resolution is offers-only and discarded by the CHAOS-4234
	// ruling -- never a real re-verification -- so it reads as
	// skipped_failed_reauth, the same honest convention the
	// ErrGraphNotProjected path uses, and (the actual regression this test
	// guards) it is DISCLOSED at all, where before this ticket it was
	// dropped with no telemetry and no wire signal on this exact path.
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry.priorSubjectReceiptsSkipped = %#v, want exactly one skip of count 1 -- before this ticket this gate never called recordPriorSubjectReceiptSkips at all", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestPriorSubjectReceiptDispositionsDisclosedOnAxisConflictVeto is codex
// round-1 finding 3: the post-Interpret window-axis-conflict veto
// (windowVetoAxisConflict, window.go's windowVetoResult) returns before
// ResolveSubjects ever runs, exactly like the ErrGraphNotProjected/
// CHAOS-4234 branches -- before this fix it built a fresh SubjectResolution
// without threading dispositions through at all, and never called
// recordPriorSubjectReceiptSkips either. Mirrors
// TestCHAOS3900_AxisConflict_InterpreterFlipVetoesInsteadOfSilentlyDropping's
// own setup (window_test.go), with a PriorSubjectReceipts entry added
// alongside the window receipt.
func TestPriorSubjectReceiptDispositionsDisclosedOnAxisConflictVeto(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorWindowResult := validInvestigationResult()
	priorWindowResult.ResultID = "result_prior_window_3478"
	priorWindowResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm34780", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	priorSubjectResult := validInvestigationResult()
	priorSubjectResult.ResultID = "result_prior_subject_3478"
	priorSubjectResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_axisconflict1", Subject: project, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{project},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{
		priorWindowResult.ResultID:  priorWindowResult,
		priorSubjectResult.ResultID: priorSubjectResult,
	}}

	asOf := time.Unix(100, 0).UTC()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil // miss: proceed to Interpret
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf}}, nil
		}),
		Telemetry: telemetry,
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorWindowResult.ResultID, ReceiptID: "winr_confirm34780"}}
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: priorSubjectResult.ResultID, ReceiptID: "receipt_axisconflict1"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q (the axis conflict veto still holds)", result.Status, InvestigationNoMatch)
	}
	// This veto never ran ResolveSubjects, so even a receipt that matched a
	// real candidate in a real prior result cannot be re-verified against a
	// real resolution here -- it reads honestly as skipped_failed_reauth,
	// never silently omitted (the actual regression this test guards: the
	// old code produced NO entry and NO telemetry on this exact path).
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: priorSubjectResult.ResultID, ReceiptID: "receipt_axisconflict1", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry.priorSubjectReceiptsSkipped = %#v, want exactly one skip of count 1 -- before this ticket the axis-conflict veto never called recordPriorSubjectReceiptSkips at all", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestPriorSubjectReceiptDispositionsDisclosedWhenResultsStoreIsNil is
// codex round-1 finding 4: EngineDependencies.Results is optional
// (NewEngine accepts nil), and resolvePriorSubjectHints is gated on
// e.results != nil -- before this fix, a PriorSubjectReceipts entry sent
// to an Engine with no result store produced neither a disposition entry
// nor a telemetry count, even though the OLD code's telemetry (computed as
// receiptCount - survived, unconditional on e.results) counted every such
// receipt as skipped. Every receipt must classify as skipped_unloadable:
// with no store, nothing could possibly have been loaded.
func TestPriorSubjectReceiptDispositionsDisclosedWhenResultsStoreIsNil(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	// Results deliberately nil: mustEngineForPriorReceiptTest accepts a nil
	// store argument directly (see its own signature).
	engine := mustEngineForPriorReceiptTest(t, graph, nil, telemetry)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_00000001", ReceiptID: "receipt_abc12345678"}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a receipt sent to a nil result store to degrade safely, not fail", err)
	}
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_00000001", ReceiptID: "receipt_abc12345678", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry.priorSubjectReceiptsSkipped = %#v, want exactly one skip of count 1 -- a nil result store must not silently swallow the telemetry the old code recorded here", telemetry.priorSubjectReceiptsSkipped)
	}
}
