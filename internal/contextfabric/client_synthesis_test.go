package contextfabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const clientTestMaxBytes = 128 << 10

// clientTestEncode is the injected encoder of the tests: a deterministic JSON
// view of the input that reports an overflow past the bound, as the
// production encoder does.
func clientTestEncode(orgID string, input SynthesisInput, maxBytes int) ([]byte, error) {
	encoded, err := json.Marshal(map[string]any{
		"org_id": orgID, "question": input.Request.Question, "facts": input.Facts.Facts, "paths": input.Graph.Paths,
	})
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxBytes {
		return nil, &ModelInputOverflow{Bytes: len(encoded), MaxBytes: maxBytes}
	}
	return encoded, nil
}

// clientRuntime is the model runtime of the tests. It counts every call and
// records the synthesis input each synthesize call was given.
type clientRuntime struct {
	mu             sync.Mutex
	interpreted    InterpretedQuestion
	draft          SynthesisDraft
	synthErr       error
	interpretCalls int
	synthCalls     int
	inputs         []SynthesisInput
}

func (r *clientRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interpretCalls++
	return r.interpreted, validModelReceiptFixture(ModelOperationInterpret), nil
}

func (r *clientRuntime) SynthesizeAnswer(_ context.Context, _ storage.Principal, input SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synthCalls++
	r.inputs = append(r.inputs, input)
	return r.draft, validModelReceiptFixture(ModelOperationSynthesize), r.synthErr
}

// clientRig is a real Engine over the real RuntimeAnswerSynthesizer and the
// real interpreter adapter, with a counting model runtime, fact reader and
// reuse gate.
type clientRig struct {
	t         *testing.T
	runtime   *clientRuntime
	store     *resultStoreStub
	telemetry *recordingTelemetry
	project   SubjectRef
	graph     graphReaderStub
	facts     []CanonicalFact
	assembly  *ClientSynthesisAssembly
	// synthesizer replaces the production synthesizer when set.
	synthesizer AnswerSynthesizer
	reuse       *InvestigationResult
	encoded     [][]byte
	factReads   int
	lookups     int
	engine      *Engine
}

func newClientRig(t *testing.T, mutate func(*clientRig)) *clientRig {
	t.Helper()
	project := acceptanceProject()
	rig := &clientRig{
		t: t, project: project, store: &resultStoreStub{}, telemetry: &recordingTelemetry{},
		runtime: &clientRuntime{interpreted: bootstrapInterpretation(), draft: bootstrapDraft(project)},
		graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context:    bootstrapGraphContext(project),
			bases:      provenCommitBases(project),
		},
		facts: bootstrapFactBundle(project).Facts,
	}
	rig.assembly = &ClientSynthesisAssembly{
		PromptVersion: "synthesis-prompt-test", ModelOutputVersion: "synthesis-output-test",
		SystemSHA256: hex.EncodeToString(sha256Sum("system")), Rules: []string{"rule one", "rule two"},
		MaxBytes: clientTestMaxBytes,
		Encode: func(orgID string, input SynthesisInput, maxBytes int) ([]byte, error) {
			encoded, err := clientTestEncode(orgID, input, maxBytes)
			if err == nil {
				rig.encoded = append(rig.encoded, encoded)
			}
			return encoded, err
		},
	}
	if mutate != nil {
		mutate(rig)
	}
	var synthesizer AnswerSynthesizer = RuntimeAnswerSynthesizer{
		Runtime: rig.runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acr-test", Backend: "graph"},
		Telemetry: rig.telemetry, ClientSynthesis: rig.assembly,
	}
	if rig.synthesizer != nil {
		synthesizer = rig.synthesizer
	}
	events := []string{}
	deps := EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: rig.runtime},
		Graph:       rig.graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			rig.factReads++
			bundle := bootstrapFactBundle(rig.project)
			bundle.Facts = rig.facts
			return bundle, nil
		}),
		Synthesizer: synthesizer,
		Results:     rig.store,
		Telemetry:   rig.telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			rig.lookups++
			if rig.reuse != nil {
				return *rig.reuse, true, nil
			}
			return InvestigationResult{}, false, nil
		}),
		ReuseSnapshotter:      orderTrackingSnapshotter{events: &events, snapshot: SourceWatermarkSnapshot{"linear": "wm-1"}},
		ReuseEpochSnapshotter: orderTrackingEpochSnapshotter{events: &events, epoch: 7},
	}
	rig.engine = mustReuseTestEngine(t, deps)
	return rig
}

func sha256Sum(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

func clientRequest() InvestigationRequest {
	request := validInvestigationRequestWithConfirmedWindow()
	request.SynthesisMode = SynthesisModeClient
	return request
}

// The reuse tests ask without an evidence window: a window keyed lookup
// needs a stored window the fixture row does not carry.
func clientRequestNoWindow() InvestigationRequest {
	request := validInvestigationRequest()
	request.SynthesisMode = SynthesisModeClient
	return request
}

func serverRequestNoWindow() InvestigationRequest {
	return validInvestigationRequest()
}

func serverRequest() InvestigationRequest {
	return validInvestigationRequestWithConfirmedWindow()
}

// investigate runs one turn with a collector installed and returns what the
// collector holds afterwards.
func (r *clientRig) investigate(request InvestigationRequest) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error) {
	r.t.Helper()
	ctx, collected := WithSynthesisInputCollector(context.Background())
	result, err := r.engine.Investigate(ctx, reusePrincipal(), request)
	return result, collected(), err
}

func clientManyFacts(project SubjectRef, count int) []CanonicalFact {
	facts := make([]CanonicalFact, 0, count)
	for index := 0; index < count; index++ {
		other := SubjectRef{Kind: SubjectProject, CanonicalID: fmt.Sprintf("project_other_%03d", index), Label: fmt.Sprintf("Other %03d", index)}
		facts = append(facts, CanonicalFact{
			Kind: FactStatus, Subject: other, Fields: map[string]FactValue{"status": StringFactValue(strings.Repeat("x", 200))},
			EvidenceRefIDs: []string{fmt.Sprintf("evidence_other_%03d", index)}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	return append(facts, bootstrapFactBundle(project).Facts...)
}

// T1: a client mode turn makes no model synthesis call and serves the
// facts-only result, the one bundle and a row without it.
func TestClientSynthesisTurnServesTheInputAndMakesNoModelCall(t *testing.T) {
	t.Parallel()
	client := newClientRig(t, nil)
	result, bundle, err := client.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if client.runtime.synthCalls != 0 || client.runtime.interpretCalls != 1 {
		t.Fatalf("model calls: synthesize = %d interpret = %d, want 0 and 1", client.runtime.synthCalls, client.runtime.interpretCalls)
	}
	if result.Status != InvestigationPartial {
		t.Fatalf("Status = %q, want partial", result.Status)
	}
	if len(result.Drivers) != 0 || len(result.ClaimedFacts) != 0 || len(result.RemainingWork) != 0 || len(result.ReadinessGaps) != 0 || len(result.Conflicts) != 0 || len(result.StrongestPressures) != 0 {
		t.Fatalf("result carries model-shaped content: drivers=%d claims=%d work=%d gaps=%d conflicts=%d pressures=%d",
			len(result.Drivers), len(result.ClaimedFacts), len(result.RemainingWork), len(result.ReadinessGaps), len(result.Conflicts), len(result.StrongestPressures))
	}
	for name, text := range map[string]string{"direct_judgment": result.DirectJudgment, "current_state": result.CurrentState, "deterministic_answer": result.DeterministicAnswer} {
		if text != contractsv1.ContextFabricClientSynthesisAnswer {
			t.Fatalf("%s = %q, want the fixed client synthesis answer", name, text)
		}
	}
	want := map[string]string{
		"synthesis_source": string(result.Versions.SynthesisSource), "synthesis_version": result.Versions.SynthesisVersion,
		"model_identity": result.Versions.ModelIdentity, "interpretation_version": result.Versions.InterpretationVersion,
	}
	if want["synthesis_source"] != "client" || want["synthesis_version"] != SynthesisVersionNotSynthesized || want["model_identity"] != "unwired" || want["interpretation_version"] != "schema-v1" {
		t.Fatalf("versions = %v, want client / not_synthesized / unwired / schema-v1", want)
	}
	if bundle == nil {
		t.Fatal("collector holds no synthesis input")
	}
	if len(client.encoded) != 1 || !bytes.Equal(bundle.Input, client.encoded[0]) {
		t.Fatalf("bundle input = %s, want exactly the bytes the injected encoder returned (%d encodes)", bundle.Input, len(client.encoded))
	}
	sum := sha256.Sum256(bundle.Input)
	if bundle.InputSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("input_sha256 = %q, want the sha256 of the input bytes", bundle.InputSHA256)
	}
	wantContract := contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "synthesis-output-test", PromptVersion: "synthesis-prompt-test", SystemSHA256: client.assembly.SystemSHA256}
	if bundle.Contract != wantContract || !reflect.DeepEqual(bundle.Rules, client.assembly.Rules) || bundle.Bounded {
		t.Fatalf("bundle contract = %+v rules = %v bounded = %v, want the assembly's own and unbounded", bundle.Contract, bundle.Rules, bundle.Bounded)
	}
	if &bundle.Rules[0] == &client.assembly.Rules[0] {
		t.Fatal("bundle rules alias the assembly's rules, want a copy")
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("bundle.Validate() = %v", err)
	}

	// The same question in server mode sends its model the same bytes.
	server := newClientRig(t, nil)
	if _, _, err := server.investigate(serverRequest()); err != nil {
		t.Fatalf("server Investigate() error = %v", err)
	}
	if len(server.runtime.inputs) != 1 {
		t.Fatalf("server model synthesize calls = %d, want 1", len(server.runtime.inputs))
	}
	userMessage, err := clientTestEncode(reusePrincipal().OrgID, server.runtime.inputs[0], clientTestMaxBytes)
	if err != nil {
		t.Fatalf("encode server input: %v", err)
	}
	var sent, served any
	if err := json.Unmarshal(userMessage, &sent); err != nil {
		t.Fatalf("decode server message: %v", err)
	}
	if err := json.Unmarshal(bundle.Input, &served); err != nil {
		t.Fatalf("decode bundle input: %v", err)
	}
	if !reflect.DeepEqual(sent, served) {
		t.Fatalf("bundle input differs from the message a server turn sends its model:\n bundle %s\n server %s", bundle.Input, userMessage)
	}

	saved := client.store.saved
	if saved.ResultID != result.ResultID || saved.Versions.SynthesisSource != SynthesisSourceClient {
		t.Fatalf("saved result %q source = %q, want the served row with synthesis_source client", saved.ResultID, saved.Versions.SynthesisSource)
	}
	row, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshal saved row: %v", err)
	}
	if bytes.Contains(row, []byte(bundle.InputSHA256)) || bytes.Contains(row, []byte("synthesis_input")) || bytes.Contains(row, bundle.Input) {
		t.Fatal("the saved row carries the synthesis input")
	}
}

// T2: the same question in server mode calls the model once and names the
// server as the writer.
func TestServerModeTurnCallsTheModelOnceAndNamesTheServer(t *testing.T) {
	t.Parallel()
	server := newClientRig(t, nil)
	result, bundle, err := server.investigate(serverRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if server.runtime.synthCalls != 1 {
		t.Fatalf("synthesize calls = %d, want 1", server.runtime.synthCalls)
	}
	if result.Versions.SynthesisSource != SynthesisSourceServer {
		t.Fatalf("synthesis_source = %q, want server", result.Versions.SynthesisSource)
	}
	if bundle != nil {
		t.Fatal("a server mode turn delivered a synthesis input")
	}
	if len(server.telemetry.clientSynthesisDecisions) != 0 {
		t.Fatalf("server mode turn wrote client synthesis decisions: %+v", server.telemetry.clientSynthesisDecisions)
	}
}

// The degraded answer of a failed model call names the server too.
func TestDegradedModelFailureAnswerNamesTheServerAsWriter(t *testing.T) {
	t.Parallel()
	server := newClientRig(t, func(r *clientRig) { r.runtime.synthErr = ErrModelOutput })
	result, _, err := server.investigate(serverRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationDegraded || result.Versions.SynthesisSource != SynthesisSourceServer {
		t.Fatalf("Status = %q synthesis_source = %q, want the degraded answer from the server", result.Status, result.Versions.SynthesisSource)
	}
}

// T3: nothing read is no_match in client mode, never complete.
func TestClientSynthesisTurnWithNothingReadIsNoMatch(t *testing.T) {
	t.Parallel()
	client := newClientRig(t, func(r *clientRig) { r.facts = []CanonicalFact{} })
	result, bundle, err := client.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match", result.Status)
	}
	if result.DeterministicAnswer == contractsv1.ContextFabricClientSynthesisAnswer {
		t.Fatal("a no_match turn carries the client answer sentence, want the composed no-match text")
	}
	if bundle == nil || result.Versions.SynthesisSource != SynthesisSourceClient || client.runtime.synthCalls != 0 {
		t.Fatalf("bundle nil = %v source = %q synth calls = %d, want the bundle, client source and no model call", bundle == nil, result.Versions.SynthesisSource, client.runtime.synthCalls)
	}
}

// T4: the entry gate fails closed, one clause at a time, before any work.
func TestClientSynthesisEntryGateFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mutate      func(*clientRig)
		noCollector bool
	}{
		{name: "synthesizer is not a composer", mutate: func(r *clientRig) {
			r.synthesizer = synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				r.t.Error("the synthesizer was called")
				return InvestigationResult{}, nil
			})
		}},
		{name: "no assembly", mutate: func(r *clientRig) { r.assembly = nil }},
		{name: "no encoder", mutate: func(r *clientRig) { r.assembly.Encode = nil }},
		{name: "no collector in context", noCollector: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newClientRig(t, tc.mutate)
			ctx := context.Background()
			if !tc.noCollector {
				ctx, _ = WithSynthesisInputCollector(ctx)
			}
			_, err := rig.engine.Investigate(ctx, reusePrincipal(), clientRequest())
			if !errors.Is(err, ErrClientSynthesisUnavailable) {
				t.Fatalf("Investigate() error = %v, want ErrClientSynthesisUnavailable", err)
			}
			if rig.runtime.interpretCalls != 0 || rig.factReads != 0 || rig.store.saved.ResultID != "" || rig.lookups != 0 {
				t.Fatalf("work before the gate: interpret = %d fact reads = %d saved = %q lookups = %d", rig.runtime.interpretCalls, rig.factReads, rig.store.saved.ResultID, rig.lookups)
			}
			want := []ClientSynthesisDecisionEvent{{Outcome: ClientSynthesisUnavailable}}
			if !reflect.DeepEqual(rig.telemetry.clientSynthesisDecisions, want) {
				t.Fatalf("decision lines = %+v, want %+v", rig.telemetry.clientSynthesisDecisions, want)
			}
		})
	}
}

// A composer that returns no input is refused: a turn must not be served as
// client written with nothing to write from.
func TestClientSynthesisComposerThatReturnsNoInputIsRefused(t *testing.T) {
	t.Parallel()
	rig := newClientRig(t, func(r *clientRig) { r.synthesizer = bundlelessComposer{} })
	_, bundle, err := rig.investigate(clientRequest())
	if !errors.Is(err, ErrClientSynthesisUnavailable) || bundle != nil {
		t.Fatalf("Investigate() error = %v bundle nil = %v, want ErrClientSynthesisUnavailable and no bundle", err, bundle == nil)
	}
	if rig.store.saved.ResultID != "" {
		t.Fatal("a result without its synthesis input was saved")
	}
}

type bundlelessComposer struct{}

func (bundlelessComposer) Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
	return InvestigationResult{}, errors.New("not used")
}

func (bundlelessComposer) ComposeForClientSynthesis(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error) {
	return validInvestigationResult(), nil, nil
}

// T5: a client mode turn never reads and never writes answer reuse.
func TestClientSynthesisTurnNeverReadsOrWritesAnswerReuse(t *testing.T) {
	t.Parallel()
	_, candidate := reusableCandidate()

	control := newClientRig(t, func(r *clientRig) { r.reuse = &candidate; r.runtime.interpreted = suppliedInterpretation() })
	served, _, err := control.investigate(serverRequestNoWindow())
	if err != nil {
		t.Fatalf("control Investigate() error = %v", err)
	}
	if !served.Reused || control.lookups != 1 || control.runtime.synthCalls != 0 {
		t.Fatalf("control: Reused = %v lookups = %d synth calls = %d, want the stored answer served from one lookup", served.Reused, control.lookups, control.runtime.synthCalls)
	}

	client := newClientRig(t, func(r *clientRig) { r.reuse = &candidate; r.runtime.interpreted = suppliedInterpretation() })
	result, bundle, err := client.investigate(clientRequestNoWindow())
	if err != nil {
		t.Fatalf("client Investigate() error = %v", err)
	}
	if result.Reused || bundle == nil || client.lookups != 0 {
		t.Fatalf("client: Reused = %v bundle nil = %v lookups = %d, want a fresh turn with no lookup", result.Reused, bundle == nil, client.lookups)
	}
	if want := []AnswerReuseBypassReason{AnswerReuseBypassClientSynthesis}; !reflect.DeepEqual(client.telemetry.answerReuseBypasses, want) {
		t.Fatalf("bypasses = %v, want %v", client.telemetry.answerReuseBypasses, want)
	}
	if client.store.saved.ResultID != result.ResultID || client.store.savedSnapshot != nil || client.store.savedEpoch != nil {
		t.Fatalf("Save: result %q snapshot = %v epoch = %v, want the row with nil reuse columns", client.store.saved.ResultID, client.store.savedSnapshot, client.store.savedEpoch)
	}

	serverFresh := newClientRig(t, func(r *clientRig) { r.runtime.interpreted = suppliedInterpretation() })
	if _, _, err := serverFresh.investigate(serverRequestNoWindow()); err != nil {
		t.Fatalf("server Investigate() error = %v", err)
	}
	if serverFresh.store.savedSnapshot == nil || serverFresh.store.savedEpoch == nil {
		t.Fatalf("server control Save snapshot = %v epoch = %v, want reusable columns so the client row's nil columns are the guard", serverFresh.store.savedSnapshot, serverFresh.store.savedEpoch)
	}
}

func TestReuseBypassReasonNamesClientSynthesisAfterASuppliedInterpretation(t *testing.T) {
	t.Parallel()
	request := suppliedInvestigationRequest(clientRequest())
	if got := reuseBypassReason(request, requestStructureCanonicalization{}); got != AnswerReuseBypassSuppliedInterpretation {
		t.Fatalf("bypass = %q, want the supplied interpretation first", got)
	}
	request.SuppliedInterpretation = nil
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectProject, PatternID: "p", Value: "v"}}
	if got := reuseBypassReason(request, requestStructureCanonicalization{}); got != AnswerReuseBypassClientSynthesis {
		t.Fatalf("bypass = %q, want %q ahead of the later arms", got, AnswerReuseBypassClientSynthesis)
	}
}

// T6: a small bound reduces the facts and the bundle fits it; no bound that
// fits ends the turn with the model path's own error.
func TestClientSynthesisInputIsBoundedToTheAssemblyBound(t *testing.T) {
	t.Parallel()
	const bound = 6000
	rig := newClientRig(t, func(r *clientRig) {
		r.facts = clientManyFacts(r.project, 40)
		r.assembly.MaxBytes = bound
	})
	result, bundle, err := rig.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if bundle == nil || !bundle.Bounded || len(bundle.Input) > bound {
		t.Fatalf("bundle nil = %v bounded = %v bytes = %d, want a bounded input within %d", bundle == nil, bundle != nil && bundle.Bounded, len(bundle.Input), bound)
	}
	if !hasLimitation(result.Limitations, contractsv1.ContextFabricSynthesisInputBoundedLimitation) || !result.Coverage.Partial {
		t.Fatalf("limitations = %v partial = %v, want the bounded limitation and coverage.partial", result.Limitations, result.Coverage.Partial)
	}
	if len(rig.telemetry.synthesisInputBounds) != 1 || rig.telemetry.synthesisInputBounds[0].Outcome != SynthesisInputBoundFitted {
		t.Fatalf("input bound events = %+v, want one fitted", rig.telemetry.synthesisInputBounds)
	}
	var decoded struct {
		Facts []json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(bundle.Input, &decoded); err != nil || len(decoded.Facts) == 0 || len(decoded.Facts) >= 42 {
		t.Fatalf("facts in bundle = %d (%v), want some but fewer than the 42 read", len(decoded.Facts), err)
	}
	served := rig.telemetry.clientSynthesisDecisions
	if len(served) != 1 || served[0].Outcome != ClientSynthesisServed || !served[0].Bounded || served[0].FactsRead != 42 || served[0].FactsGiven != len(decoded.Facts) || served[0].MaxBytes != bound || served[0].BundleBytes != len(bundle.Input) {
		t.Fatalf("decision lines = %+v, want one served, bounded, 42 read, %d given", served, len(decoded.Facts))
	}
}

func TestClientSynthesisInputThatCannotBeBoundedEndsTheTurnLikeTheModelPath(t *testing.T) {
	t.Parallel()
	client := newClientRig(t, func(r *clientRig) { r.assembly.MaxBytes = 40 })
	_, bundle, err := client.investigate(clientRequest())
	if !errors.Is(err, ErrModelInputTooLarge) || !errors.Is(err, ErrSynthesisAborted) || bundle != nil {
		t.Fatalf("client Investigate() error = %v bundle nil = %v, want ErrModelInputTooLarge inside ErrSynthesisAborted and no bundle", err, bundle == nil)
	}
	if client.store.saved.ResultID != "" {
		t.Fatal("a turn that could not build its input was saved")
	}
	if len(client.telemetry.synthesisInputBounds) != 1 || client.telemetry.synthesisInputBounds[0].Outcome != SynthesisInputBoundExhausted || client.telemetry.synthesisInputBounds[0].Passes >= maxSynthesisInputBoundPasses {
		t.Fatalf("input bound events = %+v, want one exhausted", client.telemetry.synthesisInputBounds)
	}
	if len(client.telemetry.clientSynthesisDecisions) != 1 || client.telemetry.clientSynthesisDecisions[0].Outcome != ClientSynthesisInputTooLarge || client.telemetry.clientSynthesisDecisions[0].FactsRead != 2 {
		t.Fatalf("decision lines = %+v, want one input_too_large", client.telemetry.clientSynthesisDecisions)
	}

	// The model path ends with the same sentinels for an input that cannot fit.
	server := newClientRig(t, func(r *clientRig) { r.runtime.synthErr = &ModelInputOverflow{Bytes: 100, MaxBytes: 10} })
	if _, _, err := server.investigate(serverRequest()); !errors.Is(err, ErrModelInputTooLarge) || !errors.Is(err, ErrSynthesisAborted) {
		t.Fatalf("server Investigate() error = %v, want ErrModelInputTooLarge inside ErrSynthesisAborted", err)
	}
}

// T7: commit affirmation is unchanged. Nothing a model wrote can support a
// commit, so only an identity proven commit stays; the client form says so.
func TestClientSynthesisRetractsACommitNothingProvesAndSaysSo(t *testing.T) {
	t.Parallel()
	retracted := newClientRig(t, func(r *clientRig) { r.graph.bases = nil })
	result, _, err := retracted.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed = %v, want the unproven commit retracted", result.SubjectResolution.Committed)
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		if candidate.State == ResolutionCommitted {
			t.Fatalf("candidate %v is still committed", candidate)
		}
	}
	if !hasLimitation(result.Limitations, commitRetractionLimitation) || !hasLimitation(result.Limitations, contractsv1.ContextFabricClientSynthesisCommitNotAffirmedLimitation) || !result.Coverage.Partial {
		t.Fatalf("limitations = %v partial = %v, want both retraction limitations and coverage.partial", result.Limitations, result.Coverage.Partial)
	}
	if got := retracted.telemetry.clientSynthesisDecisions; len(got) != 1 || got[0].CommitsRetracted != 1 {
		t.Fatalf("decision lines = %+v, want one with commits_retracted 1", got)
	}

	proven := newClientRig(t, nil)
	result, _, err = proven.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.SubjectResolution.Committed) != 1 || hasLimitation(result.Limitations, contractsv1.ContextFabricClientSynthesisCommitNotAffirmedLimitation) || hasLimitation(result.Limitations, commitRetractionLimitation) {
		t.Fatalf("Committed = %v limitations = %v, want the proven commit kept and neither limitation", result.SubjectResolution.Committed, result.Limitations)
	}
	if got := proven.telemetry.clientSynthesisDecisions; len(got) != 1 || got[0].CommitsRetracted != 0 {
		t.Fatalf("decision lines = %+v, want one with commits_retracted 0", got)
	}
}

// The model failure path has no draft either and retracts the same commit
// with the existing limitation only.
func TestModelFailureAnswerRetractsAnUnprovenCommitWithoutTheClientLimitation(t *testing.T) {
	t.Parallel()
	rig := newClientRig(t, func(r *clientRig) {
		r.graph.bases = nil
		r.runtime.synthErr = ErrModelOutput
	})
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationDegraded || len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Status = %q Committed = %v, want the degraded answer with the commit retracted", result.Status, result.SubjectResolution.Committed)
	}
	if !hasLimitation(result.Limitations, commitRetractionLimitation) || hasLimitation(result.Limitations, contractsv1.ContextFabricClientSynthesisCommitNotAffirmedLimitation) {
		t.Fatalf("limitations = %v, want the retraction limitation without the client one", result.Limitations)
	}
}

// T8: the stored row rule.
func TestBackfillStoredSynthesisSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		source      SynthesisSource
		version     string
		wantSource  SynthesisSource
		wantChanged bool
	}{
		{name: "source already set", source: SynthesisSourceClient, version: "context-fabric-synthesis.v17+model-x", wantSource: SynthesisSourceClient},
		{name: "empty version", wantSource: ""},
		{name: "unwired", version: "unwired", wantSource: ""},
		{name: "not synthesized", version: SynthesisVersionNotSynthesized, wantSource: ""},
		{name: "real version", version: "context-fabric-synthesis.v17+model-x", wantSource: SynthesisSourceServer, wantChanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			versions := VersionSet{SynthesisSource: tc.source, SynthesisVersion: tc.version}
			if changed := BackfillStoredSynthesisSource(&versions); changed != tc.wantChanged || versions.SynthesisSource != tc.wantSource {
				t.Fatalf("changed = %v source = %q, want %v and %q", changed, versions.SynthesisSource, tc.wantChanged, tc.wantSource)
			}
		})
	}
}

func TestReusedLegacyRowIsServedWithTheServerAsWriter(t *testing.T) {
	t.Parallel()
	_, candidate := reusableCandidate()
	candidate.Versions.SynthesisVersion = "context-fabric-synthesis.v17+model-x"
	candidate.Versions.SynthesisSource = ""
	rig := newClientRig(t, func(r *clientRig) { r.reuse = &candidate })
	result, _, err := rig.investigate(serverRequestNoWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused || result.Versions.SynthesisSource != SynthesisSourceServer {
		t.Fatalf("Reused = %v synthesis_source = %q, want the reused row served with source server", result.Reused, result.Versions.SynthesisSource)
	}
}

// T9: a terminal turn delivers no input and names no writer.
func TestClientSynthesisTurnThatEndsBeforeSynthesisDeliversNothing(t *testing.T) {
	t.Parallel()
	rig := newClientRig(t, func(r *clientRig) {
		r.graph.resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	})
	result, bundle, err := rig.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if rig.factReads != 0 || rig.runtime.synthCalls != 0 {
		t.Fatalf("fact reads = %d synth calls = %d, want a turn that ended before the facts", rig.factReads, rig.runtime.synthCalls)
	}
	if bundle != nil || result.Versions.SynthesisSource != "" || len(rig.telemetry.clientSynthesisDecisions) != 0 {
		t.Fatalf("bundle nil = %v synthesis_source = %q decisions = %v, want none of them on a terminal turn", bundle == nil, result.Versions.SynthesisSource, rig.telemetry.clientSynthesisDecisions)
	}
}

// T10: one decision line per served client turn, with the fields filled.
func TestClientSynthesisDecisionLineIsWrittenOncePerServedTurn(t *testing.T) {
	t.Parallel()
	rig := newClientRig(t, nil)
	result, bundle, err := rig.investigate(clientRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	want := []ClientSynthesisDecisionEvent{{
		Outcome: ClientSynthesisServed, Status: result.Status, BundleBytes: len(bundle.Input), MaxBytes: clientTestMaxBytes,
		FactsRead: 2, FactsGiven: 2,
	}}
	if !reflect.DeepEqual(rig.telemetry.clientSynthesisDecisions, want) {
		t.Fatalf("decision lines = %+v, want %+v", rig.telemetry.clientSynthesisDecisions, want)
	}
}

// The bundle is delivered only once the row is saved.
func TestClientSynthesisInputIsNotDeliveredWhenTheSaveFails(t *testing.T) {
	t.Parallel()
	rig := newClientRig(t, nil)
	rig.engine.results = clientSaveFailsStore{rig.store}
	_, bundle, err := rig.investigate(clientRequest())
	if err == nil || bundle != nil {
		t.Fatalf("Investigate() error = %v bundle nil = %v, want the save error and no bundle", err, bundle == nil)
	}
	if len(rig.telemetry.clientSynthesisDecisions) != 0 {
		t.Fatalf("decision lines = %+v, want none for a turn that was not served", rig.telemetry.clientSynthesisDecisions)
	}
}

type clientSaveFailsStore struct{ *resultStoreStub }

func (clientSaveFailsStore) Save(context.Context, storage.Principal, InvestigationResult, SourceWatermarkSnapshot, RebuildEpoch, string, ReuseRetrievalIdentity, ReusePromptVersions, ReuseVersionAuthorities, int64, string, SemanticStateWrite) error {
	return errors.New("save failed")
}

// retryClientComposer serves the budget stage fixture through the client
// form: each call hands back an input that names its pass.
type retryClientComposer struct {
	inner      AnswerSynthesizer
	calls      int
	failSecond bool
}

func (c *retryClientComposer) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	return c.inner.Synthesize(ctx, principal, input)
}

func (c *retryClientComposer) ComposeForClientSynthesis(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error) {
	c.calls++
	if c.failSecond && c.calls == 2 {
		return InvestigationResult{}, nil, errors.New("second pass failed")
	}
	result, err := c.inner.Synthesize(ctx, principal, input)
	if err != nil {
		return InvestigationResult{}, nil, err
	}
	result.Versions.SynthesisSource = SynthesisSourceClient
	body := []byte(fmt.Sprintf(`{"pass":%d}`, c.calls))
	sum := sha256.Sum256(body)
	return result, &contractsv1.ContextFabricSynthesisInput{
		Contract: contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "v1", PromptVersion: "v1", SystemSHA256: hex.EncodeToString(sha256Sum("system"))},
		Input:    body, InputSHA256: hex.EncodeToString(sum[:]), Rules: []string{"rule"},
	}, nil
}

// The input of the pass that is served is the one delivered: a retry pass
// replaces the first pass's input, and a retry that fails delivers none.
func TestClientSynthesisDeliversTheInputOfTheServedPassOnly(t *testing.T) {
	t.Parallel()
	for _, failSecond := range []bool{false, true} {
		t.Run(fmt.Sprintf("second pass fails %v", failSecond), func(t *testing.T) {
			t.Parallel()
			calls := 0
			engine := budgetStageEngine(t, budgetStageCohort(6), 3, budgetStageOptions(12, time.Second), &calls, &recordingTelemetry{})
			composer := &retryClientComposer{inner: engine.synthesizer, failSecond: failSecond}
			engine.synthesizer = composer
			request := validInvestigationRequestWithConfirmedWindow()
			request.SynthesisMode = SynthesisModeClient
			ctx, collected := WithSynthesisInputCollector(context.Background())

			_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)

			if composer.calls != 2 {
				t.Fatalf("composer calls = %d, want the first pass and one retry", composer.calls)
			}
			if failSecond {
				if err == nil || collected() != nil {
					t.Fatalf("Investigate() error = %v delivered nil = %v, want the retry's error and no input", err, collected() == nil)
				}
				return
			}
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if bundle := collected(); bundle == nil || string(bundle.Input) != `{"pass":2}` {
				t.Fatalf("delivered input = %v, want the retry pass's", bundle)
			}
		})
	}
}

// A synthesizer wired without the means to encode refuses before any work,
// and the composer itself refuses too when called directly.
func TestRuntimeAnswerSynthesizerRefusesClientSynthesisWithoutAnAssembly(t *testing.T) {
	t.Parallel()
	for name, synthesizer := range map[string]RuntimeAnswerSynthesizer{
		"no assembly": {},
		"no encoder":  {ClientSynthesis: &ClientSynthesisAssembly{MaxBytes: 10}},
	} {
		if _, bundle, err := synthesizer.ComposeForClientSynthesis(context.Background(), reusePrincipal(), validSynthesisInputFixture()); !errors.Is(err, ErrClientSynthesisUnavailable) || bundle != nil {
			t.Fatalf("%s: ComposeForClientSynthesis() error = %v bundle nil = %v, want ErrClientSynthesisUnavailable", name, err, bundle == nil)
		}
	}
}

// clientComposingSynthesizer lets a fixture synthesizer take a client turn:
// it serves the fixture's result with a fixed valid input.
type clientComposingSynthesizer struct{ AnswerSynthesizer }

func (c clientComposingSynthesizer) ComposeForClientSynthesis(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error) {
	result, err := c.Synthesize(ctx, principal, input)
	body := []byte(`{"fixture":true}`)
	sum := sha256.Sum256(body)
	return result, &contractsv1.ContextFabricSynthesisInput{
		Contract: contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "v1", PromptVersion: "v1", SystemSHA256: hex.EncodeToString(sha256Sum("system"))},
		Input:    body, InputSHA256: hex.EncodeToString(sum[:]), Rules: []string{"rule"},
	}, err
}

// A path read with no fact still makes the turn partial: something was read.
func TestClientSynthesisTurnWithOnlyAPathReadIsPartial(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	input.Facts.Facts = []CanonicalFact{}
	if len(input.Graph.Paths) == 0 {
		t.Fatal("fixture defect: the input carries no path")
	}
	synthesizer := RuntimeAnswerSynthesizer{ClientSynthesis: &ClientSynthesisAssembly{
		PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")), Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
	}}
	result, _, err := synthesizer.ComposeForClientSynthesis(context.Background(), reusePrincipal(), input)
	if err != nil {
		t.Fatalf("ComposeForClientSynthesis() error = %v", err)
	}
	if result.Status != InvestigationPartial || result.DeterministicAnswer != contractsv1.ContextFabricClientSynthesisAnswer {
		t.Fatalf("Status = %q answer = %q, want partial with the client answer", result.Status, result.DeterministicAnswer)
	}
}

// The interpretation version is the interpreter's, or the unwired placeholder
// when no interpretation ran.
func TestClientSynthesisResultNamesTheInterpretationVersionOrUnwired(t *testing.T) {
	t.Parallel()
	synthesizer := RuntimeAnswerSynthesizer{ClientSynthesis: &ClientSynthesisAssembly{
		PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")), Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
	}}
	input := validSynthesisInputFixture()
	bare, _, err := synthesizer.ComposeForClientSynthesis(context.Background(), reusePrincipal(), input)
	if err != nil {
		t.Fatalf("ComposeForClientSynthesis() error = %v", err)
	}
	if bare.Versions.InterpretationVersion != "unwired" {
		t.Fatalf("interpretation_version = %q with no stamp, want unwired", bare.Versions.InterpretationVersion)
	}
	for name, version := range map[string]string{"a stamp with a version": "schema-v9", "a stamp with no version": ""} {
		ctx := withInterpretationStamp(context.Background(), InterpretationStamp{Ran: true, InterpretationVersion: version})
		stamped, _, err := synthesizer.ComposeForClientSynthesis(ctx, reusePrincipal(), input)
		if err != nil {
			t.Fatalf("%s: ComposeForClientSynthesis() error = %v", name, err)
		}
		want := version
		if want == "" {
			want = "unwired"
		}
		if stamped.Versions.InterpretationVersion != want {
			t.Fatalf("%s: interpretation_version = %q, want %q", name, stamped.Versions.InterpretationVersion, want)
		}
	}
}

// An input that does not pass its own contract is never delivered.
func TestClientSynthesisInputThatFailsItsContractIsNeverServed(t *testing.T) {
	t.Parallel()
	rig := newClientRig(t, func(r *clientRig) { r.assembly.SystemSHA256 = "not-a-digest" })
	_, bundle, err := rig.investigate(clientRequest())
	if err == nil || bundle != nil {
		t.Fatalf("Investigate() error = %v bundle nil = %v, want an error and no bundle", err, bundle == nil)
	}
	if rig.store.saved.ResultID != "" {
		t.Fatal("a result without a valid synthesis input was saved")
	}
}
