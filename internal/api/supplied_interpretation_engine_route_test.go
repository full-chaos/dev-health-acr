package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// interpretCountingRuntime is a model runtime with no model: it counts the
// interpret calls it receives and reports itself unavailable.
type interpretCountingRuntime struct{ interprets *int }

func (r interpretCountingRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	*r.interprets++
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

func (r interpretCountingRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

// fixedAnswerSynthesizer returns one fixed answer and records the
// interpretation it was asked to synthesize from.
type fixedAnswerSynthesizer struct {
	interpretations *[]contextfabric.InterpretedQuestion
}

func (s fixedAnswerSynthesizer) Synthesize(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	*s.interpretations = append(*s.interpretations, input.Interpretation)
	return contextfabric.InvestigationResult{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "Ask Dev is not ready to ship.",
		CurrentState: "Release-readiness blockers remain.", StrongestPressures: []string{},
		Drivers: []contextfabric.DriverJudgment{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{},
		Paths: []contextfabric.RelationshipPath{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, ClaimedFacts: []contextfabric.ClaimedFact{},
		Coverage:            contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Ask Dev is not ready to ship because release-readiness blockers remain.", Warnings: []string{},
		Versions: contextfabric.VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "schema-v1", SynthesisVersion: "synthesis-v1", ModelIdentity: suppliedRouteSynthesisIdentity,
		},
	}, nil
}

const suppliedRouteSynthesisIdentity = "test-provider/synthesis-model"

// authorizingGraph is liveGraphReader with the live subject authorizer a
// repository-restricted caller's committed roots are re-checked against
// before facts are read.
type authorizingGraph struct {
	liveGraphReader
	outcome contextfabric.StoredSubjectOutcome
}

// ResolveSubjects commits the project on a proven basis, so the commit stands
// on its own and the answer is not asked to affirm it.
func (g authorizingGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	bases := contextfabric.CommitBasisSet{}
	bases.Record(g.project, contextfabric.CommitBasisCallerCanonicalID)
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{g.project}},
		contextfabric.StructureOfferMaterial{}, bases, nil, nil
}

func (g authorizingGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range outcomes {
		outcomes[index] = g.outcome
	}
	return outcomes, nil
}

// scriptedInterpretRuntime is a model runtime that returns one fixed
// interpretation, for the model-path leg of a comparison.
type scriptedInterpretRuntime struct {
	interpreted contextfabric.InterpretedQuestion
	receipt     contextfabric.ModelExecutionReceipt
}

func (r scriptedInterpretRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return r.interpreted, r.receipt, nil
}

func (r scriptedInterpretRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

const suppliedRouteOutput = `{"shape":"single_subject","requested_judgment":"release readiness","subject_terms":["Ask Dev"],"time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"}],"clarification_needed":false}`

func suppliedRouteProject() contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
}

// newSuppliedRouteApp puts the real route in front of a real engine with the
// given interpreter and a graph whose live authorizer gives outcome for every
// committed root. The token it returns is restricted to one repository.
func newSuppliedRouteApp(t *testing.T, interpreter contextfabric.RuntimeQuestionInterpreter, outcome contextfabric.StoredSubjectOutcome, synthesized *[]contextfabric.InterpretedQuestion) (*App, string) {
	t.Helper()
	return newSuppliedRouteAppWithStore(t, interpreter, outcome, synthesized, memoryinvestigation.NewStore())
}

// newSuppliedRouteAppWithStore is newSuppliedRouteApp over the given result
// store.
func newSuppliedRouteAppWithStore(t *testing.T, interpreter contextfabric.RuntimeQuestionInterpreter, outcome contextfabric.StoredSubjectOutcome, synthesized *[]contextfabric.InterpretedQuestion, store contextfabric.InvestigationResultStore) (*App, string) {
	t.Helper()
	return newLiveContextFabricTestApp(t, newSuppliedRouteEngine(t, interpreter, outcome, synthesized, store))
}

// newSuppliedRouteEngine is the real engine newSuppliedRouteAppWithStore puts
// behind the route.
func newSuppliedRouteEngine(t *testing.T, interpreter contextfabric.RuntimeQuestionInterpreter, outcome contextfabric.StoredSubjectOutcome, synthesized *[]contextfabric.InterpretedQuestion, store contextfabric.InvestigationResultStore) *contextfabric.Engine {
	t.Helper()
	project := suppliedRouteProject()
	results := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: interpreter,
		Graph:       authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: outcome},
		Facts:       liveFactReader{bundle: liveCanonicalFacts(project)},
		Synthesizer: fixedAnswerSynthesizer{interpretations: synthesized},
		Results:     store,
	}, contextfabric.EngineOptions{
		ServiceVersion: "supplied-interpretation-route",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			results++
			return fmt.Sprintf("result_supplied_route%02d", results)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func windowedInvestigationHTTPRequest(t *testing.T, token string, supplied *contractsv1.ContextFabricSuppliedInterpretation) *http.Request {
	t.Helper()
	body := investigationRequestBody()
	body.SuppliedInterpretation = supplied
	body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	return request
}

// TestSuppliedInterpretationIsServedEndToEndWithNoInterpretCall posts to the
// real route in front of a real engine whose interpreter is the production
// one: the real supplied-interpretation runtime under the contract this
// binary runs, and a model runtime that can only count calls. A supplied
// interpretation is answered with zero interpret calls; the same request
// without one reaches the model; a wrong contract and a bad output are
// refused with their own status and never reach the model.
func TestSuppliedInterpretationIsServedEndToEndWithNoInterpretCall(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	project := suppliedRouteProject()
	interprets := 0
	var synthesized []contextfabric.InterpretedQuestion
	app, token := newSuppliedRouteApp(t, contextfabric.RuntimeQuestionInterpreter{Runtime: interpretCountingRuntime{interprets: &interprets}, Supplied: supplied}, contextfabric.StoredSubjectAdmitted, &synthesized)
	post := func(request *http.Request) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	interpretation := func(output string) contractsv1.ContextFabricSuppliedInterpretation {
		return contractsv1.ContextFabricSuppliedInterpretation{
			Output:             json.RawMessage(output),
			ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
			ClientModel: "claude-test",
		}
	}
	const output = suppliedRouteOutput

	windowed := func(supplied contractsv1.ContextFabricSuppliedInterpretation) *http.Request {
		return windowedInvestigationHTTPRequest(t, token, &supplied)
	}
	decode := func(recorder *httptest.ResponseRecorder) contractsv1.ContextFabricInvestigationResult {
		var result contractsv1.ContextFabricInvestigationResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("served result fails its own contract: %v", err)
		}
		return result
	}

	// With no evidence window the engine asks for one before it resolves
	// anything: a result with no synthesis, built from the supplied
	// interpretation alone.
	asked := post(suppliedInvestigationHTTPRequest(t, token, interpretation(output)))
	if asked.Code != http.StatusOK {
		t.Fatalf("supplied interpretation, no window: status = %d body=%s, want 200", asked.Code, asked.Body.String())
	}
	terminal := decode(asked)
	if terminal.Status != contractsv1.ContextFabricInvestigationClarificationRequired || terminal.WindowClarification == nil || len(synthesized) != 0 {
		t.Fatalf("no window: status = %q syntheses = %d, want the window clarification with no synthesis", terminal.Status, len(synthesized))
	}
	if got := terminal.Versions; got.InterpretationSource != contractsv1.ContextFabricInterpretationSourceClient ||
		got.InterpretationModelIdentity != "client-supplied/claude-test" || got.ModelIdentity != "client-supplied/claude-test" ||
		got.InterpretationVersion != contract.ModelOutputVersion {
		t.Fatalf("no window: Versions = %#v, want the client named as the interpreter of a result with no synthesis", got)
	}

	served := post(windowed(interpretation(output)))
	if served.Code != http.StatusOK {
		t.Fatalf("supplied interpretation: status = %d body=%s, want 200", served.Code, served.Body.String())
	}
	result := decode(served)
	if interprets != 0 {
		t.Fatalf("interpret calls = %d for two supplied interpretations, want 0", interprets)
	}
	if len(synthesized) != 1 || !reflect.DeepEqual(synthesized[0].SubjectTerms, []string{"Ask Dev"}) || synthesized[0].RequestedJudgment != "release readiness" {
		t.Fatalf("synthesis input interpretation = %#v, want the supplied one", synthesized)
	}
	versions := result.Versions
	if versions.InterpretationSource != contractsv1.ContextFabricInterpretationSourceClient || versions.InterpretationModelIdentity != "client-supplied/claude-test" {
		t.Fatalf("Versions = %#v, want the client named as the interpreter", versions)
	}
	if versions.ModelIdentity != suppliedRouteSynthesisIdentity {
		t.Fatalf("model_identity = %q on a synthesized answer, want the synthesis identity %q kept", versions.ModelIdentity, suppliedRouteSynthesisIdentity)
	}
	if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0] != project {
		t.Fatalf("committed subjects = %#v, want the resolved project", result.SubjectResolution.Committed)
	}

	plain := post(investigationRequest(t, token))
	if plain.Code != http.StatusServiceUnavailable || interprets != 1 {
		t.Fatalf("same request with no supplied interpretation: status = %d interpret calls = %d, want 503 after exactly one model interpret call", plain.Code, interprets)
	}

	stale := interpretation(output)
	stale.PromptVersion = "context-fabric-interpretation.v1"
	refused := post(windowed(stale))
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Contract contractsv1.ContextFabricInterpretationContractRefusal `json:"interpretation_contract"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(refused.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if refused.Code != http.StatusConflict || envelope.Error.Code != "invalid_request" {
		t.Fatalf("stale prompt version: status = %d body=%s, want a 409 invalid_request", refused.Code, refused.Body.String())
	}
	if !reflect.DeepEqual(envelope.Error.Details.Contract.Mismatch, []string{"prompt_version"}) || envelope.Error.Details.Contract.Current != contract {
		t.Fatalf("refusal = %#v, want the prompt_version mismatch and the service's own contract %#v", envelope.Error.Details.Contract, contract)
	}

	for name, bad := range map[string]string{
		"missing required fields": `{"shape":"open"}`,
		"unknown field":           `{"shape":"open","requested_judgment":"status","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":false,"subjects":["project_ask_dev"]}`,
		"empty judgment":          `{"shape":"open","requested_judgment":"","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":false}`,
	} {
		rejected := post(windowed(interpretation(bad)))
		if rejected.Code != http.StatusUnprocessableEntity || !containsJSONCode(rejected.Body.Bytes(), "interpretation_rejected") {
			t.Fatalf("%s: status = %d body=%s, want a 422 interpretation_rejected", name, rejected.Code, rejected.Body.String())
		}
	}
	if interprets != 1 || len(synthesized) != 1 {
		t.Fatalf("interpret calls = %d syntheses = %d after the refusals, want 1 and 1: a refused supplied interpretation reaches neither", interprets, len(synthesized))
	}
}

func containsJSONCode(body []byte, code string) bool {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Error.Code == code
}

// TestSuppliedInterpretationRootTheCallerCannotReadIsRefusedLikeTheModelPath
// runs the live fact-root re-check through the real route for a
// repository-restricted caller whose graph denies the committed project. The
// supplied path and the model path, given the same interpretation, serve the
// same result: no committed subject, no fact read, no synthesis. With the
// root admitted both paths synthesize, so the refusal is the authorizer's.
func TestSuppliedInterpretationRootTheCallerCannotReadIsRefusedLikeTheModelPath(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interpreted, err := genkitruntime.ParseInterpretationOutput([]byte(suppliedRouteOutput), contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	modelReceipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: contract.PromptVersion, SchemaVersion: contract.ModelOutputVersion, EvaluatorVersion: "eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1, InputDigest: strings.Repeat("a", 64), Outcome: "success",
	}
	suppliedField := &contractsv1.ContextFabricSuppliedInterpretation{
		Output: json.RawMessage(suppliedRouteOutput), ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
	}

	type served struct {
		result    contractsv1.ContextFabricInvestigationResult
		syntheses int
	}
	run := func(t *testing.T, interpreter contextfabric.RuntimeQuestionInterpreter, outcome contextfabric.StoredSubjectOutcome, field *contractsv1.ContextFabricSuppliedInterpretation) served {
		t.Helper()
		var synthesized []contextfabric.InterpretedQuestion
		app, token := newSuppliedRouteApp(t, interpreter, outcome, &synthesized)
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, windowedInvestigationHTTPRequest(t, token, field))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
		}
		var result contractsv1.ContextFabricInvestigationResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return served{result: result, syntheses: len(synthesized)}
	}
	interprets := 0
	suppliedInterpreter := contextfabric.RuntimeQuestionInterpreter{Runtime: interpretCountingRuntime{interprets: &interprets}, Supplied: supplied}
	modelInterpreter := contextfabric.RuntimeQuestionInterpreter{Runtime: scriptedInterpretRuntime{interpreted: interpreted, receipt: modelReceipt}}

	deniedSupplied := run(t, suppliedInterpreter, contextfabric.StoredSubjectDenied, suppliedField)
	deniedModel := run(t, modelInterpreter, contextfabric.StoredSubjectDenied, nil)
	for name, leg := range map[string]served{"supplied": deniedSupplied, "model": deniedModel} {
		if len(leg.result.SubjectResolution.Committed) != 0 || leg.syntheses != 0 {
			t.Fatalf("%s path, denied root: committed = %#v syntheses = %d, want no committed subject and no synthesis", name, leg.result.SubjectResolution.Committed, leg.syntheses)
		}
	}
	if deniedSupplied.result.Status != deniedModel.result.Status || !reflect.DeepEqual(deniedSupplied.result.Limitations, deniedModel.result.Limitations) ||
		!reflect.DeepEqual(deniedSupplied.result.SubjectResolution, deniedModel.result.SubjectResolution) {
		t.Fatalf("denied root: the two paths serve different refusals\nsupplied: %q %v %#v\nmodel:    %q %v %#v",
			deniedSupplied.result.Status, deniedSupplied.result.Limitations, deniedSupplied.result.SubjectResolution,
			deniedModel.result.Status, deniedModel.result.Limitations, deniedModel.result.SubjectResolution)
	}

	admittedSupplied := run(t, suppliedInterpreter, contextfabric.StoredSubjectAdmitted, suppliedField)
	admittedModel := run(t, modelInterpreter, contextfabric.StoredSubjectAdmitted, nil)
	for name, leg := range map[string]served{"supplied": admittedSupplied, "model": admittedModel} {
		if len(leg.result.SubjectResolution.Committed) != 1 || leg.syntheses != 1 {
			t.Fatalf("%s path, admitted root: committed = %#v syntheses = %d, want the project committed and one synthesis", name, leg.result.SubjectResolution.Committed, leg.syntheses)
		}
	}
	if interprets != 0 {
		t.Fatalf("interpret calls on the supplied path = %d, want 0", interprets)
	}
}

// TestSuppliedInterpretationFollowUpTurnIsDecidedLikeTheModelPath runs two
// turns through the real route on both paths: the first asks, the second
// sends the offered window receipt back. Each turn is decided the same on
// both paths, and the supplied path makes no interpret call on either turn.
func TestSuppliedInterpretationFollowUpTurnIsDecidedLikeTheModelPath(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interpreted, err := genkitruntime.ParseInterpretationOutput([]byte(suppliedRouteOutput), contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	modelReceipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: contract.PromptVersion, SchemaVersion: contract.ModelOutputVersion, EvaluatorVersion: "eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1, InputDigest: strings.Repeat("a", 64), Outcome: "success",
	}
	suppliedField := &contractsv1.ContextFabricSuppliedInterpretation{
		Output: json.RawMessage(suppliedRouteOutput), ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
	}

	type turn struct {
		code     int
		status   contractsv1.ContextFabricInvestigationStatus
		refusal  contractsv1.ContextFabricRefusalBasis
		windowed bool
		offers   int
		limits   []string
		source   contractsv1.ContextFabricInterpretationSource
	}
	twoTurns := func(t *testing.T, interpreter contextfabric.RuntimeQuestionInterpreter, field *contractsv1.ContextFabricSuppliedInterpretation) [2]turn {
		t.Helper()
		var synthesized []contextfabric.InterpretedQuestion
		app, token := newSuppliedRouteApp(t, interpreter, contextfabric.StoredSubjectAdmitted, &synthesized)
		send := func(body contractsv1.ContextFabricInvestigationRequest) (turn, contractsv1.ContextFabricInvestigationResult) {
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
			var result contractsv1.ContextFabricInvestigationResult
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatalf("status = %d body=%s: %v", recorder.Code, recorder.Body.String(), err)
			}
			offers := 0
			if result.WindowClarification != nil {
				offers = len(result.WindowClarification.Options)
			}
			return turn{
				code: recorder.Code, status: result.Status, refusal: result.RefusalBasis, windowed: result.EffectiveEvidenceWindow != nil,
				offers: offers, limits: result.Limitations, source: result.Versions.InterpretationSource,
			}, result
		}
		first := investigationRequestBody()
		first.SuppliedInterpretation = field
		firstTurn, asked := send(first)
		if firstTurn.code != http.StatusOK || firstTurn.offers == 0 {
			t.Fatalf("first turn = %#v, want a window clarification with an option to confirm", firstTurn)
		}
		second := investigationRequestBody()
		second.SuppliedInterpretation = field
		second.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: asked.ResultID, ReceiptID: asked.WindowClarification.Options[0].ReceiptID}}
		secondTurn, _ := send(second)
		return [2]turn{firstTurn, secondTurn}
	}

	interprets := 0
	suppliedTurns := twoTurns(t, contextfabric.RuntimeQuestionInterpreter{Runtime: interpretCountingRuntime{interprets: &interprets}, Supplied: supplied}, suppliedField)
	modelTurns := twoTurns(t, contextfabric.RuntimeQuestionInterpreter{Runtime: scriptedInterpretRuntime{interpreted: interpreted, receipt: modelReceipt}}, nil)
	if interprets != 0 {
		t.Fatalf("interpret calls on the supplied path = %d over two turns, want 0", interprets)
	}
	for index := range suppliedTurns {
		got, want := suppliedTurns[index], modelTurns[index]
		if got.source != contractsv1.ContextFabricInterpretationSourceClient || want.source != contractsv1.ContextFabricInterpretationSourceServer {
			t.Fatalf("turn %d: interpretation_source = %q (supplied) and %q (model), want client and server", index+1, got.source, want.source)
		}
		got.source, want.source = "", ""
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("turn %d is decided differently on the two paths\nsupplied: %#v\nmodel:    %#v", index+1, got, want)
		}
	}
	if suppliedTurns[0].status == suppliedTurns[1].status && suppliedTurns[0].refusal == suppliedTurns[1].refusal {
		t.Fatalf("both turns read %q/%q: the follow-up turn did not take the receipt path", suppliedTurns[1].status, suppliedTurns[1].refusal)
	}
}

// TestSuppliedInterpretationMissingAContractValueIsRefusedWithTheCurrentContract
// posts a valid supplied interpretation through the real route and the real
// engine with one contract value left out, sent as null, or sent empty. Each
// is refused 409 with the field named and the contract the service runs, and
// reaches neither the model nor synthesis. The complete contract is served,
// so the refusal is the missing value's.
func TestSuppliedInterpretationMissingAContractValueIsRefusedWithTheCurrentContract(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interprets := 0
	var synthesized []contextfabric.InterpretedQuestion
	app, token := newSuppliedRouteApp(t, contextfabric.RuntimeQuestionInterpreter{Runtime: interpretCountingRuntime{interprets: &interprets}, Supplied: supplied}, contextfabric.StoredSubjectAdmitted, &synthesized)

	post := func(t *testing.T, edit func(field map[string]any)) *httptest.ResponseRecorder {
		t.Helper()
		body := investigationRequestBody()
		body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		field := map[string]any{
			"output":               json.RawMessage(suppliedRouteOutput),
			"model_output_version": contract.ModelOutputVersion,
			"prompt_version":       contract.PromptVersion,
			"system_sha256":        contract.SystemSHA256,
		}
		edit(field)
		document["supplied_interpretation"] = field
		encoded, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	forms := map[string]func(field map[string]any, name string){
		"absent": func(field map[string]any, name string) { delete(field, name) },
		"null":   func(field map[string]any, name string) { field[name] = nil },
		"empty":  func(field map[string]any, name string) { field[name] = "" },
	}
	for _, name := range []string{"model_output_version", "prompt_version", "system_sha256"} {
		for form, apply := range forms {
			t.Run(name+" "+form, func(t *testing.T) {
				refused := post(t, func(field map[string]any) { apply(field, name) })
				var envelope struct {
					Error struct {
						Code    string `json:"code"`
						Details struct {
							Contract contractsv1.ContextFabricInterpretationContractRefusal `json:"interpretation_contract"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(refused.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("status = %d body=%s: %v", refused.Code, refused.Body.String(), err)
				}
				if refused.Code != http.StatusConflict || envelope.Error.Code != "invalid_request" {
					t.Fatalf("status = %d code = %q, want a 409 invalid_request", refused.Code, envelope.Error.Code)
				}
				if !reflect.DeepEqual(envelope.Error.Details.Contract.Mismatch, []string{name}) || envelope.Error.Details.Contract.Current != contract {
					t.Fatalf("refusal = %#v, want %s named and the service's own contract %#v", envelope.Error.Details.Contract, name, contract)
				}
			})
		}
	}
	if interprets != 0 || len(synthesized) != 0 {
		t.Fatalf("interpret calls = %d syntheses = %d after the refusals, want 0 and 0", interprets, len(synthesized))
	}

	served := post(t, func(map[string]any) {})
	if served.Code != http.StatusOK || interprets != 0 || len(synthesized) != 1 {
		t.Fatalf("complete contract: status = %d interpret calls = %d syntheses = %d body=%s, want 200 with one synthesis and no interpret call", served.Code, interprets, len(synthesized), served.Body.String())
	}
}

// countingScriptedRuntime is scriptedInterpretRuntime that counts its
// interpret calls.
type countingScriptedRuntime struct {
	scriptedInterpretRuntime
	interprets *int
}

func (r countingScriptedRuntime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	*r.interprets++
	return r.scriptedInterpretRuntime.InterpretQuestion(ctx, principal, request)
}

// TestSuppliedInterpretationFollowUpTurnIsServedLikeTheModelPath runs a
// served continuation through the real route. The first turn asks for the
// evidence window; the second sends the offered receipt back and is answered
// under that window. The result store carries the graph epoch, so the engine
// can prove the offering turn and carries it. Four legs cover both turns
// supplied, both turns interpreted by the model, and the two mixed orders, on
// one interpreter that holds both paths. Every leg serves the same second
// turn, each turn names its own interpreter, and a supplied turn makes no
// model interpret call.
func TestSuppliedInterpretationFollowUpTurnIsServedLikeTheModelPath(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interpreted, err := genkitruntime.ParseInterpretationOutput([]byte(suppliedRouteOutput), contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	modelReceipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: contract.PromptVersion, SchemaVersion: contract.ModelOutputVersion, EvaluatorVersion: "eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1, InputDigest: strings.Repeat("a", 64), Outcome: "success",
	}
	suppliedField := &contractsv1.ContextFabricSuppliedInterpretation{
		Output: json.RawMessage(suppliedRouteOutput), ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
		ClientModel: "claude-test",
	}
	const clientIdentity, serverIdentity = "client-supplied/claude-test", "test-provider/test-model"

	type servedTurn struct {
		status    contractsv1.ContextFabricInvestigationStatus
		refusal   contractsv1.ContextFabricRefusalBasis
		window    *contractsv1.ContextFabricEffectiveEvidenceWindow
		limits    []string
		committed []contractsv1.ContextFabricSubjectRef
		answer    string
	}
	run := func(t *testing.T, firstSupplied, secondSupplied bool) servedTurn {
		t.Helper()
		interprets := 0
		var synthesized []contextfabric.InterpretedQuestion
		interpreter := contextfabric.RuntimeQuestionInterpreter{
			Runtime:  countingScriptedRuntime{scriptedInterpretRuntime: scriptedInterpretRuntime{interpreted: interpreted, receipt: modelReceipt}, interprets: &interprets},
			Supplied: supplied,
		}
		app, token := newSuppliedRouteAppWithStore(t, interpreter, contextfabric.StoredSubjectAdmitted, &synthesized, rankingSurfaceStore{memoryinvestigation.NewStore()})
		send := func(body contractsv1.ContextFabricInvestigationRequest, suppliedTurn bool) contractsv1.ContextFabricInvestigationResult {
			t.Helper()
			if suppliedTurn {
				body.SuppliedInterpretation = suppliedField
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
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
			}
			var result contractsv1.ContextFabricInvestigationResult
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("served result fails its own contract: %v", err)
			}
			wantSource, wantIdentity := contractsv1.ContextFabricInterpretationSourceServer, serverIdentity
			if suppliedTurn {
				wantSource, wantIdentity = contractsv1.ContextFabricInterpretationSourceClient, clientIdentity
			}
			if got := result.Versions; got.InterpretationSource != wantSource || got.InterpretationModelIdentity != wantIdentity {
				t.Fatalf("interpretation_source = %q interpretation_model_identity = %q, want %q and %q", got.InterpretationSource, got.InterpretationModelIdentity, wantSource, wantIdentity)
			}
			return result
		}

		asked := send(investigationRequestBody(), firstSupplied)
		if asked.Status != contractsv1.ContextFabricInvestigationClarificationRequired || asked.WindowClarification == nil || len(asked.WindowClarification.Options) == 0 || len(synthesized) != 0 {
			t.Fatalf("first turn: status = %q syntheses = %d, want a window clarification with an option and no synthesis", asked.Status, len(synthesized))
		}
		followUp := investigationRequestBody()
		followUp.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: asked.ResultID, ReceiptID: asked.WindowClarification.Options[0].ReceiptID}}
		served := send(followUp, secondSupplied)
		if served.Status != contractsv1.ContextFabricInvestigationComplete || served.EffectiveEvidenceWindow == nil || len(synthesized) != 1 {
			t.Fatalf("second turn: status = %q refusal = %q window = %v syntheses = %d limitations = %v, want the answer served under the confirmed window with one synthesis",
				served.Status, served.RefusalBasis, served.EffectiveEvidenceWindow, len(synthesized), served.Limitations)
		}
		if served.EffectiveEvidenceWindow.Provenance != contractsv1.ContextFabricWindowClarificationConfirmed {
			t.Fatalf("second turn: window provenance = %q, want the window the receipt confirmed", served.EffectiveEvidenceWindow.Provenance)
		}
		if !reflect.DeepEqual(synthesized[0].SubjectTerms, []string{"Ask Dev"}) {
			t.Fatalf("synthesis input interpretation = %#v, want the interpretation of the follow-up turn", synthesized[0])
		}
		wantInterprets := 0
		for _, suppliedTurn := range []bool{firstSupplied, secondSupplied} {
			if !suppliedTurn {
				wantInterprets++
			}
		}
		if interprets != wantInterprets {
			t.Fatalf("model interpret calls = %d, want %d: one for each turn the service interprets and none for a supplied turn", interprets, wantInterprets)
		}
		return servedTurn{
			status: served.Status, refusal: served.RefusalBasis, window: served.EffectiveEvidenceWindow, limits: served.Limitations,
			committed: served.SubjectResolution.Committed, answer: served.DeterministicAnswer,
		}
	}

	model := run(t, false, false)
	for name, leg := range map[string][2]bool{
		"client then client": {true, true},
		"client then server": {true, false},
		"server then client": {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(t, leg[0], leg[1]); !reflect.DeepEqual(got, model) {
				t.Fatalf("the served follow-up differs from the model path\ngot:   %#v\nmodel: %#v", got, model)
			}
		})
	}
}

// TestSuppliedInterpretationContractIsRefusedOnATurnThatEndsBeforeInterpretation
// posts a supplied interpretation on requests the engine ends before its
// interpret step: one names a prior window receipt of a result that does not
// exist, one a prior kind receipt of such a result. With a stale or missing
// contract value each is refused 409 with the current contract. With the
// service's own contract the same requests are served the engine's own
// refusal of the receipt, so the 409 is the contract's and not the receipt's.
func TestSuppliedInterpretationContractIsRefusedOnATurnThatEndsBeforeInterpretation(t *testing.T) {
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	contract := supplied.Contract()
	interprets := 0
	var synthesized []contextfabric.InterpretedQuestion
	app, token := newSuppliedRouteApp(t, contextfabric.RuntimeQuestionInterpreter{Runtime: interpretCountingRuntime{interprets: &interprets}, Supplied: supplied}, contextfabric.StoredSubjectAdmitted, &synthesized)

	exits := map[string]func(*contractsv1.ContextFabricInvestigationRequest){
		"unresolved window receipt": func(r *contractsv1.ContextFabricInvestigationRequest) {
			r.PriorWindowReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
		},
		"unresolved kind receipt": func(r *contractsv1.ContextFabricInvestigationRequest) {
			r.PriorKindReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "kindr_confirm0001"}}
		},
	}
	contracts := map[string]func(*contractsv1.ContextFabricSuppliedInterpretation) string{
		"stale prompt version": func(s *contractsv1.ContextFabricSuppliedInterpretation) string {
			s.PromptVersion = "context-fabric-interpretation.v1"
			return "prompt_version"
		},
		"stale model output version": func(s *contractsv1.ContextFabricSuppliedInterpretation) string {
			s.ModelOutputVersion = "context-fabric-model-output.v1"
			return "model_output_version"
		},
		"digest of another prompt": func(s *contractsv1.ContextFabricSuppliedInterpretation) string {
			s.SystemSHA256 = strings.Repeat("a", 64)
			return "system_sha256"
		},
		"digest absent": func(s *contractsv1.ContextFabricSuppliedInterpretation) string {
			s.SystemSHA256 = ""
			return "system_sha256"
		},
	}
	post := func(t *testing.T, exit func(*contractsv1.ContextFabricInvestigationRequest), field contractsv1.ContextFabricSuppliedInterpretation) *httptest.ResponseRecorder {
		t.Helper()
		body := investigationRequestBody()
		body.SuppliedInterpretation = &field
		exit(&body)
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
		return recorder
	}
	current := func() contractsv1.ContextFabricSuppliedInterpretation {
		return contractsv1.ContextFabricSuppliedInterpretation{
			Output:             json.RawMessage(suppliedRouteOutput),
			ModelOutputVersion: contract.ModelOutputVersion, PromptVersion: contract.PromptVersion, SystemSHA256: contract.SystemSHA256,
		}
	}

	for exitName, exit := range exits {
		control := post(t, exit, current())
		var served contractsv1.ContextFabricInvestigationResult
		if err := json.Unmarshal(control.Body.Bytes(), &served); err != nil {
			t.Fatalf("%s, the service's own contract: status = %d body=%s: %v", exitName, control.Code, control.Body.String(), err)
		}
		if control.Code != http.StatusOK || served.Status != contractsv1.ContextFabricInvestigationNoMatch || served.Versions.InterpretationSource != "" {
			t.Fatalf("%s, the service's own contract: status = %d result status = %q interpretation_source = %q, want the engine's own refusal of the receipt with no interpretation",
				exitName, control.Code, served.Status, served.Versions.InterpretationSource)
		}
		for contractName, mutate := range contracts {
			t.Run(exitName+", "+contractName, func(t *testing.T) {
				field := current()
				wantField := mutate(&field)
				refused := post(t, exit, field)
				var envelope struct {
					Error struct {
						Code    string `json:"code"`
						Details struct {
							Contract contractsv1.ContextFabricInterpretationContractRefusal `json:"interpretation_contract"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(refused.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("status = %d body=%s: %v", refused.Code, refused.Body.String(), err)
				}
				if refused.Code != http.StatusConflict || envelope.Error.Code != "invalid_request" {
					t.Fatalf("status = %d code = %q body=%s, want a 409 invalid_request", refused.Code, envelope.Error.Code, refused.Body.String())
				}
				if !reflect.DeepEqual(envelope.Error.Details.Contract.Mismatch, []string{wantField}) || envelope.Error.Details.Contract.Current != contract {
					t.Fatalf("refusal = %#v, want %s named and the service's own contract %#v", envelope.Error.Details.Contract, wantField, contract)
				}
			})
		}
	}
	if interprets != 0 || len(synthesized) != 0 {
		t.Fatalf("interpret calls = %d syntheses = %d, want 0 and 0: every one of these turns ends before interpretation", interprets, len(synthesized))
	}
}
