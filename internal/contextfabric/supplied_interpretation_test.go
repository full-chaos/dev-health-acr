package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// modelFreeRuntime fails the test when a model call of either kind is made.
type modelFreeRuntime struct{ t *testing.T }

func (m modelFreeRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	m.t.Helper()
	m.t.Fatal("the model runtime interpreted a request that carries its own interpretation")
	return InterpretedQuestion{}, ModelExecutionReceipt{}, nil
}

func (m modelFreeRuntime) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	m.t.Helper()
	m.t.Fatal("the interpreter's model runtime was asked to synthesize")
	return SynthesisDraft{}, ModelExecutionReceipt{}, nil
}

// scriptedSuppliedRuntime is a SuppliedInterpretationRuntime that returns a
// fixed answer and records each request it was given. contractErr is what
// its contract check returns; checks counts those checks.
type scriptedSuppliedRuntime struct {
	interpreted InterpretedQuestion
	receipt     ModelExecutionReceipt
	err         error
	requests    []InvestigationRequest
	contractErr error
	checks      int
}

func (s *scriptedSuppliedRuntime) CheckSuppliedContract(context.Context, storage.Principal, InvestigationRequest) error {
	s.checks++
	return s.contractErr
}

func (s *scriptedSuppliedRuntime) InterpretSuppliedQuestion(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	s.requests = append(s.requests, request)
	return s.interpreted, s.receipt, s.err
}

// resolveRecordingGraph records the interpretation and principal each
// ResolveSubjects call received.
type resolveRecordingGraph struct {
	graphReaderStub
	interpreted *[]InterpretedQuestion
	principals  *[]storage.Principal
}

func (g resolveRecordingGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, kind *ConfirmedExpectedKind, anchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	*g.interpreted = append(*g.interpreted, interpreted)
	*g.principals = append(*g.principals, principal)
	return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, kind, anchor, frame, scopeAnchorKind)
}

const clientInterpretationIdentity = "client-supplied/claude-test"

func suppliedInvestigationRequest(base InvestigationRequest) InvestigationRequest {
	base.SuppliedInterpretation = &SuppliedInterpretation{
		Output: json.RawMessage(`{}`), ModelOutputVersion: "schema-v1", PromptVersion: "prompt-v1",
	}
	return base
}

func clientInterpretReceipt() ModelExecutionReceipt {
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.Provider, receipt.Model, receipt.ModelVersion = contractsv1.ContextFabricClientSuppliedProvider, "claude-test", "n/a"
	return receipt
}

const synthesisModelIdentity = "test-provider/synthesis-model"

func suppliedInterpretation() InterpretedQuestion {
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactReadiness}},
	}
}

// decisiveSynthesis is the answer the rig's synthesizer writes. Its
// model_identity is the synthesis model's.
func decisiveSynthesis() InvestigationResult {
	return InvestigationResult{
		Status: InvestigationComplete, DirectJudgment: "Ask Dev is not ready to ship.",
		CurrentState: "Release-readiness blockers remain.", StrongestPressures: []string{},
		Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
		Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Ask Dev is not ready to ship because release-readiness blockers remain.", Warnings: []string{},
		Versions: VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "schema-v1", SynthesisVersion: "synthesis-v1", ModelIdentity: synthesisModelIdentity,
		},
	}
}

type suppliedEngineRig struct {
	engine      *Engine
	store       *resultStoreStub
	telemetry   *recordingTelemetry
	sink        *fakeReceiptSink
	interpreted []InterpretedQuestion
	principals  []storage.Principal
	lookups     int
	syntheses   int
	snapshot    SourceWatermarkSnapshot
}

// newSuppliedEngineRig builds an engine whose fresh path commits one project
// and synthesizes a complete answer, with a reuse gate that counts its
// lookups and a watermark snapshot and epoch for Save to receive.
func newSuppliedEngineRig(t *testing.T, interpreter RuntimeQuestionInterpreter) *suppliedEngineRig {
	t.Helper()
	rig := &suppliedEngineRig{
		store: &resultStoreStub{}, telemetry: &recordingTelemetry{}, sink: &fakeReceiptSink{},
		snapshot: SourceWatermarkSnapshot{"linear": "wm-1"},
	}
	interpreter.Sink = rig.sink
	var events []string
	rig.engine = mustReuseTestEngine(t, EngineDependencies{
		Graph: resolveRecordingGraph{
			graphReaderStub: graphReaderStub{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}}},
				context: GraphContext{
					Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
					FactRequirements: []FactRequirement{{Kind: FactBlockers}, {Kind: FactReadiness}},
					EvidenceRefIDs:   []string{"evidence_project_status"},
					Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				},
			},
			interpreted: &rig.interpreted, principals: &rig.principals,
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			rig.syntheses++
			return decisiveSynthesis(), nil
		}),
		Interpreter: interpreter,
		Results:     rig.store,
		Telemetry:   rig.telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			rig.lookups++
			return InvestigationResult{}, false, nil
		}),
		ReuseSnapshotter:      orderTrackingSnapshotter{events: &events, snapshot: rig.snapshot},
		ReuseEpochSnapshotter: orderTrackingEpochSnapshotter{events: &events, epoch: 7},
	})
	return rig
}

func TestSuppliedInterpretationReachesResolutionWithNoModelInterpretCall(t *testing.T) {
	t.Parallel()
	supplied := &scriptedSuppliedRuntime{interpreted: suppliedInterpretation(), receipt: clientInterpretReceipt()}
	rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied})
	request := suppliedInvestigationRequest(validInvestigationRequest())
	principal := reusePrincipal()

	result, err := rig.engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(supplied.requests) != 1 || supplied.requests[0].SuppliedInterpretation == nil {
		t.Fatalf("supplied runtime calls = %#v, want exactly one, carrying the supplied interpretation", supplied.requests)
	}
	if len(rig.interpreted) != 1 || !reflect.DeepEqual(rig.interpreted[0], suppliedInterpretation()) {
		t.Fatalf("resolution received %#v, want the supplied interpretation exactly once", rig.interpreted)
	}
	if !reflect.DeepEqual(rig.principals, []storage.Principal{principal}) {
		t.Fatalf("resolution principals = %#v, want the caller's own principal", rig.principals)
	}
	if len(rig.sink.recorded) != 1 || rig.sink.recorded[0].Provider != contractsv1.ContextFabricClientSuppliedProvider {
		t.Fatalf("receipt sink = %#v, want the one client-supplied interpret receipt", rig.sink.recorded)
	}
	if rig.syntheses != 1 || result.Status != InvestigationComplete || result.DirectJudgment != decisiveSynthesis().DirectJudgment {
		t.Fatalf("syntheses = %d Status = %q judgment = %q, want the one synthesized answer served", rig.syntheses, result.Status, result.DirectJudgment)
	}
}

func TestDecisiveResultNamesWhoInterpretedAndKeepsTheSynthesisModelIdentity(t *testing.T) {
	t.Parallel()
	const synthesisIdentity = synthesisModelIdentity

	supplied := &scriptedSuppliedRuntime{interpreted: suppliedInterpretation(), receipt: clientInterpretReceipt()}
	client := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied})
	clientResult, err := client.engine.Investigate(context.Background(), reusePrincipal(), suppliedInvestigationRequest(validInvestigationRequest()))
	if err != nil {
		t.Fatalf("supplied path: Investigate() error = %v", err)
	}
	if client.syntheses != 1 || clientResult.Status != InvestigationComplete {
		t.Fatalf("supplied path: syntheses = %d Status = %q, want the decisive path", client.syntheses, clientResult.Status)
	}
	if got := clientResult.Versions; got.InterpretationSource != InterpretationSourceClient || got.InterpretationModelIdentity != clientInterpretationIdentity || got.ModelIdentity != synthesisIdentity {
		t.Fatalf("supplied path Versions = %#v, want interpretation_source=client, interpretation_model_identity=%q and model_identity unchanged at %q", got, clientInterpretationIdentity, synthesisIdentity)
	}
	if !reflect.DeepEqual(client.store.saved.Versions, clientResult.Versions) {
		t.Fatalf("saved Versions = %#v, served = %#v, want the stored row to carry the same provenance", client.store.saved.Versions, clientResult.Versions)
	}

	server := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: suppliedInterpretation(), receipt: validModelReceiptFixture(ModelOperationInterpret)}})
	serverResult, err := server.engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("server path: Investigate() error = %v", err)
	}
	if server.syntheses != 1 || serverResult.Status != InvestigationComplete {
		t.Fatalf("server path: syntheses = %d Status = %q, want the decisive path", server.syntheses, serverResult.Status)
	}
	if got := serverResult.Versions; got.InterpretationSource != InterpretationSourceServer || got.InterpretationModelIdentity != "test-provider/test-model" || got.ModelIdentity != synthesisIdentity {
		t.Fatalf("server path Versions = %#v, want interpretation_source=server, interpretation_model_identity=test-provider/test-model and model_identity %q", got, synthesisIdentity)
	}
}

func TestTerminalResultOfASuppliedInterpretationNamesTheClient(t *testing.T) {
	t.Parallel()
	supplied := &scriptedSuppliedRuntime{interpreted: bootstrapInterpretation(), receipt: clientInterpretReceipt()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied}, graph, newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), suppliedInvestigationRequest(validInvestigationRequestWithConfirmedWindow()))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match", result.Status)
	}
	versions := result.Versions
	if versions.InterpretationSource != InterpretationSourceClient || versions.InterpretationModelIdentity != clientInterpretationIdentity {
		t.Fatalf("Versions = %#v, want the client named as the interpreter", versions)
	}
	if versions.ModelIdentity != clientInterpretationIdentity || versions.SynthesisVersion != SynthesisVersionNotSynthesized {
		t.Fatalf("Versions = %#v, want model_identity to name the interpret call of a result with no synthesis", versions)
	}
}

func TestResultWithNoInterpretCallCarriesNoInterpretationProvenance(t *testing.T) {
	t.Parallel()
	engine := buildTerminalEngine(t, &acceptanceGraphReader{resolution: ambiguousResolution("Which one?"), context: emptyGraphContext()}, nil)
	versions := engine.terminalVersions(context.Background())
	if versions.InterpretationSource != "" || versions.InterpretationModelIdentity != "" {
		t.Fatalf("Versions = %#v, want no interpretation provenance when no interpret call ran", versions)
	}
}

// TestSuppliedInterpretationNeverReadsOrWritesAnswerReuse pins both halves of
// the isolation against a server-interpreted control on the same rig: the
// control consults the reuse gate and saves with the snapshot and epoch, so
// the supplied path's zero lookups and nil save inputs are the bypass and the
// save guard, not a rig that never reuses.
func TestSuppliedInterpretationNeverReadsOrWritesAnswerReuse(t *testing.T) {
	t.Parallel()
	server := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: suppliedInterpretation(), receipt: validModelReceiptFixture(ModelOperationInterpret)}})
	if _, err := server.engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("server path: Investigate() error = %v", err)
	}
	if server.lookups != 1 || len(server.telemetry.answerReuseBypasses) != 0 || server.syntheses != 1 {
		t.Fatalf("server path: lookups = %d bypasses = %v syntheses = %d, want one lookup, no bypass and the decisive path", server.lookups, server.telemetry.answerReuseBypasses, server.syntheses)
	}
	if !reflect.DeepEqual(server.store.savedSnapshot, server.snapshot) || server.store.savedEpoch == nil || *server.store.savedEpoch != 7 {
		t.Fatalf("server path: Save snapshot = %v epoch = %v, want the reusable inputs", server.store.savedSnapshot, server.store.savedEpoch)
	}

	supplied := &scriptedSuppliedRuntime{interpreted: suppliedInterpretation(), receipt: clientInterpretReceipt()}
	client := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied})
	if _, err := client.engine.Investigate(context.Background(), reusePrincipal(), suppliedInvestigationRequest(validInvestigationRequest())); err != nil {
		t.Fatalf("supplied path: Investigate() error = %v", err)
	}
	if client.lookups != 0 {
		t.Fatalf("supplied path: reuse lookups = %d, want none", client.lookups)
	}
	if want := []AnswerReuseBypassReason{AnswerReuseBypassSuppliedInterpretation}; !reflect.DeepEqual(client.telemetry.answerReuseBypasses, want) {
		t.Fatalf("supplied path: bypasses = %v, want %v", client.telemetry.answerReuseBypasses, want)
	}
	if client.store.saved.ResultID == "" || client.syntheses != 1 || client.store.saved.Status != InvestigationComplete {
		t.Fatalf("supplied path: saved = %q syntheses = %d Status = %q, want the decisive answer saved", client.store.saved.ResultID, client.syntheses, client.store.saved.Status)
	}
	if client.store.savedSnapshot != nil || client.store.savedEpoch != nil {
		t.Fatalf("supplied path: Save snapshot = %v epoch = %v, want nil for both: the row must never be reusable", client.store.savedSnapshot, client.store.savedEpoch)
	}
}

func TestReuseBypassReasonNamesASuppliedInterpretationFirst(t *testing.T) {
	t.Parallel()
	request := suppliedInvestigationRequest(validInvestigationRequest())
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectProject, PatternID: "p", Value: "v"}}
	if got := reuseBypassReason(request, requestStructureCanonicalization{}); got != AnswerReuseBypassSuppliedInterpretation {
		t.Fatalf("bypass = %q, want %q", got, AnswerReuseBypassSuppliedInterpretation)
	}
	if got := reuseBypassReason(validInvestigationRequest(), requestStructureCanonicalization{}); got != "" {
		t.Fatalf("bypass = %q for a plain request, want none", got)
	}
}

func TestInterpreterMarksTheSourceOfTheInterpretation(t *testing.T) {
	t.Parallel()
	supplied := &scriptedSuppliedRuntime{interpreted: suppliedInterpretation(), receipt: clientInterpretReceipt()}
	_, outcome, err := RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied}.
		Interpret(context.Background(), reusePrincipal(), suppliedInvestigationRequest(validInvestigationRequest()))
	if err != nil {
		t.Fatalf("supplied path: Interpret() error = %v", err)
	}
	want := InterpretationStamp{Ran: true, InterpretationVersion: "schema-v1", ModelIdentity: clientInterpretationIdentity, Source: InterpretationSourceClient}
	if outcome.Interpretation != want {
		t.Fatalf("supplied path stamp = %#v, want %#v", outcome.Interpretation, want)
	}

	_, outcome, err = RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: suppliedInterpretation(), receipt: validModelReceiptFixture(ModelOperationInterpret)}}.
		Interpret(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("server path: Interpret() error = %v", err)
	}
	if outcome.Interpretation.Source != InterpretationSourceServer {
		t.Fatalf("server path source = %q, want %q", outcome.Interpretation.Source, InterpretationSourceServer)
	}
}

func TestSuppliedInterpretationWithNoSuppliedRuntimeIsRefusedWithoutAModelCall(t *testing.T) {
	t.Parallel()
	_, _, err := RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}}.
		Interpret(context.Background(), reusePrincipal(), suppliedInvestigationRequest(validInvestigationRequest()))
	if !errors.Is(err, ErrSuppliedInterpretationUnsupported) {
		t.Fatalf("error = %v, want ErrSuppliedInterpretationUnsupported", err)
	}
}

func TestRejectedSuppliedInterpretationIsReturnedAndNeverReinterpretedByTheModel(t *testing.T) {
	t.Parallel()
	rejected := clientInterpretReceipt()
	rejected.Outcome = "invalid_output"
	rejection := ClassifyInterpretationRejection(InterpretedQuestion{}, errors.New("interpreted question violates v1 bounds"), nil)
	rejected.InterpretationRejectionReason = InterpretationRejectionReasonOf(rejection)
	supplied := &scriptedSuppliedRuntime{receipt: rejected, err: rejection}
	rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied})

	_, err := rig.engine.Investigate(context.Background(), reusePrincipal(), suppliedInvestigationRequest(validInvestigationRequest()))
	if !errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("error = %v, want the rejection returned to the caller", err)
	}
	if stage, ok := FailureStage(err); !ok || stage != StageInterpretation {
		t.Fatalf("failure stage = %q, want %q", stage, StageInterpretation)
	}
	if len(rig.interpreted) != 0 {
		t.Fatalf("resolution ran %d times for a rejected interpretation, want none", len(rig.interpreted))
	}
	if len(rig.sink.recorded) != 1 || rig.sink.recorded[0].Outcome != "invalid_output" {
		t.Fatalf("receipt sink = %#v, want the rejected receipt recorded once", rig.sink.recorded)
	}
}

func TestContractMismatchOfASuppliedInterpretationReachesTheCallerTyped(t *testing.T) {
	t.Parallel()
	refusal := InterpretationContractRefusal{
		Mismatch: []string{contractsv1.ContextFabricInterpretationContractFieldPromptVersion},
		Current: InterpretationContract{
			ModelOutputVersion: "schema-v1", PromptVersion: "prompt-v2",
			SystemSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	supplied := &scriptedSuppliedRuntime{err: &SuppliedInterpretationContractMismatch{Refusal: refusal}}
	rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: supplied})

	_, err := rig.engine.Investigate(context.Background(), reusePrincipal(), suppliedInvestigationRequest(validInvestigationRequest()))
	var mismatch *SuppliedInterpretationContractMismatch
	if !errors.As(err, &mismatch) || !reflect.DeepEqual(mismatch.Refusal, refusal) {
		t.Fatalf("error = %v, want the contract mismatch with its refusal intact", err)
	}
	if len(rig.sink.recorded) != 0 || len(rig.interpreted) != 0 || rig.store.saved.ResultID != "" {
		t.Fatalf("receipts = %d resolutions = %d saved = %q, want nothing recorded, resolved or saved", len(rig.sink.recorded), len(rig.interpreted), rig.store.saved.ResultID)
	}
}

// TestSuppliedInterpretationContractIsCheckedAboveEveryExitOfTheTurn runs
// supplied interpretations on turns the engine ends before its interpret
// step: a prior window receipt and a prior kind receipt of a result that does
// not exist. A contract the runtime refuses ends the turn with that refusal
// and nothing saved. A contract it accepts lets the turn reach the engine's
// own refusal of the receipt, with one check and no interpretation.
func TestSuppliedInterpretationContractIsCheckedAboveEveryExitOfTheTurn(t *testing.T) {
	t.Parallel()
	refusal := InterpretationContractRefusal{
		Mismatch: []string{contractsv1.ContextFabricInterpretationContractFieldSystemSHA256},
		Current: InterpretationContract{
			ModelOutputVersion: "schema-v1", PromptVersion: "prompt-v2",
			SystemSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	exits := map[string]func(*InvestigationRequest){
		"unresolved window receipt": func(r *InvestigationRequest) {
			r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
		},
		"unresolved kind receipt": func(r *InvestigationRequest) {
			r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "kindr_confirm0001"}}
		},
	}
	for name, exit := range exits {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := suppliedInvestigationRequest(validInvestigationRequest())
			exit(&request)

			accepted := &scriptedSuppliedRuntime{interpreted: suppliedInterpretation(), receipt: clientInterpretReceipt()}
			control := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: accepted})
			served, err := control.engine.Investigate(context.Background(), reusePrincipal(), request)
			if err != nil {
				t.Fatalf("accepted contract: Investigate() error = %v, want the engine's own refusal of the receipt", err)
			}
			if served.Status != InvestigationNoMatch || accepted.checks != 1 || len(accepted.requests) != 0 || len(control.interpreted) != 0 {
				t.Fatalf("accepted contract: Status = %q contract checks = %d interpretations = %d resolutions = %d, want a no_match turn that checked the contract once and interpreted nothing",
					served.Status, accepted.checks, len(accepted.requests), len(control.interpreted))
			}

			refused := &scriptedSuppliedRuntime{contractErr: &SuppliedInterpretationContractMismatch{Refusal: refusal}}
			rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}, Supplied: refused})
			_, err = rig.engine.Investigate(context.Background(), reusePrincipal(), request)
			var mismatch *SuppliedInterpretationContractMismatch
			if !errors.As(err, &mismatch) || !reflect.DeepEqual(mismatch.Refusal, refusal) {
				t.Fatalf("refused contract: error = %v, want the contract mismatch with its refusal intact", err)
			}
			if stage, ok := FailureStage(err); !ok || stage != StageInterpretation {
				t.Fatalf("refused contract: failure stage = %q, want %q", stage, StageInterpretation)
			}
			if refused.checks != 1 || len(refused.requests) != 0 || rig.store.saved.ResultID != "" || rig.lookups != 0 || len(rig.sink.recorded) != 0 {
				t.Fatalf("refused contract: checks = %d interpretations = %d saved = %q lookups = %d receipts = %d, want one check and nothing interpreted, saved, looked up or recorded",
					refused.checks, len(refused.requests), rig.store.saved.ResultID, rig.lookups, len(rig.sink.recorded))
			}
		})
	}
}

// TestSuppliedInterpretationIsRefusedAtTheStartOfTheTurnWhenNothingCanCheckIt
// covers the two interpreters that cannot check a supplied interpretation:
// the production interpreter with no supplied runtime, and an interpreter
// that is not a SuppliedInterpretationGate. Each turn ends before any exit
// that serves a result, here the refusal of an unresolved window receipt.
func TestSuppliedInterpretationIsRefusedAtTheStartOfTheTurnWhenNothingCanCheckIt(t *testing.T) {
	t.Parallel()
	request := suppliedInvestigationRequest(validInvestigationRequest())
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
	noRuntime := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Runtime: modelFreeRuntime{t: t}})
	if _, err := noRuntime.engine.Investigate(context.Background(), reusePrincipal(), request); !errors.Is(err, ErrSuppliedInterpretationUnsupported) || noRuntime.store.saved.ResultID != "" {
		t.Fatalf("no supplied runtime: error = %v saved = %q, want ErrSuppliedInterpretationUnsupported and nothing saved", err, noRuntime.store.saved.ResultID)
	}

	store := &resultStoreStub{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{},
		Results: store,
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("an interpreter that cannot check a supplied interpretation was asked to interpret one")
			return InterpretedQuestion{}, nil
		}),
	})
	if _, err := engine.Investigate(context.Background(), reusePrincipal(), request); !errors.Is(err, ErrSuppliedInterpretationUnsupported) || store.saved.ResultID != "" {
		t.Fatalf("interpreter with no gate: error = %v saved = %q, want ErrSuppliedInterpretationUnsupported and nothing saved", err, store.saved.ResultID)
	}
}
