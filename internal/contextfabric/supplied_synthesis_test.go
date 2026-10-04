package contextfabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const writeBackMarker = "zx-marker-7f3a91-do-not-log"

// suppliedInterpretationStub is the supplied interpretation runtime of the
// write-back tests: it counts and makes no model call.
type suppliedInterpretationStub struct {
	mu        sync.Mutex
	checks    int
	interpret int
	question  InterpretedQuestion
}

func (s *suppliedInterpretationStub) CheckSuppliedContract(context.Context, storage.Principal, InvestigationRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	return nil
}

func (s *suppliedInterpretationStub) InterpretSuppliedQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interpret++
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.Provider = contractsv1.ContextFabricClientSuppliedProvider
	return s.question, receipt, nil
}

// draftParserStub is the ParseDraft of the write-back tests. It counts, keeps
// the raw output it was given and hands back the configured draft or error.
type draftParserStub struct {
	mu    sync.Mutex
	calls int
	raw   [][]byte
	draft SynthesisDraft
	err   error
}

func (p *draftParserStub) Parse(raw []byte) (SynthesisDraft, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.raw = append(p.raw, append([]byte(nil), raw...))
	return p.draft, p.err
}

type writeBackRig struct {
	*clientRig
	parse    *draftParserStub
	supplied *suppliedInterpretationStub
	sink     *fakeReceiptSink
	// clientDecisionsAfterCall1 is how many no-draft decision lines call 1 wrote.
	clientDecisionsAfterCall1 int
}

func newWriteBackRig(t *testing.T, mutate func(*clientRig)) *writeBackRig {
	t.Helper()
	wb := &writeBackRig{
		parse: &draftParserStub{}, sink: &fakeReceiptSink{},
		supplied: &suppliedInterpretationStub{question: bootstrapInterpretation()},
	}
	wb.clientRig = newClientRig(t, func(r *clientRig) {
		r.supplied = wb.supplied
		r.sink = wb.sink
		r.assembly.ParseDraft = wb.parse.Parse
		wb.parse.draft = bootstrapDraft(r.project)
		if mutate != nil {
			mutate(r)
		}
	})
	return wb
}

func writeBackSuppliedInterpretation() *SuppliedInterpretation {
	return &SuppliedInterpretation{
		Output: json.RawMessage(`{}`), ModelOutputVersion: "interpretation-output-test", PromptVersion: "interpretation-prompt-test",
		SystemSHA256: hex.EncodeToString(sha256Sum("interpretation-system")),
	}
}

// firstCall is call 1 of the flow: client mode, no draft. It returns the input
// the caller's model writes from.
func (w *writeBackRig) firstCall() (InvestigationResult, *contractsv1.ContextFabricSynthesisInput) {
	w.t.Helper()
	return w.firstCallWith(nil)
}

func (w *writeBackRig) firstCallWith(edit func(*InvestigationRequest)) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput) {
	w.t.Helper()
	request := clientRequest()
	request.SuppliedInterpretation = writeBackSuppliedInterpretation()
	if edit != nil {
		edit(&request)
	}
	result, bundle, err := w.investigate(request)
	if err != nil || bundle == nil {
		w.t.Fatalf("call 1: error = %v bundle nil = %v", err, bundle == nil)
	}
	w.clientDecisionsAfterCall1 = len(w.telemetry.clientSynthesisDecisions)
	return result, bundle
}

// writeBackRequest is call 2: the same question with the draft written from bundle.
func writeBackRequest(bundle *contractsv1.ContextFabricSynthesisInput, edit func(*InvestigationRequest)) InvestigationRequest {
	request := clientRequest()
	request.SuppliedInterpretation = writeBackSuppliedInterpretation()
	request.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
		Output:             json.RawMessage(`{"marker":"` + writeBackMarker + `"}`),
		ModelOutputVersion: bundle.Contract.ModelOutputVersion, PromptVersion: bundle.Contract.PromptVersion,
		SystemSHA256: bundle.Contract.SystemSHA256, InputSHA256: bundle.InputSHA256, ClientModel: "writer-model-1",
	}
	if edit != nil {
		edit(&request)
	}
	return request
}

// untouched asserts a refused write-back did no work: no interpret call, no
// fact read, no reuse lookup, no save, no parse and no receipt.
func (w *writeBackRig) untouched(t *testing.T, label string, factReadsBefore int) {
	t.Helper()
	if w.supplied.interpret != 0 || w.runtime.interpretCalls != 0 || w.runtime.synthCalls != 0 {
		t.Errorf("%s: interpret calls = %d/%d synthesize = %d, want none", label, w.supplied.interpret, w.runtime.interpretCalls, w.runtime.synthCalls)
	}
	if w.factReads != factReadsBefore || w.lookups != 0 {
		t.Errorf("%s: fact reads = %d (before %d) reuse lookups = %d, want no read", label, w.factReads, factReadsBefore, w.lookups)
	}
	if w.store.saved.ResultID != "" {
		t.Errorf("%s: a result was saved", label)
	}
	if w.parse.calls != 0 || len(w.sink.recorded) != 0 {
		t.Errorf("%s: parse calls = %d receipts = %d, want none", label, w.parse.calls, len(w.sink.recorded))
	}
}

func (w *writeBackRig) onlyDecision(t *testing.T, label string) SuppliedSynthesisDecisionEvent {
	t.Helper()
	got := w.telemetry.suppliedSynthesisDecisions
	if len(got) != 1 {
		t.Fatalf("%s: supplied synthesis decisions = %+v, want exactly one", label, got)
	}
	if len(w.telemetry.clientSynthesisDecisions) != w.clientDecisionsAfterCall1 {
		t.Fatalf("%s: client synthesis decisions = %+v, want none beyond call 1's on a turn that carries a draft", label, w.telemetry.clientSynthesisDecisions)
	}
	return got[0]
}

// E1: a served write-back makes no model call and serves the draft's answer.
func TestSuppliedSynthesisIsServedFromTheDraftWithoutAModelCall(t *testing.T) {
	t.Parallel()
	for name, model := range map[string]string{"a declared model": "writer-model-1", "no declared model": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wb := newWriteBackRig(t, nil)
			first, bundle := wb.firstCall()
			wb.supplied.interpret = 0
			request := writeBackRequest(bundle, func(r *InvestigationRequest) { r.SuppliedSynthesis.ClientModel = model })
			result, delivered, err := wb.investigate(request)
			if err != nil {
				t.Fatalf("call 2: Investigate() error = %v", err)
			}
			if wb.supplied.interpret != 1 || wb.runtime.interpretCalls != 0 || wb.runtime.synthCalls != 0 {
				t.Fatalf("model calls: supplied interpret = %d model interpret = %d model synthesize = %d, want 1 supplied and no model call", wb.supplied.interpret, wb.runtime.interpretCalls, wb.runtime.synthCalls)
			}
			if delivered != nil {
				t.Fatal("a served write-back delivered a synthesis input")
			}
			if wb.parse.calls != 1 || !bytes.Equal(wb.parse.raw[0], request.SuppliedSynthesis.Output) {
				t.Fatalf("parse calls = %d, want one with the supplied output", wb.parse.calls)
			}

			server := newClientRig(t, func(r *clientRig) { r.runtime.draft = bootstrapDraft(r.project) })
			want, _, err := server.investigate(serverRequest())
			if err != nil {
				t.Fatalf("server Investigate() error = %v", err)
			}
			if result.Status != InvestigationComplete || result.Status != want.Status {
				t.Fatalf("status = %q, want complete as the draft says", result.Status)
			}
			if !reflect.DeepEqual(result.Drivers, want.Drivers) || !reflect.DeepEqual(result.ClaimedFacts, want.ClaimedFacts) {
				t.Fatalf("drivers/claimed facts differ from the model path's for the same draft:\n got %+v %+v\nwant %+v %+v", result.Drivers, result.ClaimedFacts, want.Drivers, want.ClaimedFacts)
			}
			if len(result.Drivers) != 1 || result.Drivers[0].DriverID != "driver_bootstrap01" {
				t.Fatalf("drivers = %+v, want the draft's driver", result.Drivers)
			}
			for field, pair := range map[string][2]string{
				"direct_judgment": {result.DirectJudgment, want.DirectJudgment}, "current_state": {result.CurrentState, want.CurrentState},
				"deterministic_answer": {result.DeterministicAnswer, want.DeterministicAnswer},
			} {
				if pair[0] != pair[1] || pair[0] == contractsv1.ContextFabricClientSynthesisAnswer || pair[0] == "" {
					t.Errorf("%s = %q, want the text composed from the draft (%q), never the fixed client sentence", field, pair[0], pair[1])
				}
			}

			wantModel := "client-supplied/undeclared"
			if model != "" {
				wantModel = "client-supplied/" + model
			}
			versions := result.Versions
			if versions.SynthesisSource != SynthesisSourceClient || versions.ModelIdentity != wantModel || versions.SynthesisVersion != "synthesis-prompt-test" {
				t.Fatalf("versions = source %q identity %q synthesis %q, want client / %q / the prompt version alone", versions.SynthesisSource, versions.ModelIdentity, versions.SynthesisVersion, wantModel)
			}
			if versions.InterpretationVersion != first.Versions.InterpretationVersion || versions.InterpretationVersion == "unwired" || versions.InterpretationVersion == "" {
				t.Fatalf("interpretation_version = %q, want the stamp's (call 1 read %q)", versions.InterpretationVersion, first.Versions.InterpretationVersion)
			}

			saved, err := json.Marshal(wb.store.saved)
			if err != nil {
				t.Fatalf("marshal saved: %v", err)
			}
			served, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal served: %v", err)
			}
			if !bytes.Equal(saved, served) {
				t.Fatalf("the saved row differs from the served result:\n saved  %s\n served %s", saved, served)
			}

			if len(wb.sink.recorded) != 1 {
				t.Fatalf("receipts = %d, want one", len(wb.sink.recorded))
			}
			receipt := wb.sink.recorded[0]
			if err := receipt.Validate(); err != nil {
				t.Fatalf("receipt.Validate() = %v", err)
			}
			sum := sha256.Sum256(wb.encoded[len(wb.encoded)-1])
			if receipt.Operation != ModelOperationSynthesize || receipt.Provider != contractsv1.ContextFabricClientSuppliedProvider || receipt.Outcome != "success" || receipt.Attempts != 1 ||
				receipt.Model != strings.TrimPrefix(wantModel, "client-supplied/") || receipt.PromptVersion != "synthesis-prompt-test" || receipt.SchemaVersion != "synthesis-output-test" ||
				receipt.InputDigest != DigestModelValue(wb.encoded[len(wb.encoded)-1]) || receipt.OutputDigest != DigestModelValue(request.SuppliedSynthesis.Output) {
				t.Fatalf("receipt = %+v (input sha %x), want the client-supplied synthesize receipt of this exchange", receipt, sum)
			}
			if decision := wb.onlyDecision(t, "served"); decision.Outcome != SuppliedSynthesisServed || decision.ClientModel != declaredSynthesisModel(request.SuppliedSynthesis) || decision.OutputBytes != len(request.SuppliedSynthesis.Output) {
				t.Fatalf("decision = %+v, want served with the declared model and the output size", decision)
			}
		})
	}
}

// E2: every clause of the entry gate refuses before any work.
func TestSuppliedSynthesisEntryGateRefusesBeforeAnyWork(t *testing.T) {
	t.Parallel()
	system := hex.EncodeToString(sha256Sum("system"))
	wrongSystem := hex.EncodeToString(sha256Sum("other system"))
	current := contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "synthesis-output-test", PromptVersion: "synthesis-prompt-test", SystemSHA256: system}
	mismatchCases := map[string]struct {
		edit func(*contractsv1.ContextFabricSuppliedSynthesis)
		want []string
	}{
		"wrong prompt version":       {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.PromptVersion = "old-prompt" }, []string{"prompt_version"}},
		"wrong model output version": {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "old-output" }, []string{"model_output_version"}},
		"wrong system digest":        {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.SystemSHA256 = wrongSystem }, []string{"system_sha256"}},
		"missing prompt version":     {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.PromptVersion = "" }, []string{"prompt_version"}},
		"missing output version":     {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "" }, []string{"model_output_version"}},
		"missing system digest":      {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.SystemSHA256 = "" }, []string{"system_sha256"}},
		"missing input digest":       {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.InputSHA256 = "" }, []string{"input_sha256"}},
		"several at once": {func(s *contractsv1.ContextFabricSuppliedSynthesis) {
			s.PromptVersion, s.ModelOutputVersion, s.SystemSHA256, s.InputSHA256 = "old-prompt", "", wrongSystem, ""
		}, []string{"model_output_version", "prompt_version", "system_sha256", "input_sha256"}},
	}
	for name, tc := range mismatchCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wb := newWriteBackRig(t, nil)
			_, bundle := wb.firstCall()
			reads := wb.factReads
			wb.supplied.interpret, wb.runtime.interpretCalls, wb.store.saved = 0, 0, InvestigationResult{}
			_, _, err := wb.investigate(writeBackRequest(bundle, func(r *InvestigationRequest) { tc.edit(r.SuppliedSynthesis) }))
			var mismatch *SynthesisContractMismatch
			if !errors.As(err, &mismatch) {
				t.Fatalf("error = %v, want *SynthesisContractMismatch", err)
			}
			if !reflect.DeepEqual(mismatch.Mismatch, tc.want) || mismatch.Current != current {
				t.Fatalf("mismatch = %v current = %+v, want %v and the assembly's own %+v", mismatch.Mismatch, mismatch.Current, tc.want, current)
			}
			wb.untouched(t, name, reads)
			if decision := wb.onlyDecision(t, name); decision.Outcome != SuppliedSynthesisContractMismatch || !reflect.DeepEqual(decision.Mismatch, tc.want) {
				t.Fatalf("decision = %+v, want contract_mismatch naming %v", decision, tc.want)
			}
		})
	}

	t.Run("no ParseDraft", func(t *testing.T) {
		t.Parallel()
		wb := newWriteBackRig(t, func(r *clientRig) { r.assembly.ParseDraft = nil })
		_, bundle := wb.firstCall()
		reads := wb.factReads
		wb.supplied.interpret, wb.store.saved = 0, InvestigationResult{}
		_, _, err := wb.investigate(writeBackRequest(bundle, nil))
		if !errors.Is(err, ErrClientSynthesisUnavailable) {
			t.Fatalf("error = %v, want ErrClientSynthesisUnavailable", err)
		}
		wb.untouched(t, "no ParseDraft", reads)
		if decision := wb.onlyDecision(t, "no ParseDraft"); decision.Outcome != SuppliedSynthesisUnavailable {
			t.Fatalf("decision = %+v, want unavailable", decision)
		}
	})

	t.Run("no assembly", func(t *testing.T) {
		t.Parallel()
		bare := newWriteBackRig(t, func(r *clientRig) { r.synthesizer = RuntimeAnswerSynthesizer{Runtime: r.runtime} })
		_, _, err := bare.investigate(writeBackRequest(plausibleBundle(), nil))
		if !errors.Is(err, ErrClientSynthesisUnavailable) {
			t.Fatalf("error = %v, want ErrClientSynthesisUnavailable", err)
		}
		bare.untouched(t, "no assembly", 0)
		if decision := bare.onlyDecision(t, "no assembly"); decision.Outcome != SuppliedSynthesisUnavailable {
			t.Fatalf("decision = %+v, want unavailable", decision)
		}
	})

	t.Run("a synthesizer that cannot name its contract", func(t *testing.T) {
		t.Parallel()
		bare := newWriteBackRig(t, func(r *clientRig) { r.synthesizer = bareClientComposer{} })
		_, _, err := bare.investigate(writeBackRequest(plausibleBundle(), nil))
		if !errors.Is(err, ErrClientSynthesisUnavailable) {
			t.Fatalf("error = %v, want ErrClientSynthesisUnavailable", err)
		}
		bare.untouched(t, "bare composer", 0)
		if decision := bare.onlyDecision(t, "bare composer"); decision.Outcome != SuppliedSynthesisUnavailable {
			t.Fatalf("decision = %+v, want unavailable", decision)
		}
	})

	t.Run("no collector", func(t *testing.T) {
		t.Parallel()
		wb := newWriteBackRig(t, nil)
		_, bundle := wb.firstCall()
		reads := wb.factReads
		wb.supplied.interpret, wb.store.saved = 0, InvestigationResult{}
		_, err := wb.engine.Investigate(context.Background(), reusePrincipal(), writeBackRequest(bundle, nil))
		if !errors.Is(err, ErrClientSynthesisUnavailable) {
			t.Fatalf("error = %v, want ErrClientSynthesisUnavailable", err)
		}
		wb.untouched(t, "no collector", reads)
		if decision := wb.onlyDecision(t, "no collector"); decision.Outcome != SuppliedSynthesisUnavailable {
			t.Fatalf("decision = %+v, want unavailable", decision)
		}
	})

	t.Run("no supplied interpretation", func(t *testing.T) {
		t.Parallel()
		wb := newWriteBackRig(t, nil)
		_, bundle := wb.firstCall()
		reads := wb.factReads
		wb.supplied.interpret, wb.store.saved = 0, InvestigationResult{}
		_, _, err := wb.investigate(writeBackRequest(bundle, func(r *InvestigationRequest) { r.SuppliedInterpretation = nil }))
		if !errors.Is(err, ErrSuppliedInterpretationRequired) {
			t.Fatalf("error = %v, want ErrSuppliedInterpretationRequired", err)
		}
		wb.untouched(t, "no supplied interpretation", reads)
		if decision := wb.onlyDecision(t, "no supplied interpretation"); decision.Outcome != SuppliedSynthesisInterpretationRequired {
			t.Fatalf("decision = %+v, want interpretation_required", decision)
		}
	})

	t.Run("the interpretation requirement is judged before the contract", func(t *testing.T) {
		t.Parallel()
		wb := newWriteBackRig(t, nil)
		_, bundle := wb.firstCall()
		_, _, err := wb.investigate(writeBackRequest(bundle, func(r *InvestigationRequest) {
			r.SuppliedInterpretation = nil
			r.SuppliedSynthesis.PromptVersion = "old-prompt"
		}))
		var mismatch *SynthesisContractMismatch
		if !errors.Is(err, ErrSuppliedInterpretationRequired) || errors.As(err, &mismatch) {
			t.Fatalf("error = %v, want the interpretation requirement first", err)
		}
	})
}

// plausibleBundle is the contract values of the test assembly with an input digest.
func plausibleBundle() *contractsv1.ContextFabricSynthesisInput {
	return &contractsv1.ContextFabricSynthesisInput{
		Contract:    contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "synthesis-output-test", PromptVersion: "synthesis-prompt-test", SystemSHA256: hex.EncodeToString(sha256Sum("system"))},
		InputSHA256: hex.EncodeToString(sha256Sum("some input")),
	}
}

// bareClientComposer composes a client turn but cannot name a synthesis contract.
type bareClientComposer struct{}

func (bareClientComposer) Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
	return InvestigationResult{}, errors.New("not used")
}

func (bareClientComposer) ComposeForClientSynthesis(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, *contractsv1.ContextFabricSynthesisInput, error) {
	return InvestigationResult{}, nil, errors.New("not used")
}

// E3: a draft written from an input that has changed is not read.
func TestSuppliedSynthesisWrittenFromAChangedInputIsRefusedWithTheNewInput(t *testing.T) {
	t.Parallel()
	wb := newWriteBackRig(t, nil)
	_, old := wb.firstCall()
	wb.facts = append([]CanonicalFact{}, wb.facts...)
	wb.facts[0].Fields = map[string]FactValue{"status": StringFactValue("done")}
	wb.supplied.interpret, wb.store.saved = 0, InvestigationResult{}

	_, _, err := wb.investigate(writeBackRequest(old, nil))
	var changed *SynthesisInputChanged
	if !errors.As(err, &changed) || changed.Input == nil {
		t.Fatalf("error = %v, want *SynthesisInputChanged carrying the new input", err)
	}
	if wb.parse.calls != 0 {
		t.Fatalf("parse calls = %d, the draft must not be read when the input changed", wb.parse.calls)
	}
	if wb.store.saved.ResultID != "" || len(wb.sink.recorded) != 0 {
		t.Fatalf("saved = %q receipts = %d, want nothing", wb.store.saved.ResultID, len(wb.sink.recorded))
	}
	if err := changed.Input.Validate(); err != nil {
		t.Fatalf("new input Validate() = %v", err)
	}
	if changed.Input.InputSHA256 == old.InputSHA256 || bytes.Equal(changed.Input.Input, old.Input) {
		t.Fatal("the new input equals the old one")
	}
	_, fresh := wb.firstCall()
	if !reflect.DeepEqual(changed.Input, fresh) {
		t.Fatalf("new input differs from what a fresh call 1 returns now:\n got %+v\nwant %+v", changed.Input, fresh)
	}
	if decision := wb.onlyDecisionAfter(t, 1); decision.Outcome != SuppliedSynthesisInputChangedOutcome {
		t.Fatalf("decision = %+v, want input_changed", decision)
	}
}

// onlyDecisionAfter is onlyDecision for a rig whose call 1 ran before it.
func (w *writeBackRig) onlyDecisionAfter(t *testing.T, want int) SuppliedSynthesisDecisionEvent {
	t.Helper()
	got := w.telemetry.suppliedSynthesisDecisions
	if len(got) != want {
		t.Fatalf("supplied synthesis decisions = %+v, want %d", got, want)
	}
	return got[len(got)-1]
}

// E4: a draft that is not valid against the rebuilt input ends the turn.
func TestSuppliedSynthesisThatFailsValidationIsRejectedAndNothingIsSaved(t *testing.T) {
	t.Parallel()
	outside := SubjectRef{Kind: SubjectProject, CanonicalID: "project_outside_input", Label: "Outside"}
	cases := map[string]struct {
		prepare  func(*writeBackRig)
		reason   SynthesisRejectionReason
		outcome  string
		receipts int
	}{
		"a claimed value that is not in the facts": {func(w *writeBackRig) {
			w.parse.draft.ClaimedFacts[0].Value = boolScalar(true)
		}, RejectionReasonClaimValueContradicts, "invalid_output", 1},
		"a driver naming a subject outside the input": {func(w *writeBackRig) {
			w.parse.draft.Drivers[0].AffectedSubjects = []SubjectRef{outside}
		}, RejectionReasonDriverSubjectOutOfScope, "invalid_output", 1},
		"output that is not the schema": {func(w *writeBackRig) {
			w.parse.draft, w.parse.err = SynthesisDraft{}, errors.New("unknown field")
		}, RejectionReasonOutputSchemaMismatch, "invalid_output", 1},
		"output with a wrong type": {func(w *writeBackRig) {
			w.parse.draft, w.parse.err = SynthesisDraft{}, NewSynthesisRejection(RejectionReasonDeterministicAnswerMissing, errors.New("deterministic answer is required"))
		}, RejectionReasonDeterministicAnswerMissing, "invalid_output", 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wb := newWriteBackRig(t, nil)
			_, bundle := wb.firstCall()
			wb.supplied.interpret, wb.store.saved = 0, InvestigationResult{}
			tc.prepare(wb)
			result, _, err := wb.investigate(writeBackRequest(bundle, nil))
			if err == nil || !errors.Is(err, ErrSynthesisRejected) {
				t.Fatalf("error = %v, want one that matches ErrSynthesisRejected", err)
			}
			if got := SynthesisRejectionReasonOf(err); got != tc.reason {
				t.Fatalf("rejection reason = %q, want %q", got, tc.reason)
			}
			if result.ResultID != "" || result.Status == InvestigationDegraded {
				t.Fatalf("result = %+v, want no answer, degraded or not", result)
			}
			if wb.store.saved.ResultID != "" {
				t.Fatal("a rejected draft was saved")
			}
			if wb.runtime.synthCalls != 0 || wb.runtime.interpretCalls != 0 {
				t.Fatalf("model calls = %d/%d, want none", wb.runtime.synthCalls, wb.runtime.interpretCalls)
			}
			if len(wb.sink.recorded) != tc.receipts || wb.sink.recorded[0].Outcome != tc.outcome {
				t.Fatalf("receipts = %+v, want %d with outcome %s", wb.sink.recorded, tc.receipts, tc.outcome)
			}
			if wb.sink.recorded[0].Provider != contractsv1.ContextFabricClientSuppliedProvider {
				t.Fatalf("receipt provider = %q, want client-supplied", wb.sink.recorded[0].Provider)
			}
			decision := wb.onlyDecisionAfter(t, 1)
			if decision.Outcome != SuppliedSynthesisRejected || decision.RejectionReason != tc.reason {
				t.Fatalf("decision = %+v, want rejected with %q", decision, tc.reason)
			}
		})
	}
}

func emptyWriteBackDraft() SynthesisDraft {
	return SynthesisDraft{
		Status: InvestigationPartial, DirectJudgment: "Nothing is said about the subject.", CurrentState: "Nothing is said about the subject.",
		StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, DeterministicAnswer: "Nothing is said about the subject.", Warnings: []string{},
	}
}

// E5: a commit nothing proves stays when the draft affirms it and is
// retracted, with the model path's limitation only, when it does not.
func TestSuppliedSynthesisAffirmsOrRetractsAnUnprovenCommitLikeTheModelPath(t *testing.T) {
	t.Parallel()
	affirmed := newWriteBackRig(t, func(r *clientRig) { r.graph.bases = nil })
	_, bundle := affirmed.firstCall()
	result, _, err := affirmed.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("affirming draft: Investigate() error = %v", err)
	}
	if len(result.SubjectResolution.Committed) != 1 || hasLimitation(result.Limitations, commitRetractionLimitation) || hasLimitation(result.Limitations, contractsv1.ContextFabricClientSynthesisCommitNotAffirmedLimitation) {
		t.Fatalf("committed = %v limitations = %v, want the commit kept and no retraction limitation", result.SubjectResolution.Committed, result.Limitations)
	}

	retracted := newWriteBackRig(t, func(r *clientRig) { r.graph.bases = nil })
	_, bundle = retracted.firstCall()
	retracted.parse.draft = emptyWriteBackDraft()
	result, _, err = retracted.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("silent draft: Investigate() error = %v", err)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed = %v, want the commit retracted", result.SubjectResolution.Committed)
	}
	if !hasLimitation(result.Limitations, commitRetractionLimitation) || hasLimitation(result.Limitations, contractsv1.ContextFabricClientSynthesisCommitNotAffirmedLimitation) {
		t.Fatalf("limitations = %v, want the commit retraction limitation and not the client one", result.Limitations)
	}
	if got := retracted.telemetry.clientSynthesisDecisions; len(got) != 1 {
		t.Fatalf("client decisions = %+v, want only call 1's", got)
	}
}

// E6: the draft goes through the model path's own sequence.
func TestSuppliedSynthesisDraftIsStrippedAndDeconflictedLikeAModelDraft(t *testing.T) {
	t.Parallel()
	rows := []ClaimedFactRow{{Fields: map[string]ScalarValue{"anything": {String: strPtr("authored by the model")}}}}
	draftFor := func(project SubjectRef) SynthesisDraft {
		draft := bootstrapDraft(project)
		draft.ClaimedFacts[0].Rows = rows
		second := draft.Drivers[0]
		second.Title = "A second reading of the same readiness fact"
		draft.Drivers = append(draft.Drivers, second)
		return draft
	}
	wb := newWriteBackRig(t, nil)
	_, bundle := wb.firstCall()
	wb.parse.draft = draftFor(wb.project)
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	server := newClientRig(t, func(r *clientRig) { r.runtime.draft = draftFor(r.project) })
	want, _, err := server.investigate(serverRequest())
	if err != nil {
		t.Fatalf("server Investigate() error = %v", err)
	}
	if !reflect.DeepEqual(wb.telemetry.modelRowsStripped, []int{1}) || !reflect.DeepEqual(wb.telemetry.modelRowsStripped, server.telemetry.modelRowsStripped) {
		t.Fatalf("rows stripped = %v, want one claim, as the model path records (%v)", wb.telemetry.modelRowsStripped, server.telemetry.modelRowsStripped)
	}
	if len(wb.telemetry.driverIdentityCollisions) != 1 || wb.telemetry.driverIdentityCollisions[0].Total() != 1 || !reflect.DeepEqual(wb.telemetry.driverIdentityCollisions, server.telemetry.driverIdentityCollisions) {
		t.Fatalf("driver collisions = %+v, want one resolved, as the model path records (%+v)", wb.telemetry.driverIdentityCollisions, server.telemetry.driverIdentityCollisions)
	}
	if len(result.Drivers) != 2 || result.Drivers[0].DriverID == result.Drivers[1].DriverID || !reflect.DeepEqual(result.Drivers, want.Drivers) {
		t.Fatalf("drivers = %+v, want two with distinct ids equal to the model path's %+v", result.Drivers, want.Drivers)
	}
	if !reflect.DeepEqual(result.ClaimedFacts, want.ClaimedFacts) {
		t.Fatalf("claimed facts differ from the model path's:\n got %+v\nwant %+v", result.ClaimedFacts, want.ClaimedFacts)
	}
}

func strPtr(value string) *string { return &value }

// E9: a write-back turn neither reads nor writes answer reuse.
func TestSuppliedSynthesisTurnNeverReadsOrWritesAnswerReuse(t *testing.T) {
	t.Parallel()
	wb := newWriteBackRig(t, nil)
	_, bundle := wb.firstCall()
	_, candidate := reusableCandidate()
	wb.reuse = &candidate
	wb.lookups = 0
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if wb.lookups != 0 || result.Reused {
		t.Fatalf("reuse lookups = %d reused = %v, want no lookup and a fresh answer", wb.lookups, result.Reused)
	}
	if wb.store.saved.ResultID != result.ResultID || wb.store.savedSnapshot != nil || wb.store.savedEpoch != nil {
		t.Fatalf("Save: result %q snapshot = %v epoch = %v, want the row with nil reuse columns", wb.store.saved.ResultID, wb.store.savedSnapshot, wb.store.savedEpoch)
	}
}

// E10: no text of the caller's output reaches any event or log line.
func TestSuppliedSynthesisDecisionCarriesNoTextOfTheCallersOutput(t *testing.T) {
	t.Parallel()
	for name, reject := range map[string]bool{"served": false, "rejected": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			wb := newWriteBackRig(t, func(r *clientRig) {
				r.engineTelemetry = NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&logs, nil)))
			})
			question := func(r *InvestigationRequest) { r.Question = "a question " + writeBackMarker + "?" }
			_, bundle := wb.firstCallWith(question)
			wb.parse.draft.DirectJudgment = writeBackMarker + " judgment"
			wb.parse.draft.CurrentState = writeBackMarker + " state"
			wb.parse.draft.DeterministicAnswer = writeBackMarker + " answer"
			wb.parse.draft.Drivers[0].Title = writeBackMarker + " title"
			wb.parse.draft.Limitations = []string{writeBackMarker + " limitation"}
			if reject {
				wb.parse.draft.ClaimedFacts[0].Value = boolScalar(true)
			}
			logs.Reset()
			request := writeBackRequest(bundle, question)
			if _, _, err := wb.investigate(request); (err != nil) != reject {
				t.Fatalf("Investigate() error = %v, want an error: %v", err, reject)
			}
			if strings.Contains(logs.String(), writeBackMarker) {
				t.Fatalf("a log line of the turn carries the caller's text:\n%s", logs.String())
			}
			lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
			var decisions []map[string]any
			for _, line := range lines {
				var parsed map[string]any
				if err := json.Unmarshal(line, &parsed); err == nil && parsed["msg"] == SuppliedSynthesisDecisionLogMessage {
					decisions = append(decisions, parsed)
				}
			}
			if len(decisions) != 1 {
				t.Fatalf("decision lines = %d, want one in:\n%s", len(decisions), logs.String())
			}
			for _, line := range lines {
				if bytes.Contains(line, []byte(ClientSynthesisDecisionLogMessage)) {
					t.Fatalf("a turn that carries a draft wrote a client synthesis decision line: %s", line)
				}
			}
			want, wantReason := "served", ""
			if reject {
				want, wantReason = "rejected", string(RejectionReasonClaimValueContradicts)
			}
			if decisions[0]["outcome"] != want || decisions[0]["rejection_reason"] != wantReason || decisions[0]["client_model"] != "writer-model-1" {
				t.Fatalf("decision line = %v, want outcome %q reason %q model writer-model-1", decisions[0], want, wantReason)
			}
		})
	}

	t.Run("recorded events", func(t *testing.T) {
		t.Parallel()
		wb := newWriteBackRig(t, nil)
		_, bundle := wb.firstCall()
		wb.parse.draft.DirectJudgment = writeBackMarker
		if _, _, err := wb.investigate(writeBackRequest(bundle, nil)); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		recorded, err := json.Marshal(wb.telemetry.suppliedSynthesisDecisions)
		if err != nil || bytes.Contains(recorded, []byte(writeBackMarker)) {
			t.Fatalf("recorded decision %s (marshal error %v) carries the caller's text", recorded, err)
		}
	})
}

// A receipt that cannot be recorded ends the turn, as on the model path.
func TestSuppliedSynthesisTurnFailsWhenItsReceiptCannotBeRecorded(t *testing.T) {
	t.Parallel()
	wb := newWriteBackRig(t, nil)
	_, bundle := wb.firstCall()
	wb.store.saved = InvestigationResult{}
	wb.sink.err = errors.New("receipt store down")
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err == nil || !errors.Is(err, wb.sink.err) {
		t.Fatalf("error = %v, want the receipt sink's error", err)
	}
	if result.ResultID != "" || wb.store.saved.ResultID != "" {
		t.Fatal("a turn whose receipt was not recorded served or saved an answer")
	}
}

// The composer refuses a write-back it cannot parse, whoever calls it.
func TestSuppliedSynthesisComposerWithoutAParserIsUnavailable(t *testing.T) {
	t.Parallel()
	wb := newWriteBackRig(t, nil)
	_, bundle := wb.firstCall()
	synthesizer := RuntimeAnswerSynthesizer{ClientSynthesis: &ClientSynthesisAssembly{
		PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: bundle.Contract.SystemSHA256, Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
	}}
	input := validSynthesisInputFixture()
	input.Request = writeBackRequest(bundle, nil)
	if _, _, err := synthesizer.ComposeForClientSynthesis(context.Background(), reusePrincipal(), input); !errors.Is(err, ErrClientSynthesisUnavailable) {
		t.Fatalf("ComposeForClientSynthesis() error = %v, want ErrClientSynthesisUnavailable", err)
	}
}
