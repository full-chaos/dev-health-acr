package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// clientRouteModel is the model runtime behind the route: it interprets one
// fixed question, records every synthesize call and returns a valid draft.
type clientRouteModel struct {
	mu          sync.Mutex
	interpreted contextfabric.InterpretedQuestion
	interprets  int
	synthesized []contextfabric.SynthesisInput
}

func (m *clientRouteModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interprets++
	return m.interpreted, clientRouteReceipt(contextfabric.ModelOperationInterpret), nil
}

func (m *clientRouteModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synthesized = append(m.synthesized, input)
	return contextfabric.SynthesisDraft{
		Status: contextfabric.InvestigationPartial, DirectJudgment: "Ask Dev appears not ready to ship.", CurrentState: "Readiness appears to be open.",
		StrongestPressures: []string{}, Drivers: []contextfabric.DriverJudgment{}, RemainingWork: []contextfabric.Finding{},
		ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, ClaimedFacts: []contextfabric.ClaimedFact{},
		DeterministicAnswer: "model prose placeholder", Warnings: []string{},
	}, clientRouteReceipt(contextfabric.ModelOperationSynthesize), nil
}

func (m *clientRouteModel) counts() (interprets, synthesizes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interprets, len(m.synthesized)
}

func clientRouteReceipt(operation contextfabric.ModelOperation) contextfabric.ModelExecutionReceipt {
	at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	return contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: "prompt-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1, InputDigest: strings.Repeat("a", 64), OutputDigest: strings.Repeat("b", 64), Outcome: "success",
	}
}

// clientRouteGraph commits the project on a proven basis, or answers a cohort
// question with a discovered cohort of teams.
type clientRouteGraph struct {
	authorizingGraph
	cohort *contextfabric.Cohort
}

func (g clientRouteGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, binding contextfabric.ResolvedGraphBinding, kind *contextfabric.ConfirmedExpectedKind, anchor *contextfabric.ConfirmedAnchorSelection, frame *contextfabric.QuestionFrame, inferred contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	if g.cohort != nil {
		return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
	}
	return g.authorizingGraph.ResolveSubjects(ctx, principal, request, interpreted, binding, kind, anchor, frame, inferred)
}

func (g clientRouteGraph) DiscoverContext(ctx context.Context, principal storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	resolution := contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{g.project}}
	if g.cohort != nil {
		resolution.Committed = []contextfabric.SubjectRef{}
	}
	return contextfabric.GraphContext{
		Resolution: resolution, Cohort: g.cohort, Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type clientRouteFixture struct {
	interpreted contextfabric.InterpretedQuestion
	cohort      *contextfabric.Cohort
	facts       contextfabric.CanonicalFactBundle
	// assembly overrides the production assembly; set noAssembly for none.
	noAssembly bool
	resources  limits.ResourceBudget
}

type clientRouteRig struct {
	app    *App
	token  string
	model  *clientRouteModel
	store  *memoryinvestigation.Store
	logs   *bytes.Buffer
	nextID int
}

func singleSubjectFixture() clientRouteFixture {
	project := suppliedRouteProject()
	return clientRouteFixture{
		interpreted: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "release readiness", SubjectTerms: []string{"Ask Dev"},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		facts: liveCanonicalFacts(project),
	}
}

func newClientRouteRig(t *testing.T, fixture clientRouteFixture) *clientRouteRig {
	t.Helper()
	project := suppliedRouteProject()
	model := &clientRouteModel{interpreted: fixture.interpreted}
	store := memoryinvestigation.NewStore()
	rig := &clientRouteRig{model: model, store: store, logs: &bytes.Buffer{}}
	synthesizer := contextfabric.RuntimeAnswerSynthesizer{
		Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "client-synthesis-route", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
	}
	if !fixture.noAssembly {
		synthesizer.ClientSynthesis = synthesisprompt.ClientAssembly()
	}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model},
		Graph:       clientRouteGraph{authorizingGraph: authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: contextfabric.StoredSubjectAdmitted}, cohort: fixture.cohort},
		Facts:       liveFactReader{bundle: fixture.facts},
		Synthesizer: synthesizer,
		Results:     store,
	}, contextfabric.EngineOptions{
		ServiceVersion: "client-synthesis-route",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			rig.nextID++
			return fmt.Sprintf("result_client_route%02d", rig.nextID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resources := fixture.resources
	if resources == (limits.ResourceBudget{}) {
		resources = limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}
	}
	rig.app, rig.token = newParityHostedAppWithLogs(t, engine, store, resources, rig.logs)
	return rig
}

// post sends a windowed investigation request; mode and edit shape it.
func (r *clientRouteRig) post(t *testing.T, mode contractsv1.ContextFabricSynthesisMode, edit func(*contractsv1.ContextFabricInvestigationRequest)) *httptest.ResponseRecorder {
	t.Helper()
	body := investigationRequestBody()
	body.SynthesisMode = mode
	body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	if edit != nil {
		edit(&body)
	}
	return r.postBody(t, body)
}

func (r *clientRouteRig) postBody(t *testing.T, body contractsv1.ContextFabricInvestigationRequest) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return r.postRaw(encoded)
}

func (r *clientRouteRig) postRaw(encoded []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func (r *clientRouteRig) get(resultID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, ContextFabricInvestigationsPath+"/"+resultID, nil)
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func decodeEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) contractsv1.ContextFabricInvestigationResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope contractsv1.ContextFabricInvestigationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("served response fails its own contract: %v", err)
	}
	return envelope
}

func topLevelKeys(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

func systemSHA(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(synthesisprompt.System()))
	return hex.EncodeToString(sum[:])
}

// A client turn through the real route: no synthesize call, a partial result
// with no model-written content, and a synthesis input that is, byte for byte,
// what the same question sent to the service's own model in server mode.
func TestClientSynthesisTurnReturnsTheExactInputTheServerModelIsGiven(t *testing.T) {
	client := newClientRouteRig(t, singleSubjectFixture())
	envelope := decodeEnvelope(t, client.post(t, contractsv1.ContextFabricSynthesisModeClient, nil))
	if _, synthesizes := client.model.counts(); synthesizes != 0 {
		t.Fatalf("synthesize calls = %d on a client turn, want 0", synthesizes)
	}
	if envelope.Status != contractsv1.ContextFabricInvestigationPartial {
		t.Fatalf("status = %q, want partial", envelope.Status)
	}
	if got := envelope.Versions; got.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient ||
		got.SynthesisVersion != contextfabric.SynthesisVersionNotSynthesized || got.ModelIdentity != "unwired" {
		t.Fatalf("versions = source %q synthesis %q model %q, want client, not_synthesized, unwired", got.SynthesisSource, got.SynthesisVersion, got.ModelIdentity)
	}
	bundle := envelope.SynthesisInput
	if bundle == nil {
		t.Fatal("the response carries no synthesis_input")
	}
	if want := (contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: synthesisprompt.OutputVersion, PromptVersion: synthesisprompt.PromptVersion, SystemSHA256: systemSHA(t)}); bundle.Contract != want {
		t.Fatalf("contract = %#v, want %#v", bundle.Contract, want)
	}
	if !reflect.DeepEqual(bundle.Rules, synthesisprompt.ClientRules()) {
		t.Fatalf("rules = %q, want the client rules", bundle.Rules)
	}

	server := newClientRouteRig(t, singleSubjectFixture())
	serverEnvelope := decodeEnvelope(t, server.post(t, "", nil))
	if _, synthesizes := server.model.counts(); synthesizes != 1 {
		t.Fatalf("server synthesize calls = %d, want 1", synthesizes)
	}
	given := server.model.synthesized[0]
	want, err := synthesisprompt.UserPayload(callerOrgID, given, contractsv1.ContextFabricSynthesisInputDefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.Input) != string(want) {
		t.Fatalf("client input differs from the server model input\nclient: %s\nserver: %s", bundle.Input, want)
	}
	sum := sha256.Sum256(want)
	if bundle.InputSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("input_sha256 = %s, want the sha256 of the server model input", bundle.InputSHA256)
	}
	if serverEnvelope.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceServer {
		t.Fatalf("server turn synthesis_source = %q, want server", serverEnvelope.Versions.SynthesisSource)
	}
}

func TestServerModeResponseCarriesNoSynthesisInputKey(t *testing.T) {
	for name, mode := range map[string]contractsv1.ContextFabricSynthesisMode{"absent": "", "server": contractsv1.ContextFabricSynthesisModeServer} {
		t.Run(name, func(t *testing.T) {
			rig := newClientRouteRig(t, singleSubjectFixture())
			recorder := rig.post(t, mode, nil)
			envelope := decodeEnvelope(t, recorder)
			if _, present := topLevelKeys(t, recorder.Body.Bytes())["synthesis_input"]; present {
				t.Fatal("a server turn carries a synthesis_input key")
			}
			if _, synthesizes := rig.model.counts(); synthesizes != 1 {
				t.Fatalf("synthesize calls = %d, want 1", synthesizes)
			}
			if envelope.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceServer {
				t.Fatalf("synthesis_source = %q, want server", envelope.Versions.SynthesisSource)
			}
		})
	}
}

func TestClientSynthesisResultReadByIDCarriesNoSynthesisInput(t *testing.T) {
	rig := newClientRouteRig(t, singleSubjectFixture())
	served := decodeEnvelope(t, rig.post(t, contractsv1.ContextFabricSynthesisModeClient, nil))
	if served.SynthesisInput == nil {
		t.Fatal("the client turn returned no synthesis input")
	}
	recorder := rig.get(served.ResultID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("read by id: status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, present := topLevelKeys(t, recorder.Body.Bytes())["synthesis_input"]; present {
		t.Fatal("read by id returned a synthesis_input: the input is served only in the turn that built it")
	}
	var stored contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient || stored.Versions.SynthesisVersion != contextfabric.SynthesisVersionNotSynthesized {
		t.Fatalf("stored versions = source %q synthesis %q, want client and not_synthesized", stored.Versions.SynthesisSource, stored.Versions.SynthesisVersion)
	}
}

// A row stored before the synthesis source existed names the service as its
// writer when it records a real synthesis version, and names nothing when the
// turn ended before synthesis or served no model-written text.
func TestAStoredResultReadByIDNamesItsSynthesisSource(t *testing.T) {
	rig := newClientRouteRig(t, singleSubjectFixture())
	written := decodeEnvelope(t, rig.post(t, "", nil))
	stored, err := rig.store.Get(context.Background(), seedPrincipal(callerOrgID), written.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	realVersion := stored.Result.Versions.SynthesisVersion
	if realVersion == "" || realVersion == "unwired" || realVersion == contextfabric.SynthesisVersionNotSynthesized {
		t.Fatalf("the service's own turn stored synthesis_version %q, want a real version", realVersion)
	}
	seed := func(id, version string, source contractsv1.ContextFabricSynthesisSource) {
		row := stored.Result
		row.ResultID = id
		row.Versions.SynthesisVersion = version
		row.Versions.SynthesisSource = source
		saveSurfaceRow(t, rig.store, row, contextfabric.SemanticStateOf(stored.SemanticState))
	}
	seed("result_legacy_real_version", realVersion, "")
	seed("result_legacy_unwired", "unwired", "")
	seed("result_legacy_not_synthesized", contextfabric.SynthesisVersionNotSynthesized, "")
	seed("result_names_client", contextfabric.SynthesisVersionNotSynthesized, contractsv1.ContextFabricSynthesisSourceClient)

	for _, row := range []struct {
		id         string
		want       contractsv1.ContextFabricSynthesisSource
		backfilled bool
	}{
		{"result_legacy_real_version", contractsv1.ContextFabricSynthesisSourceServer, true},
		{"result_legacy_unwired", "", false},
		{"result_legacy_not_synthesized", "", false},
		{"result_names_client", contractsv1.ContextFabricSynthesisSourceClient, false},
		{written.ResultID, contractsv1.ContextFabricSynthesisSourceServer, false},
	} {
		offset := rig.logs.Len()
		recorder := rig.get(row.id)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", row.id, recorder.Code, recorder.Body.String())
		}
		var served contractsv1.ContextFabricInvestigationResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
			t.Fatal(err)
		}
		if served.Versions.SynthesisSource != row.want {
			t.Errorf("%s: synthesis_source = %q, want %q", row.id, served.Versions.SynthesisSource, row.want)
		}
		lines := 0
		for _, raw := range strings.Split(strings.TrimSpace(rig.logs.String()[offset:]), "\n") {
			var line map[string]any
			if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == storedSynthesisSourceBackfilledLogMessage {
				if line["level"] != "INFO" || line["synthesis_source"] != string(contractsv1.ContextFabricSynthesisSourceServer) {
					t.Fatalf("%s: backfill line = %v, want an Info line that names server", row.id, line)
				}
				lines++
			}
		}
		if (lines == 1) != row.backfilled || lines > 1 {
			t.Errorf("%s: backfill lines = %d, want backfilled = %v", row.id, lines, row.backfilled)
		}
	}
}

func TestAnInvalidSynthesisModeIsRefusedBeforeTheEngineRuns(t *testing.T) {
	for _, mode := range []string{"x", "Client", "SERVER", "client ", "synthesis"} {
		rig := newClientRouteRig(t, singleSubjectFixture())
		body, err := json.Marshal(investigationRequestBody())
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		fields["synthesis_mode"], _ = json.Marshal(mode)
		encoded, _ := json.Marshal(fields)
		recorder := rig.postRaw(encoded)
		if recorder.Code != http.StatusBadRequest || !containsJSONCode(recorder.Body.Bytes(), "invalid_request") {
			t.Fatalf("synthesis_mode %q: status = %d body=%s, want 400 invalid_request", mode, recorder.Code, recorder.Body.String())
		}
		if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
			t.Fatalf("synthesis_mode %q: model calls = %d interpret, %d synthesize, want none", mode, interprets, synthesizes)
		}
	}
}

func TestClientSynthesisOnADeploymentWithoutTheAssemblyIsRefusedAndNothingIsSaved(t *testing.T) {
	fixture := singleSubjectFixture()
	fixture.noAssembly = true
	rig := newClientRouteRig(t, fixture)
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeClient, nil)
	if recorder.Code != http.StatusBadRequest || !containsJSONCode(recorder.Body.Bytes(), "invalid_request") {
		t.Fatalf("status = %d body=%s, want 400 invalid_request", recorder.Code, recorder.Body.String())
	}
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = %d interpret, %d synthesize, want none: the turn is refused before any work", interprets, synthesizes)
	}
	if _, err := rig.store.Get(context.Background(), seedPrincipal(callerOrgID), "result_client_route01"); err == nil {
		t.Fatal("a refused client turn saved a result")
	}
	if !strings.Contains(rig.logs.String(), `"failure_classification":"client_synthesis_unavailable"`) {
		t.Fatalf("the refusal is not classified in the log: %s", rig.logs.String())
	}
}

// manyFactsFixture reads n status facts of about size bytes each, one per work
// item beside the committed project (a fact about a committed subject is never
// cut by the input bound), enough to push the synthesis input past it.
func manyFactsFixture(n, size int) clientRouteFixture {
	fixture := singleSubjectFixture()
	facts := make([]contextfabric.CanonicalFact, 0, n)
	for index := range n {
		item := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item_%04d", index), Label: fmt.Sprintf("Work item %04d", index)}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactStatus, Subject: item,
			Fields: map[string]contextfabric.FactValue{
				"status": contextfabric.StringFactValue(fmt.Sprintf("in_progress_%03d", index)),
				"note":   contextfabric.StringFactValue(strings.Repeat(fmt.Sprintf("n%03d ", index), size/5)),
			},
			EvidenceRefIDs: []string{fmt.Sprintf("evidence_status_%04d", index)}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	fixture.facts = contextfabric.CanonicalFactBundle{
		Facts: facts, Version: "ops-v1",
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{{Source: "canonical_fact:status", State: contextfabric.SourceAvailable}}, DegradedReasons: []string{}},
	}
	return fixture
}

// cohortFixture answers "which teams" with n discovered teams and one health
// fact for each.
func cohortFixture(n int) clientRouteFixture {
	cohort := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Rationale: "kind census match", Members: make([]contextfabric.CohortMember, 0, n)}
	facts := make([]contextfabric.CanonicalFact, 0, n)
	for index := range n {
		team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: fmt.Sprintf("team:T%03d", index), Label: fmt.Sprintf("Team %03d", index)}
		cohort.Members = append(cohort.Members, contextfabric.CohortMember{Subject: team, Rank: index + 1, InclusionReasons: []string{"matched the team census"}, EvidenceRefIDs: []string{fmt.Sprintf("evidence_team_%03d", index)}})
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: team, Fields: map[string]contextfabric.FactValue{"severity": contextfabric.StringFactValue("high")},
			EvidenceRefIDs: []string{fmt.Sprintf("evidence_health_%03d", index)}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	return clientRouteFixture{
		interpreted: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}},
		},
		cohort: cohort,
		facts: contextfabric.CanonicalFactBundle{
			Facts: facts, Version: "ops-v1",
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{{Source: "canonical_fact:health", State: contextfabric.SourceAvailable}}, DegradedReasons: []string{}},
		},
	}
}

// The result alone is measured against the caller's byte budget, as in server
// mode. The synthesis input has its own bound and is not counted.
func TestClientSynthesisInputIsNotCountedInTheResultByteBudget(t *testing.T) {
	fixture := manyFactsFixture(40, 600)
	probe := newClientRouteRig(t, fixture)
	recorder := probe.post(t, contractsv1.ContextFabricSynthesisModeClient, nil)
	envelope := decodeEnvelope(t, recorder)
	resultBytes, err := json.Marshal(envelope.ContextFabricInvestigationResult)
	if err != nil {
		t.Fatal(err)
	}
	limit := contractsv1.ContextFabricSerializedBytesMin
	if len(resultBytes) > limit || recorder.Body.Len() <= limit {
		t.Fatalf("result_bytes=%d response_bytes=%d: the fixture must fit the result in %d bytes and not the result plus the input", len(resultBytes), recorder.Body.Len(), limit)
	}
	withLimit := func(limit int) func(*contractsv1.ContextFabricInvestigationRequest) {
		return func(r *contractsv1.ContextFabricInvestigationRequest) { r.Options.MaxSerializedBytes = limit }
	}
	fits := newClientRouteRig(t, fixture)
	served := decodeEnvelope(t, fits.post(t, contractsv1.ContextFabricSynthesisModeClient, withLimit(limit)))
	if served.SynthesisInput == nil {
		t.Fatal("a budget that fits the result and not the result plus the input dropped the input")
	}

	large := cohortFixture(40)
	for _, mode := range []contractsv1.ContextFabricSynthesisMode{contractsv1.ContextFabricSynthesisModeClient, ""} {
		over := newClientRouteRig(t, large)
		refused := over.post(t, mode, withLimit(limit))
		if refused.Code != http.StatusRequestEntityTooLarge || !containsJSONCode(refused.Body.Bytes(), "invalid_request") {
			t.Fatalf("mode %q: a result above the budget: status = %d body=%s, want 413", mode, refused.Code, refused.Body.String())
		}
	}
}

// A client turn that ends before synthesis delivers no input and names no
// synthesis source.
func TestATerminalClientTurnCarriesNoSynthesisInputAndNoSource(t *testing.T) {
	rig := newClientRouteRig(t, singleSubjectFixture())
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeClient, func(r *contractsv1.ContextFabricInvestigationRequest) { r.TimeContext.EvidenceWindow = nil })
	envelope := decodeEnvelope(t, recorder)
	if envelope.Status != contractsv1.ContextFabricInvestigationClarificationRequired || envelope.WindowClarification == nil {
		t.Fatalf("status = %q window clarification = %v, want the window clarification", envelope.Status, envelope.WindowClarification != nil)
	}
	if _, present := topLevelKeys(t, recorder.Body.Bytes())["synthesis_input"]; present {
		t.Fatal("a terminal turn carries a synthesis_input key")
	}
	if envelope.Versions.SynthesisSource != "" {
		t.Fatalf("synthesis_source = %q on a turn that ended before synthesis, want none", envelope.Versions.SynthesisSource)
	}
	if _, synthesizes := rig.model.counts(); synthesizes != 0 {
		t.Fatalf("synthesize calls = %d, want 0", synthesizes)
	}
}

// SIZE measurement of three turns in client mode: one subject with a few
// facts, a cohort, and a turn whose facts the input bound cuts. Each input
// must validate and fit the bound.
func TestClientSynthesisInputSizeAcrossThreeTurns(t *testing.T) {
	for _, fixture := range []struct {
		name string
		clientRouteFixture
		bounded bool
	}{
		{"single_subject", singleSubjectFixture(), false},
		{"cohort", cohortFixture(12), false},
		{"facts_cut_by_the_bound", manyFactsFixture(400, 600), true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			rig := newClientRouteRig(t, fixture.clientRouteFixture)
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeClient, nil)
			envelope := decodeEnvelope(t, recorder)
			bundle := envelope.SynthesisInput
			if bundle == nil {
				t.Fatal("no synthesis_input")
			}
			resultBytes, err := json.Marshal(envelope.ContextFabricInvestigationResult)
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				Facts []json.RawMessage `json:"canonical_facts"`
			}
			if err := json.Unmarshal(bundle.Input, &input); err != nil {
				t.Fatal(err)
			}
			t.Logf("SIZE fixture=%s result_bytes=%d synthesis_input_bytes=%d facts_given=%d bounded=%v",
				fixture.name, len(resultBytes), len(bundle.Input), len(input.Facts), bundle.Bounded)
			if len(bundle.Input) > contractsv1.ContextFabricSynthesisInputDefaultMaxBytes {
				t.Fatalf("input is %d bytes, over the %d bound", len(bundle.Input), contractsv1.ContextFabricSynthesisInputDefaultMaxBytes)
			}
			if bundle.Bounded != fixture.bounded {
				t.Fatalf("bounded = %v, want %v", bundle.Bounded, fixture.bounded)
			}
			if fixture.bounded && len(input.Facts) >= len(fixture.facts.Facts) {
				t.Fatalf("facts_given = %d of %d read: the bound cut nothing", len(input.Facts), len(fixture.facts.Facts))
			}
		})
	}
}
