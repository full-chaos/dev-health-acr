package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// THE RELEASE-LEVEL CLAIM: one answerability determination, whichever surface
// serves the clarification.
//
// Three stored rows share one question and one accepted reading (a named
// subject whose declared kind is project):
//
//   - UNUSABLE: a clarification offering only ci_pipeline_run candidates, with
//     its reading persisted. Fresh composition refuses exactly these offers,
//     so every surface must serve the declared-kind terminal.
//   - CONTROL: the same clarification with one project candidate. Every
//     surface must keep it a clarification, and reuse must still serve it.
//   - LEGACY: the unusable row saved WITHOUT a reading. The determination is
//     unavailable: the reuse lookup declines it and the engine composes
//     afresh; the result-by-id route and the MCP tool forwarding it serve the
//     row as stored, and the route logs the unavailable determination.
//
// Every surface is the production one: Engine.Investigate for fresh
// composition and reuse, the hosted App over TLS for result-by-id, and the
// real MCP server bootstrapped against that App for investigation_result.

// surfaceInterpreter accepts one frame. A status question infers no window;
// drivers asks a status-and-drivers question read as a trend assessment,
// which the window class table gives a default window, so a request with no
// window of its own meets the window gate.
type surfaceInterpreter struct {
	frame   *contextfabric.QuestionFrame
	drivers bool
}

func (i surfaceInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	if i.drivers {
		interpreted.RequestedJudgment = "status_and_drivers"
		interpreted.FactRequirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}, {Kind: contextfabric.FactReadiness}}
		interpreted.WindowClass = contextfabric.WindowClassTrendAssessment
		interpreted.WindowConfidence = contextfabric.WindowConfidenceHigh
	}
	return interpreted, contextfabric.QuestionFamilyOutcome{
		Family: contextfabric.QuestionFamilyUnclassified, Source: contextfabric.QuestionFamilySourceModel,
		Frame: i.frame, Gate: contextfabric.FrameGate{Outcome: contextfabric.FrameGatePassed},
	}, nil
}

type surfaceGraph struct {
	resolution contextfabric.SubjectResolution
	material   contextfabric.StructureOfferMaterial
}

func (g surfaceGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "surface-graph", Epoch: 0}, nil
}

// ResolveSubjects answers a request carrying subject hints -- the reuse
// lookup's authorization recheck -- with exactly the hinted subjects
// committed, which is a graph that still authorizes every stored subject; any
// other request gets the fixed pool.
func (g surfaceGraph) ResolveSubjects(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	if hints := request.RequestedScope.SubjectHints; len(hints) > 0 {
		committed := make([]contextfabric.SubjectRef, 0, len(hints))
		for _, hint := range hints {
			committed = append(committed, contextfabric.SubjectRef{Kind: hint.Kind, CanonicalID: hint.ID, Label: hint.Label})
		}
		return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: committed}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
	}
	return g.resolution, g.material, nil, nil, nil
}

func (g surfaceGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

type surfaceFacts struct{ t *testing.T }

func (f surfaceFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	f.t.Error("a subjectless turn read facts")
	return contextfabric.CanonicalFactBundle{}, errors.New("unexpected fact read")
}

type surfaceSynthesizer struct{ t *testing.T }

func (s surfaceSynthesizer) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	s.t.Error("a subjectless turn synthesized")
	return contextfabric.InvestigationResult{}, errors.New("unexpected synthesis")
}

// surfaceReuseGate hands back one stored row for any key, so the reuse
// lookup's own filters are what decide.
type surfaceReuseGate struct {
	candidate contextfabric.InvestigationResult
}

func (g surfaceReuseGate) FindReusable(context.Context, storage.Principal, contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	return contextfabric.StoredInvestigationResult{Result: g.candidate, SemanticStateRead: contextfabric.SemanticStateReadAbsent}, true, "", nil
}

func surfaceEngine(t *testing.T, label string, frame *contextfabric.QuestionFrame, pool []contextfabric.SubjectCandidate, results contextfabric.InvestigationResultStore, gate contextfabric.AnswerReuseGate) *contextfabric.Engine {
	t.Helper()
	return surfaceEngineWith(t, label, surfaceInterpreter{frame: frame}, pool, contextfabric.StructureOfferMaterial{}, results, gate)
}

func surfaceEngineWith(t *testing.T, label string, interpreter surfaceInterpreter, pool []contextfabric.SubjectCandidate, material contextfabric.StructureOfferMaterial, results contextfabric.InvestigationResultStore, gate contextfabric.AnswerReuseGate) *contextfabric.Engine {
	t.Helper()
	var mu sync.Mutex
	next := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: interpreter,
		Graph: surfaceGraph{resolution: contextfabric.SubjectResolution{
			Candidates: pool, Committed: []contextfabric.SubjectRef{}, ClarificationPrompt: "Which subject did you mean: Candidate A, Candidate B?",
		}, material: material},
		Facts: surfaceFacts{t: t}, Synthesizer: surfaceSynthesizer{t: t},
		Results: results, ReuseGate: gate,
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			mu.Lock()
			defer mu.Unlock()
			next++
			return fmt.Sprintf("result_surface_%s_%02d", label, next)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func surfaceRequest(requestID string) contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     requestID,
		Question:      "What is the status of the named project?",
		TimeContext:   contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
}

// surfacePool builds a two-candidate pool whose labels are fixed by position,
// so the unusable and control rows differ in candidate KIND only.
func surfacePool(first, second contextfabric.SubjectKind) []contextfabric.SubjectCandidate {
	candidate := func(receipt string, kind contextfabric.SubjectKind, id, label string) contextfabric.SubjectCandidate {
		return contextfabric.SubjectCandidate{
			ReceiptID: receipt, Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: label},
			State: contextfabric.ResolutionAmbiguous, MatchReasons: []string{"Exact canonical subject label match."}, Confidence: 0.9,
		}
	}
	return []contextfabric.SubjectCandidate{
		candidate("subr_surface_first", first, string(first)+":surface-a", "Candidate A"),
		candidate("subr_surface_second", second, string(second)+":surface-b", "Candidate B"),
	}
}

// surfaceReading is the persisted reading the question's turn accepts: the
// frame validated and gated as interpretation does, through the codec.
func surfaceReading(t *testing.T, frame contextfabric.QuestionFrame) *contextfabric.PersistedSemanticState {
	t.Helper()
	validated := contextfabric.ValidateFrame(frame, nil, contextfabric.ShapeOpen)
	if validated.Outcome != contextfabric.FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: frame invalid (%v)", validated.Failure.Invariant)
	}
	state := contextfabric.BuildSemanticState(contextfabric.SemanticStateInput{
		Outcome: contextfabric.QuestionFamilyOutcome{
			Family: contextfabric.QuestionFamilyUnclassified, Source: contextfabric.QuestionFamilySourceModel,
			Frame: &validated.Frame, Gate: contextfabric.DecideFrameGate(validated, true),
		},
		EmittedShape:    contextfabric.ShapeOpen,
		FamilyVersion:   contextfabric.QuestionFamilyTableVersion,
		RequestIdentity: contextfabric.SemanticRequestIdentityOf(surfaceRequest("request_surface_identity"), ""),
	})
	if _, err := contextfabric.EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture defect: the reading does not encode: %v", err)
	}
	return state
}

func saveSurfaceRow(t *testing.T, store *memoryinvestigation.Store, result contextfabric.InvestigationResult, semantic contextfabric.SemanticStateWrite) {
	t.Helper()
	if err := store.Save(context.Background(), storage.Principal{OrgID: callerOrgID}, result, contextfabric.SourceWatermarkSnapshot{}, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{},
		contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", semantic); err != nil {
		t.Fatalf("seed %s: %v", result.ResultID, err)
	}
}

// callRealMCPInvestigationResult drives the real MCP server's
// investigation_result tool over the SDK's in-memory transport.
func callRealMCPInvestigationResult(t *testing.T, boot *acrmcp.Bootstrap, resultID string) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	ctx := context.Background()
	server := acrmcp.NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "surface-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	defer clientSession.Close()
	arguments, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: resultID})
	if err != nil {
		t.Fatal(err)
	}
	called, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "investigation_result", Arguments: json.RawMessage(arguments)})
	if err != nil {
		t.Fatalf("investigation_result call: %v", err)
	}
	if called.IsError {
		t.Fatalf("investigation_result reported an error: %s", mustRawJSON(t, called.Content))
	}
	var response contractsv1.MCPInvestigationResultResponse
	if err := json.Unmarshal(mustRawJSON(t, called.StructuredContent), &response); err != nil {
		t.Fatalf("decode tool response: %v", err)
	}
	return response.Structured
}

// getRealAPIResult reads the canonical view from the real hosted route.
func getRealAPIResult(t *testing.T, server *httptest.Server, token, resultID string) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/context-fabric/investigations/"+resultID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.2.5")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("hosted result request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("hosted result status = %d, body %s", response.StatusCode, body)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode hosted result: %v", err)
	}
	return result
}

// storedAnswerabilityLines returns the stored-answerability lines written to
// logs after offset.
func storedAnswerabilityLines(t *testing.T, logs *bytes.Buffer, offset int) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()[offset:]), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == contextfabric.StoredAnswerabilityLogMessage {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestAStoredClarificationIsHandledAlikeOnEveryServingSurface(t *testing.T) {
	project := contextfabric.SubjectProject
	ciRun := contractsv1.ContextFabricSubjectCIRun
	frame := contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"named-project"}, ExpectedKind: &project}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	reading := surfaceReading(t, frame)
	accepted := reading.Frame
	controlPool := surfacePool(ciRun, project)
	unusablePool := surfacePool(ciRun, ciRun)

	// The control document is COMPOSED by the engine; the unusable and
	// legacy rows are that document with only the second candidate's kind
	// changed -- the row an earlier build composed for the unusable offers.
	composed, err := surfaceEngine(t, "compose", accepted, controlPool, memoryinvestigation.NewStore(), nil).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, surfaceRequest("request_surface_compose"))
	if err != nil {
		t.Fatalf("compose the control row: %v", err)
	}
	if composed.Status != contextfabric.InvestigationClarificationRequired {
		t.Fatalf("fixture defect: the composed control row is %q, want clarification_required", composed.Status)
	}
	withPool := func(id string, pool []contextfabric.SubjectCandidate) contextfabric.InvestigationResult {
		row := composed
		row.ResultID = id
		row.SubjectResolution.Candidates = append([]contextfabric.SubjectCandidate(nil), pool...)
		return row
	}
	store := memoryinvestigation.NewStore()
	rows := []struct {
		name           string
		row            contextfabric.InvestigationResult
		pool           []contextfabric.SubjectCandidate
		semantic       contextfabric.SemanticStateWrite
		fresh          contextfabric.InvestigationStatus
		reused         bool
		read           contextfabric.InvestigationStatus
		determination  string
		semanticReadAs string
	}{
		{"unusable", withPool("result_surface_unusable", unusablePool), unusablePool, contextfabric.SemanticStateOf(reading),
			contextfabric.InvestigationNoMatch, false, contextfabric.InvestigationNoMatch, "unanswerable", "available"},
		{"control", withPool("result_surface_control", controlPool), controlPool, contextfabric.SemanticStateOf(reading),
			contextfabric.InvestigationClarificationRequired, true, contextfabric.InvestigationClarificationRequired, "answerable", "available"},
		{"legacy", withPool("result_surface_legacy", unusablePool), unusablePool, contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation),
			contextfabric.InvestigationNoMatch, false, contextfabric.InvestigationClarificationRequired, "unavailable", "absent"},
	}
	for _, row := range rows {
		saveSurfaceRow(t, store, row.row, row.semantic)
	}

	logs := &bytes.Buffer{}
	app, token := newParityHostedAppWithLogs(t, surfaceEngine(t, "app", accepted, controlPool, store, nil), store,
		limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, logs)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("sidecar bootstrap: %v", err)
	}
	assertAdvertised(t, boot.Capabilities.EnabledTools, "investigation_result")

	declaredKindBasis := contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			wantBasis := contractsv1.ContextFabricRefusalBasis("")
			if row.read == contextfabric.InvestigationNoMatch {
				wantBasis = declaredKindBasis
			}

			fresh, err := surfaceEngine(t, "fresh_"+row.name, accepted, row.pool, memoryinvestigation.NewStore(), nil).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, surfaceRequest("request_surface_fresh"))
			if err != nil {
				t.Fatalf("fresh composition: %v", err)
			}
			if fresh.Status != row.fresh {
				t.Fatalf("fresh composition status = %q, want %q", fresh.Status, row.fresh)
			}

			stored, err := store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, row.row.ResultID)
			if err != nil {
				t.Fatalf("read the stored row: %v", err)
			}
			reused, err := surfaceEngine(t, "reuse_"+row.name, accepted, row.pool, store, surfaceReuseGate{candidate: stored.Result}).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, surfaceRequest("request_surface_reuse"))
			if err != nil {
				t.Fatalf("reuse: %v", err)
			}
			if reused.Reused != row.reused || reused.Status != row.fresh {
				t.Fatalf("reuse served reused=%v status=%q, want reused=%v status=%q", reused.Reused, reused.Status, row.reused, row.fresh)
			}

			offset := logs.Len()
			byID := getRealAPIResult(t, server, token, row.row.ResultID)
			if byID.Status != row.read || byID.RefusalBasis != wantBasis {
				t.Fatalf("result-by-id status/basis = %q/%q, want %q/%q", byID.Status, byID.RefusalBasis, row.read, wantBasis)
			}
			viaMCP := callRealMCPInvestigationResult(t, boot, row.row.ResultID)
			if viaMCP.Status != byID.Status || viaMCP.RefusalBasis != byID.RefusalBasis || viaMCP.DeterministicAnswer != byID.DeterministicAnswer {
				t.Fatalf("MCP investigation_result status/basis/answer = %q/%q/%q, result-by-id served %q/%q/%q", viaMCP.Status, viaMCP.RefusalBasis, viaMCP.DeterministicAnswer, byID.Status, byID.RefusalBasis, byID.DeterministicAnswer)
			}
			for surface, served := range map[string]*contractsv1.ContextFabricSemanticReading{"result-by-id": byID.SemanticReading, "MCP investigation_result": viaMCP.SemanticReading} {
				if row.name == "legacy" {
					if served == nil || served.Status != contractsv1.ContextFabricSemanticReadingUnavailable || served.Reason != contractsv1.ContextFabricSemanticReadingStateAbsent {
						t.Fatalf("%s semantic_reading = %+v, want unavailable/semantic_state_absent on a row with no stored reading", surface, served)
					}
				} else if served != nil {
					t.Fatalf("%s semantic_reading = %+v on a row whose reading was available", surface, served)
				}
			}
			if row.read == contextfabric.InvestigationNoMatch && row.fresh == contextfabric.InvestigationNoMatch && byID.DeterministicAnswer != fresh.DeterministicAnswer {
				t.Fatalf("result-by-id answer %q differs from fresh composition's %q for the same offers", byID.DeterministicAnswer, fresh.DeterministicAnswer)
			}

			lines := storedAnswerabilityLines(t, logs, offset)
			if len(lines) != 2 {
				t.Fatalf("stored-answerability lines = %d, want one per read (result-by-id, then the MCP forward); log: %s", len(lines), logs.String()[offset:])
			}
			for _, line := range lines {
				if line["surface"] != "result_by_id" || line["determination"] != row.determination || line["decided_by"] != "role" || line["semantic_state"] != row.semanticReadAs ||
					line["stored_status"] != "clarification_required" || line["served_status"] != string(row.read) || line["level"] != "INFO" {
					t.Fatalf("stored-answerability line = %v, want surface result_by_id, determination %q, semantic_state %q, served %q", line, row.determination, row.semanticReadAs, row.read)
				}
				if repaired, _ := line["repaired"].(bool); repaired != (row.read == contextfabric.InvestigationNoMatch) {
					t.Fatalf("repaired = %v on %s", line["repaired"], row.name)
				}
			}
			if row.name == "legacy" {
				projection := getRealAPIProjection(t, server, token, row.row.ResultID, 3, 1, 10)
				if projection.SemanticReading == nil || projection.SemanticReading.Reason != contractsv1.ContextFabricSemanticReadingStateAbsent {
					t.Fatalf("projection semantic_reading = %+v, want the same disclosure the canonical view carries", projection.SemanticReading)
				}
			}
		})
	}
}

// TestTheWindowGateAndOfferLessRowsAreHandledAlikeOnEveryServingSurface holds
// the precedence steps the role rows above do not reach, on every serving
// surface: the window gate's own clarification (named and organization-scope
// readings) is served as fresh composition serves it, and an offer-less row
// with an organization-scope reading is refused on that basis.
func TestTheWindowGateAndOfferLessRowsAreHandledAlikeOnEveryServingSurface(t *testing.T) {
	project := contextfabric.SubjectProject
	ciRun := contractsv1.ContextFabricSubjectCIRun
	namedFrame := contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"named-project"}, ExpectedKind: &project}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	orgFrame := contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionOrganizationScope, Org: &contextfabric.OrganizationScopeExpression{}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	unconfirmed := func(requestID string) contextfabric.InvestigationRequest {
		request := surfaceRequest(requestID)
		request.TimeContext.EvidenceWindow = nil
		return request
	}
	// The gate's offers-only resolve returns options of kinds neither reading
	// admits, so the stored gate row carries offers a terminal would refuse:
	// only the window-gate step can serve it.
	wrongKindOffers := contextfabric.StructureOfferMaterial{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
		HandleOptions: []contractsv1.ContextFabricHandleOption{
			{ReceiptID: "handr_surface_gate_a", OptionID: "opt_handle_gate_a", Label: "CI run 29213415002", Kind: ciRun, PatternID: "pr_number", Value: "29213415002", SourceColumn: "ci_pipeline_runs.run_id", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			{ReceiptID: "candr_surface_gate_a", OptionID: "opt_cand_gate_a", Label: "CI run 29213415002", Kind: ciRun, CanonicalID: "ci_pipeline_run.v2:x:29213415002", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
	type cell struct {
		name          string
		drivers       bool
		material      contextfabric.StructureOfferMaterial
		reading       *contextfabric.PersistedSemanticState
		pool          []contextfabric.SubjectCandidate
		request       func(string) contextfabric.InvestigationRequest
		row           contextfabric.InvestigationResult
		fresh         contextfabric.InvestigationStatus
		basis         contractsv1.ContextFabricRefusalBasis
		step          string
		determination string
		semanticState string
	}
	var cells []cell
	for _, gate := range []struct {
		name  string
		frame contextfabric.QuestionFrame
	}{{"window_gate_named", namedFrame}, {"window_gate_organization", orgFrame}} {
		reading := surfaceReading(t, gate.frame)
		pool := surfacePool(ciRun, ciRun)
		composed, err := surfaceEngineWith(t, "compose_"+gate.name, surfaceInterpreter{frame: reading.Frame, drivers: true}, pool, wrongKindOffers, memoryinvestigation.NewStore(), nil).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, unconfirmed("request_compose_"+gate.name))
		if err != nil {
			t.Fatalf("compose %s: %v", gate.name, err)
		}
		if composed.Status != contextfabric.InvestigationClarificationRequired || composed.StructureNeeds == nil || len(composed.StructureNeeds.WindowOptions) == 0 {
			t.Fatalf("fixture defect: %s composed status %q without structure window options; the window gate did not run", gate.name, composed.Status)
		}
		if len(composed.StructureNeeds.HandleOptions)+len(composed.StructureNeeds.CandidateOptions) == 0 {
			t.Fatalf("fixture defect: %s gate row carries no subject-kind option, so no step but the window gate is excluded", gate.name)
		}
		composed.ResultID = "result_surface_" + gate.name
		cells = append(cells, cell{gate.name, true, wrongKindOffers, reading, pool, unconfirmed, composed, contextfabric.InvestigationClarificationRequired, "", "window_gate", "answerable", "not_read"})
	}
	orgReading := surfaceReading(t, orgFrame)
	cells = append(cells, cell{"offer_less_organization", false, contextfabric.StructureOfferMaterial{}, orgReading, []contextfabric.SubjectCandidate{}, surfaceRequest,
		legacyUnanswerableClarificationRow(t, "result_surface_offer_less_org"), contextfabric.InvestigationNoMatch,
		contractsv1.ContextFabricRefusalBasisOrganizationScopeUnsupported, "organization_scope", "unanswerable", "available"})

	store := memoryinvestigation.NewStore()
	for _, c := range cells {
		saveSurfaceRow(t, store, c.row, contextfabric.SemanticStateOf(c.reading))
	}
	logs := &bytes.Buffer{}
	app, token := newParityHostedAppWithLogs(t, surfaceEngine(t, "app_gate", orgReading.Frame, nil, store, nil), store,
		limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, logs)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("sidecar bootstrap: %v", err)
	}

	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			fresh, err := surfaceEngineWith(t, "fresh_"+c.name, surfaceInterpreter{frame: c.reading.Frame, drivers: c.drivers}, c.pool, c.material, memoryinvestigation.NewStore(), nil).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, c.request("request_fresh_"+c.name))
			if err != nil {
				t.Fatalf("fresh composition: %v", err)
			}
			if fresh.Status != c.fresh || fresh.RefusalBasis != c.basis {
				t.Fatalf("fresh composition status/basis = %q/%q, want %q/%q", fresh.Status, fresh.RefusalBasis, c.fresh, c.basis)
			}
			stored, err := store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, c.row.ResultID)
			if err != nil {
				t.Fatalf("read the stored row: %v", err)
			}
			reused, err := surfaceEngineWith(t, "reuse_"+c.name, surfaceInterpreter{frame: c.reading.Frame, drivers: c.drivers}, c.pool, c.material, store, surfaceReuseGate{candidate: stored.Result}).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, c.request("request_reuse_"+c.name))
			if err != nil {
				t.Fatalf("reuse: %v", err)
			}
			if reused.Status != fresh.Status || reused.RefusalBasis != fresh.RefusalBasis {
				t.Fatalf("reuse served %q/%q (reused=%v), fresh composition %q/%q", reused.Status, reused.RefusalBasis, reused.Reused, fresh.Status, fresh.RefusalBasis)
			}

			offset := logs.Len()
			byID := getRealAPIResult(t, server, token, c.row.ResultID)
			if byID.Status != fresh.Status || byID.RefusalBasis != fresh.RefusalBasis {
				t.Fatalf("result-by-id status/basis = %q/%q, fresh composition %q/%q", byID.Status, byID.RefusalBasis, fresh.Status, fresh.RefusalBasis)
			}
			if c.basis == contractsv1.ContextFabricRefusalBasisOrganizationScopeUnsupported {
				found := false
				for _, limitation := range byID.Limitations {
					found = found || limitation == contractsv1.ContextFabricOrganizationScopeUnsupportedLimitation
				}
				if !found {
					t.Fatalf("result-by-id limitations = %#v, want the organization-scope sentence", byID.Limitations)
				}
			}
			viaMCP := callRealMCPInvestigationResult(t, boot, c.row.ResultID)
			if viaMCP.Status != byID.Status || viaMCP.RefusalBasis != byID.RefusalBasis || viaMCP.DeterministicAnswer != byID.DeterministicAnswer {
				t.Fatalf("MCP investigation_result %q/%q/%q, result-by-id %q/%q/%q", viaMCP.Status, viaMCP.RefusalBasis, viaMCP.DeterministicAnswer, byID.Status, byID.RefusalBasis, byID.DeterministicAnswer)
			}
			if byID.SemanticReading != nil || viaMCP.SemanticReading != nil {
				t.Fatalf("semantic_reading = %+v / %+v on a row whose determination was taken", byID.SemanticReading, viaMCP.SemanticReading)
			}
			lines := storedAnswerabilityLines(t, logs, offset)
			if len(lines) != 2 {
				t.Fatalf("stored-answerability lines = %d, want one per read (result-by-id, then the MCP forward); log: %s", len(lines), logs.String()[offset:])
			}
			for _, line := range lines {
				repaired, _ := line["repaired"].(bool)
				if line["determination"] != c.determination || line["decided_by"] != c.step || line["semantic_state"] != c.semanticState ||
					line["served_status"] != string(fresh.Status) || repaired != (c.fresh == contextfabric.InvestigationNoMatch) {
					t.Fatalf("stored-answerability line = %v, want %s/%s, semantic_state %s, served %s", line, c.determination, c.step, c.semanticState, fresh.Status)
				}
			}
		})
	}
}
