package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	writeBackMarker   = "zx-marker-7f3a91-do-not-log"
	writeBackQuestion = "Why is the payments ledger service not ready to ship?"
	writeBackOutput   = `{"shape":"single_subject","requested_judgment":"release readiness","subject_terms":["the ledger service"],"time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"}],"clarification_needed":false}`
)

func writeBackProject() contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_zebra_ledger", Label: "Zebra Ledger"}
}

// writeBackModel is the model runtime behind the route. It counts every call.
// A write-back must make none; the server-mode leg of a comparison uses it.
type writeBackModel struct {
	mu          sync.Mutex
	interprets  int
	synthesizes int
	interpreted contextfabric.InterpretedQuestion
	draft       contextfabric.SynthesisDraft
}

func (m *writeBackModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interprets++
	return m.interpreted, clientRouteReceipt(contextfabric.ModelOperationInterpret), nil
}

func (m *writeBackModel) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synthesizes++
	return m.draft, clientRouteReceipt(contextfabric.ModelOperationSynthesize), nil
}

func (m *writeBackModel) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interprets, m.synthesizes
}

type acceptingSink struct{}

func (acceptingSink) RecordModelExecution(context.Context, storage.Principal, contextfabric.ModelExecutionReceipt) error {
	return nil
}

// savingStore records every result the engine saves.
type savingStore struct {
	*memoryinvestigation.Store
	mu    sync.Mutex
	saved []contextfabric.InvestigationResult
}

func (s *savingStore) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult, snapshot contextfabric.SourceWatermarkSnapshot, epoch contextfabric.RebuildEpoch, axis string, identity contextfabric.ReuseRetrievalIdentity, prompts contextfabric.ReusePromptVersions, authorities contextfabric.ReuseVersionAuthorities, bytes int64, parent string, semantic contextfabric.SemanticStateWrite) error {
	if err := s.Store.Save(ctx, principal, result, snapshot, epoch, axis, identity, prompts, authorities, bytes, parent, semantic); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, result)
	return nil
}

func (s *savingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

// writeBackGraph is the authorizing graph whose live verdict and fact bundle
// the test changes between the two calls.
type writeBackGraph struct {
	authorizingGraph
	rig *writeBackRouteRig
}

func (g writeBackGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.authorizingGraph.outcome = g.rig.currentOutcome()
	return g.authorizingGraph.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

func (g writeBackGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	project := g.rig.project
	return contextfabric.GraphContext{
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
		Paths:      []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type writeBackRouteRig struct {
	app     *App
	token   string
	model   *writeBackModel
	store   *savingStore
	logs    *bytes.Buffer
	project contextfabric.SubjectRef
	nextID  int

	mu        sync.Mutex
	facts     contextfabric.CanonicalFactBundle
	outcome   contextfabric.StoredSubjectOutcome
	factReads int
}

func (r *writeBackRouteRig) currentOutcome() contextfabric.StoredSubjectOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outcome
}

func (r *writeBackRouteRig) setOutcome(outcome contextfabric.StoredSubjectOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcome = outcome
}

func (r *writeBackRouteRig) setFacts(bundle contextfabric.CanonicalFactBundle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.facts = bundle
}

func (r *writeBackRouteRig) reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.factReads
}

func (r *writeBackRouteRig) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factReads++
	return r.facts, nil
}

func newWriteBackRouteRig(t *testing.T) *writeBackRouteRig {
	t.Helper()
	project := writeBackProject()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	model := &writeBackModel{interpreted: contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "release readiness", SubjectTerms: []string{"the ledger service"},
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}}
	store := &savingStore{Store: memoryinvestigation.NewStore()}
	rig := &writeBackRouteRig{
		model: model, store: store, logs: logs, project: project,
		facts: liveCanonicalFacts(project), outcome: contextfabric.StoredSubjectAdmitted,
	}
	suppliedInterpreter, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	suppliedSynthesizer, err := genkitruntime.NewSuppliedSynthesizer(genkitruntime.SuppliedSynthesizerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	assembly := synthesisprompt.ClientAssembly()
	assembly.ParseDraft = suppliedSynthesizer.Parse
	telemetry := contextfabric.NewSlogEngineTelemetry(logger)
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model, Supplied: suppliedInterpreter},
		Graph:       writeBackGraph{authorizingGraph: authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: contextfabric.StoredSubjectAdmitted}, rig: rig},
		Facts:       rig,
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{
			Runtime: model, Sink: acceptingSink{}, Telemetry: telemetry, ClientSynthesis: assembly,
			Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "write-back-route", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
		},
		Results: store, Telemetry: telemetry,
	}, contextfabric.EngineOptions{
		ServiceVersion: "write-back-route",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			rig.nextID++
			return fmt.Sprintf("result_write_back%02d", rig.nextID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.app, rig.token = newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, logs)
	return rig
}

func writeBackInterpretation(t *testing.T) *contractsv1.ContextFabricSuppliedInterpretation {
	t.Helper()
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	return &contractsv1.ContextFabricSuppliedInterpretation{
		Output: json.RawMessage(writeBackOutput), ModelOutputVersion: contract.ModelOutputVersion,
		PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256, ClientModel: "claude-test",
	}
}

func (r *writeBackRouteRig) post(t *testing.T, edit func(*contractsv1.ContextFabricInvestigationRequest)) *httptest.ResponseRecorder {
	t.Helper()
	body := investigationRequestBody()
	body.Question = writeBackQuestion
	body.SynthesisMode = contractsv1.ContextFabricSynthesisModeClient
	body.SuppliedInterpretation = writeBackInterpretation(t)
	body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	if edit != nil {
		edit(&body)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

// firstCall is call 1: client mode, no draft. It returns the bundle.
func (r *writeBackRouteRig) firstCall(t *testing.T) contractsv1.ContextFabricInvestigationResponse {
	t.Helper()
	envelope := decodeEnvelope(t, r.post(t, nil))
	if envelope.SynthesisInput == nil {
		t.Fatal("call 1 returned no synthesis input")
	}
	return envelope
}

func writeBackDraft(project contextfabric.SubjectRef, marker string) contextfabric.SynthesisDraft {
	return contextfabric.SynthesisDraft{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "The service appears not ready to ship.",
		CurrentState:       "Release readiness appears to be open.",
		StrongestPressures: []string{"Release readiness appears to be open."},
		Drivers: []contextfabric.DriverJudgment{{
			DriverID: "driver_writeback01", Standing: contextfabric.DriverPrincipal, Category: "readiness",
			Title: "Release readiness is false " + marker, Summary: "The canonical readiness evaluation is negative. " + marker,
			AffectedSubjects: []contextfabric.SubjectRef{project}, EvidenceRefIDs: []string{"evidence_readiness_0001"},
			ClaimedFactIDs: []string{"claim_readiness_writeback"},
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.98, Current: true,
		}},
		RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{},
		Limitations:    []string{"Only canonical facts were read. " + marker},
		EvidenceRefIDs: []string{"evidence_status_0001", "evidence_readiness_0001"},
		ClaimedFacts: []contextfabric.ClaimedFact{{
			ClaimID: "claim_readiness_writeback", Kind: contextfabric.FactReadiness, Subject: project, Field: "release_ready",
			Value: contextfabric.ScalarValue{Boolean: boolPointer(false)},
		}},
		DeterministicAnswer: "The service appears not ready to ship.", Warnings: []string{},
	}
}

func boolPointer(value bool) *bool { return &value }

func writeBackOutputJSON(t *testing.T, draft contextfabric.SynthesisDraft) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (r *writeBackRouteRig) writeBack(t *testing.T, bundle *contractsv1.ContextFabricSynthesisInput, output json.RawMessage, edit func(*contractsv1.ContextFabricSuppliedSynthesis)) *httptest.ResponseRecorder {
	t.Helper()
	return r.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
		supplied := &contractsv1.ContextFabricSuppliedSynthesis{
			Output: output, ModelOutputVersion: bundle.Contract.ModelOutputVersion, PromptVersion: bundle.Contract.PromptVersion,
			SystemSHA256: bundle.Contract.SystemSHA256, InputSHA256: bundle.InputSHA256, ClientModel: "writer-model-1",
		}
		if edit != nil {
			edit(supplied)
		}
		body.SuppliedSynthesis = supplied
	})
}

type refusalBody struct {
	Error struct {
		Code      string                     `json:"code"`
		Retryable bool                       `json:"retryable"`
		Details   map[string]json.RawMessage `json:"details"`
	} `json:"error"`
}

func decodeRefusal(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) refusalBody {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), status)
	}
	var body refusalBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("code = %q body=%s, want %q", body.Error.Code, recorder.Body.String(), code)
	}
	return body
}

func detailString(t *testing.T, body refusalBody, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(body.Error.Details[key], &value); err != nil {
		t.Fatalf("details.%s = %s: %v", key, body.Error.Details[key], err)
	}
	return value
}

func TestWriteBackIsServedWithoutAModelCall(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	savedAfterFirst := rig.store.count()
	draft := writeBackDraft(rig.project, writeBackMarker)
	recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, draft), nil)
	served := decodeEnvelope(t, recorder)

	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = %d interpret / %d synthesize across both calls, want none", interprets, synthesizes)
	}
	if _, present := topLevelKeys(t, recorder.Body.Bytes())["synthesis_input"]; present {
		t.Fatal("a served write-back carries a synthesis_input key")
	}
	versions := served.Versions
	if versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient || versions.ModelIdentity != "client-supplied/writer-model-1" || versions.SynthesisVersion != synthesisprompt.PromptVersion {
		t.Fatalf("versions = source %q identity %q synthesis %q, want client / client-supplied/writer-model-1 / %q", versions.SynthesisSource, versions.ModelIdentity, versions.SynthesisVersion, synthesisprompt.PromptVersion)
	}
	if served.Status != contractsv1.ContextFabricInvestigationComplete {
		t.Fatalf("status = %q, want the draft's complete", served.Status)
	}
	if len(served.Drivers) != 1 || served.Drivers[0].DriverID != "driver_writeback01" || len(served.ClaimedFacts) == 0 || served.ClaimedFacts[0].ClaimID != "claim_readiness_writeback" {
		t.Fatalf("drivers = %+v claims = %+v, want the draft's", served.Drivers, served.ClaimedFacts)
	}
	if served.DeterministicAnswer == contractsv1.ContextFabricClientSynthesisAnswer || served.DeterministicAnswer == "" {
		t.Fatalf("deterministic_answer = %q, want the composed answer", served.DeterministicAnswer)
	}
	if rig.store.count() != savedAfterFirst+1 {
		t.Fatalf("saved results = %d, want one more than after call 1 (%d)", rig.store.count(), savedAfterFirst)
	}

	read := rig.get(served.ResultID)
	if read.Code != http.StatusOK {
		t.Fatalf("read by id: status = %d body=%s", read.Code, read.Body.String())
	}
	var stored contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(read.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.DeterministicAnswer != served.DeterministicAnswer || stored.Versions.ModelIdentity != versions.ModelIdentity || len(stored.Drivers) != 1 {
		t.Fatalf("stored answer differs from the served one: %+v", stored.Versions)
	}
	if _, present := topLevelKeys(t, read.Body.Bytes())["synthesis_input"]; present {
		t.Fatal("read by id returned a synthesis_input")
	}
}

func TestWriteBackContractMismatchNamesTheFieldsAndDoesNoWork(t *testing.T) {
	current := contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: synthesisprompt.OutputVersion, PromptVersion: synthesisprompt.PromptVersion, SystemSHA256: systemSHA(t)}
	cases := map[string]struct {
		edit func(*contractsv1.ContextFabricSuppliedSynthesis)
		want []string
	}{
		"model_output_version differs": {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "old.v1" }, []string{"model_output_version"}},
		"prompt_version differs":       {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.PromptVersion = "old.v1" }, []string{"prompt_version"}},
		"system_sha256 differs":        {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.SystemSHA256 = strings.Repeat("c", 64) }, []string{"system_sha256"}},
		"model_output_version missing": {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.ModelOutputVersion = "" }, []string{"model_output_version"}},
		"prompt_version missing":       {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.PromptVersion = "" }, []string{"prompt_version"}},
		"system_sha256 missing":        {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.SystemSHA256 = "" }, []string{"system_sha256"}},
		"input_sha256 missing":         {func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.InputSHA256 = "" }, []string{"input_sha256"}},
		"two differ": {func(s *contractsv1.ContextFabricSuppliedSynthesis) {
			s.PromptVersion = "old.v1"
			s.InputSHA256 = ""
		}, []string{"prompt_version", "input_sha256"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := rig.firstCall(t)
			readsBefore, savedBefore := rig.reads(), rig.store.count()
			recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), tc.edit)
			body := decodeRefusal(t, recorder, http.StatusConflict, "invalid_request")
			var refusal contractsv1.ContextFabricSynthesisContractRefusal
			if err := json.Unmarshal(body.Error.Details["synthesis_contract"], &refusal); err != nil {
				t.Fatal(err)
			}
			if err := refusal.Validate(); err != nil {
				t.Fatal(err)
			}
			if strings.Join(refusal.Mismatch, ",") != strings.Join(tc.want, ",") || refusal.Current != current {
				t.Fatalf("refusal = %+v, want mismatch %v and the current contract %+v", refusal, tc.want, current)
			}
			if rig.reads() != readsBefore || rig.store.count() != savedBefore {
				t.Fatalf("fact reads %d -> %d saves %d -> %d, want no work", readsBefore, rig.reads(), savedBefore, rig.store.count())
			}
			if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
				t.Fatalf("model calls = %d / %d", interprets, synthesizes)
			}
		})
	}
}

func TestWriteBackWithoutASuppliedInterpretationIsRefused(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	readsBefore := rig.reads()
	recorder := rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
		body.SuppliedInterpretation = nil
		body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
			Output: writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), ModelOutputVersion: first.SynthesisInput.Contract.ModelOutputVersion,
			PromptVersion: first.SynthesisInput.Contract.PromptVersion, SystemSHA256: first.SynthesisInput.Contract.SystemSHA256, InputSHA256: first.SynthesisInput.InputSHA256,
		}
	})
	body := decodeRefusal(t, recorder, http.StatusBadRequest, "invalid_request")
	if got := detailString(t, body, "reason"); got != contractsv1.ContextFabricSuppliedSynthesisReasonInterpretationRequired {
		t.Fatalf("reason = %q", got)
	}
	if rig.reads() != readsBefore {
		t.Fatal("the refusal read facts")
	}
	if interprets, _ := rig.model.counts(); interprets != 0 {
		t.Fatal("the refusal reached the model")
	}
}

func TestWriteBackAfterTheFactsChangedCarriesTheNewInput(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	changed := liveCanonicalFacts(rig.project)
	changed.Facts[0].Fields["status"] = contextfabric.StringFactValue("blocked")
	rig.setFacts(changed)
	savedBefore := rig.store.count()

	recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), nil)
	body := decodeRefusal(t, recorder, http.StatusConflict, "invalid_request")
	if got := detailString(t, body, "reason"); got != "input_changed" {
		t.Fatalf("reason = %q, want input_changed", got)
	}
	var bundle contractsv1.ContextFabricSynthesisInput
	if err := json.Unmarshal(body.Error.Details["synthesis_input"], &bundle); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("the new bundle does not validate: %v", err)
	}
	sum := sha256.Sum256(bundle.Input)
	if bundle.InputSHA256 == first.SynthesisInput.InputSHA256 || bundle.InputSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("input_sha256 = %s, want the digest of the new input and not call 1's", bundle.InputSHA256)
	}
	fresh := rig.firstCall(t)
	if fresh.SynthesisInput.InputSHA256 != bundle.InputSHA256 {
		t.Fatalf("the refusal's input digest %s is not the one a fresh first call builds (%s)", bundle.InputSHA256, fresh.SynthesisInput.InputSHA256)
	}
	if rig.store.count() != savedBefore+1 {
		t.Fatalf("saves = %d, want only the fresh first call's (%d before)", rig.store.count(), savedBefore)
	}
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = %d / %d", interprets, synthesizes)
	}
}

func TestWriteBackOfADraftTheFactsDoNotSupportIsRejected(t *testing.T) {
	project := writeBackProject()
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_other_team", Label: "Other Team"}
	cases := map[string]func(*contextfabric.SynthesisDraft){
		"a fact value the input does not hold": func(d *contextfabric.SynthesisDraft) {
			d.ClaimedFacts[0].Value = contextfabric.ScalarValue{Boolean: boolPointer(true)}
		},
		"a subject outside the input": func(d *contextfabric.SynthesisDraft) {
			d.Drivers[0].AffectedSubjects = []contextfabric.SubjectRef{other}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := rig.firstCall(t)
			savedBefore := rig.store.count()
			draft := writeBackDraft(project, writeBackMarker)
			mutate(&draft)
			recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, draft), nil)
			body := decodeRefusal(t, recorder, http.StatusUnprocessableEntity, "synthesis_rejected")
			reason := contextfabric.SynthesisRejectionReason(detailString(t, body, "rejection_reason"))
			if !contextfabric.ValidSynthesisRejectionReason(reason) || reason == contextfabric.RejectionReasonUnclassified {
				t.Fatalf("rejection_reason = %q, want a named member of the closed vocabulary", reason)
			}
			if rig.store.count() != savedBefore {
				t.Fatal("a rejected draft was saved")
			}
			if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
				t.Fatalf("model calls = %d / %d", interprets, synthesizes)
			}
		})
	}
	t.Run("an unknown field", func(t *testing.T) {
		rig := newWriteBackRouteRig(t)
		first := rig.firstCall(t)
		savedBefore := rig.store.count()
		var document map[string]any
		if err := json.Unmarshal(writeBackOutputJSON(t, writeBackDraft(project, writeBackMarker)), &document); err != nil {
			t.Fatal(err)
		}
		document["extra_field"] = "x"
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		body := decodeRefusal(t, rig.writeBack(t, first.SynthesisInput, raw, nil), http.StatusUnprocessableEntity, "synthesis_rejected")
		if got := detailString(t, body, "rejection_reason"); got != string(contextfabric.RejectionReasonOutputSchemaMismatch) {
			t.Fatalf("rejection_reason = %q, want %q", got, contextfabric.RejectionReasonOutputSchemaMismatch)
		}
		if rig.store.count() != savedBefore {
			t.Fatal("a rejected draft was saved")
		}
	})
}

// A caller whose access no longer covers the subject, sending the draft it
// wrote while it did, is answered like any turn: the live authorization of
// the turn is not satisfied, the subject is not committed, no fact is read
// for it and the draft is never applied.
func TestWriteBackAfterTheCallerLostAccessLeaksNothingAboutTheSubject(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	rig.setOutcome(contextfabric.StoredSubjectDenied)
	readsBefore := rig.reads()
	recorder := rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, writeBackDraft(rig.project, writeBackMarker)), nil)

	raw := recorder.Body.String()
	for _, secret := range []string{rig.project.Label, rig.project.CanonicalID, writeBackMarker, "evidence_readiness_0001", "evidence_status_0001"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the response of a turn the caller may no longer read contains %q: %s", secret, raw)
		}
	}
	if recorder.Code == http.StatusOK {
		var result contractsv1.ContextFabricInvestigationResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.SubjectResolution.Committed) != 0 || len(result.Drivers) != 0 || len(result.ClaimedFacts) != 0 || result.SynthesisInput != nil ||
			result.Versions.ModelIdentity == "client-supplied/writer-model-1" {
			t.Fatalf("the draft was applied for a subject the caller cannot read: %+v", result)
		}
	}
	if rig.reads() != readsBefore {
		t.Fatalf("fact reads = %d, want none for the denied subject (%d before)", rig.reads(), readsBefore)
	}
	var withDraft, withoutDraft contractsv1.ContextFabricInvestigationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &withDraft); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rig.post(t, nil).Body.Bytes(), &withoutDraft); err != nil {
		t.Fatal(err)
	}
	if withDraft.Status != withoutDraft.Status || strings.Join(withDraft.Limitations, "|") != strings.Join(withoutDraft.Limitations, "|") ||
		len(withDraft.SubjectResolution.Committed) != len(withoutDraft.SubjectResolution.Committed) {
		t.Fatalf("the write-back is answered differently from the same turn without a draft:\n with   %q %q\n without %q %q",
			withDraft.Status, withDraft.Limitations, withoutDraft.Status, withoutDraft.Limitations)
	}
	for _, saved := range rig.store.saved[1:] {
		encoded, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{rig.project.Label, rig.project.CanonicalID, writeBackMarker} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("a result saved for the caller who lost access contains %q", secret)
			}
		}
	}
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = %d / %d", interprets, synthesizes)
	}
}

func TestWriteBackTurnsNeverLogTheCallersText(t *testing.T) {
	project := writeBackProject()
	flows := map[string]func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder{
		"served": func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder {
			return rig.writeBack(t, bundle, writeBackOutputJSON(t, writeBackDraft(project, writeBackMarker)), nil)
		},
		"rejected": func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder {
			draft := writeBackDraft(project, writeBackMarker)
			draft.ClaimedFacts[0].Value = contextfabric.ScalarValue{Boolean: boolPointer(true)}
			return rig.writeBack(t, bundle, writeBackOutputJSON(t, draft), nil)
		},
		"schema mismatch": func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder {
			return rig.writeBack(t, bundle, json.RawMessage(`{"`+writeBackMarker+`":"`+writeBackMarker+`"}`), nil)
		},
		"input changed": func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder {
			changed := liveCanonicalFacts(project)
			changed.Facts[0].Fields["status"] = contextfabric.StringFactValue("blocked")
			rig.setFacts(changed)
			return rig.writeBack(t, bundle, writeBackOutputJSON(t, writeBackDraft(project, writeBackMarker)), nil)
		},
		"contract mismatch": func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder {
			return rig.writeBack(t, bundle, writeBackOutputJSON(t, writeBackDraft(project, writeBackMarker)), func(s *contractsv1.ContextFabricSuppliedSynthesis) { s.PromptVersion = "old.v1" })
		},
		"lost access": func(t *testing.T, rig *writeBackRouteRig, bundle *contractsv1.ContextFabricSynthesisInput) *httptest.ResponseRecorder {
			rig.setOutcome(contextfabric.StoredSubjectDenied)
			return rig.writeBack(t, bundle, writeBackOutputJSON(t, writeBackDraft(project, writeBackMarker)), nil)
		},
	}
	for name, flow := range flows {
		t.Run(name, func(t *testing.T) {
			rig := newWriteBackRouteRig(t)
			first := rig.firstCall(t)
			rig.logs.Reset()
			recorder := flow(t, rig, first.SynthesisInput)
			if name != "served" && name != "lost access" && recorder.Code == http.StatusOK {
				t.Fatalf("status = 200, want a refusal")
			}
			logged := rig.logs.String()
			if logged == "" {
				t.Fatal("the turn wrote no log line: the capture is not wired")
			}
			if strings.Contains(logged, writeBackMarker) {
				t.Fatalf("a log line carries the caller's text: %s", logged)
			}
			if name != "lost access" && !strings.Contains(logged, contextfabric.SuppliedSynthesisDecisionLogMessage) && name != "schema mismatch" {
				t.Fatalf("no supplied synthesis decision line in %s", logged)
			}
		})
	}
}

// The same synthesis rejection is answered as it was before a caller could
// send a draft when the request carries none, and with the closed reason when
// it does.
func TestSynthesisRejectionResponseDependsOnWhetherTheRequestCarriesADraft(t *testing.T) {
	rejection := func() error {
		return contextfabric.NewModelBoundViolation("synthesis.driver.title.max_length",
			contextfabric.NewSynthesisRejection(contextfabric.RejectionReasonDriverInvalid, fmt.Errorf("%w: driver judgment violates v1 bounds", contextfabric.ErrSynthesisRejected)))
	}
	post := func(t *testing.T, supplied bool) refusalBody {
		t.Helper()
		app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			return contextfabric.InvestigationResult{}, rejection()
		}))
		body := investigationRequestBody()
		if supplied {
			body.SynthesisMode = contractsv1.ContextFabricSynthesisModeClient
			body.SuppliedInterpretation = writeBackInterpretation(t)
			body.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
				Output: json.RawMessage(`{}`), ModelOutputVersion: "o", PromptVersion: "p", SystemSHA256: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64),
			}
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		return decodeRefusal(t, recorder, http.StatusUnprocessableEntity, "synthesis_rejected")
	}

	model := post(t, false)
	if !model.Error.Retryable || len(model.Error.Details) != 1 || string(model.Error.Details["violated_bound"]) != `"synthesis.driver.title.max_length"` {
		t.Fatalf("model-path rejection = retryable %v details %v, want retryable with only the violated bound", model.Error.Retryable, model.Error.Details)
	}

	supplied := post(t, true)
	if supplied.Error.Retryable {
		t.Fatal("a rejected supplied draft is marked retryable")
	}
	if got := detailString(t, supplied, "rejection_reason"); got != string(contextfabric.RejectionReasonDriverInvalid) {
		t.Fatalf("rejection_reason = %q, want %q", got, contextfabric.RejectionReasonDriverInvalid)
	}
}

func (r *writeBackRouteRig) get(resultID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, ContextFabricInvestigationsPath+"/"+resultID, nil)
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

// An input-changed error that carries no input cannot be answered with the
// refusal that promises one.
func TestInputChangedWithoutAnInputIsNotAnsweredAsInputChanged(t *testing.T) {
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, &contextfabric.SynthesisInputChanged{}
	}))
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationRequest(t, token))
	if recorder.Code == http.StatusConflict || strings.Contains(recorder.Body.String(), "synthesis_input") {
		t.Fatalf("status = %d body=%s, want no input-changed refusal", recorder.Code, recorder.Body.String())
	}
}

func TestWriteBackWithAnUnknownMemberInACoverageDisclosureIsRejected(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	first := rig.firstCall(t)
	savedBefore := rig.store.count()
	var document map[string]any
	if err := json.Unmarshal(writeBackOutputJSON(t, writeBackDraft(writeBackProject(), writeBackMarker)), &document); err != nil {
		t.Fatal(err)
	}
	document["coverage_disclosures"] = []any{map[string]any{"detail_id": "cov-01", "text": "x", "extra": "y"}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeRefusal(t, rig.writeBack(t, first.SynthesisInput, raw, nil), http.StatusUnprocessableEntity, "synthesis_rejected")
	if got := detailString(t, body, "rejection_reason"); got != string(contextfabric.RejectionReasonOutputSchemaMismatch) {
		t.Fatalf("rejection_reason = %q, want %q", got, contextfabric.RejectionReasonOutputSchemaMismatch)
	}
	if rig.store.count() != savedBefore {
		t.Fatal("a rejected draft was saved")
	}
}
