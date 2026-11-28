package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// wholeCandidatesEngine is the production engine over a graph that answers
// every request with a two-project choice, proven whole when whole is true.
func wholeCandidatesEngine(t *testing.T, label string, whole bool, results contextfabric.InvestigationResultStore, gate contextfabric.AnswerReuseGate) *contextfabric.Engine {
	t.Helper()
	pool := surfacePool(contextfabric.SubjectProject, contextfabric.SubjectProject)
	resolution := contextfabric.SubjectResolution{
		Candidates: pool, Committed: []contextfabric.SubjectRef{}, ClarificationPrompt: "Which subject did you mean: Candidate A, Candidate B?",
	}
	if whole {
		resolution.WholeCandidates = contractsv1.ContextFabricSubjectRefsOf(pool)
	}
	project := contextfabric.SubjectProject
	frame := surfaceReading(t, contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"named-project"}, ExpectedKind: &project}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}).Frame
	var mu sync.Mutex
	next := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: surfaceInterpreter{frame: frame},
		Graph:       surfaceGraph{resolution: resolution},
		Facts:       surfaceFacts{t: t}, Synthesizer: surfaceSynthesizer{t: t},
		Results: results, ReuseGate: gate,
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			mu.Lock()
			defer mu.Unlock()
			next++
			return fmt.Sprintf("result_whole_%s_%02d", label, next)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

type wholeCandidatesServed struct {
	header string
	result contractsv1.ContextFabricInvestigationResult
}

func serveWholeCandidates(t *testing.T, server *httptest.Server, token, method, path string, body any) wholeCandidatesServed {
	t.Helper()
	header, raw := exchangeWholeCandidates(t, server, token, method, path, body)
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return wholeCandidatesServed{header: header, result: result}
}

// serveWholeCandidatesRaw is serveWholeCandidates for a body it does not decode.
func serveWholeCandidatesRaw(t *testing.T, server *httptest.Server, token, method, path string, body any) string {
	t.Helper()
	header, _ := exchangeWholeCandidates(t, server, token, method, path, body)
	return header
}

func exchangeWholeCandidates(t *testing.T, server *httptest.Server, token, method, path string, body any) (string, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.2.5")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s status = %d, body %s", method, path, response.StatusCode, raw)
	}
	return response.Header.Get(contractsv1.ContextFabricSubjectCandidatesCompleteHeader), raw
}

func wholeCandidatesServer(t *testing.T, investigator contextfabric.Investigator, store contextfabric.InvestigationResultStore) (*httptest.Server, string) {
	t.Helper()
	app, token := newParityHostedAppWithLogs(t, investigator, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, &bytes.Buffer{})
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	return server, token
}

func TestSubjectCandidatesCompleteHeaderRidesAFreshWholeClarification(t *testing.T) {
	store := memoryinvestigation.NewStore()
	server, token := wholeCandidatesServer(t, wholeCandidatesEngine(t, "fresh", true, store, nil), store)

	served := serveWholeCandidates(t, server, token, http.MethodPost, ContextFabricInvestigationsPath, investigationRequestBody())
	if served.result.Status != contractsv1.ContextFabricInvestigationClarificationRequired || len(served.result.SubjectResolution.Candidates) != 2 {
		t.Fatalf("fixture defect: served status %q with %d candidates, want a two-candidate clarification", served.result.Status, len(served.result.SubjectResolution.Candidates))
	}
	if served.header != contractsv1.ContextFabricSubjectCandidatesCompleteValue {
		t.Fatalf("%s = %q on a fresh clarification proven whole, want %q", contractsv1.ContextFabricSubjectCandidatesCompleteHeader, served.header, contractsv1.ContextFabricSubjectCandidatesCompleteValue)
	}

	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("sidecar bootstrap: %v", err)
	}
	viaSidecar, _, err := boot.Client.InvestigateWithSynthesisInput(context.Background(), investigationRequestBody())
	if err != nil {
		t.Fatalf("sidecar investigate: %v", err)
	}
	if !viaSidecar.SubjectResolution.CandidateSetProvenWhole() {
		t.Fatalf("the MCP server's client did not read the claim: WholeCandidates = %#v", viaSidecar.SubjectResolution.WholeCandidates)
	}
}

func TestSubjectCandidatesCompleteHeaderIsAbsentOnEveryOtherPath(t *testing.T) {
	store := memoryinvestigation.NewStore()
	server, token := wholeCandidatesServer(t, wholeCandidatesEngine(t, "seed", true, store, nil), store)
	seeded := serveWholeCandidates(t, server, token, http.MethodPost, ContextFabricInvestigationsPath, investigationRequestBody())
	if seeded.header == "" {
		t.Fatal("fixture defect: the seeding answer carries no header, so the arms below would prove nothing")
	}
	fresh, err := wholeCandidatesEngine(t, "direct", true, memoryinvestigation.NewStore(), nil).Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, surfaceRequest("request_whole_direct"))
	if err != nil {
		t.Fatalf("fresh composition: %v", err)
	}
	if !fresh.SubjectResolution.CandidateSetProvenWhole() || fresh.Reused {
		t.Fatal("fixture defect: the engine's own fresh answer is not proven whole")
	}
	answering := func(result contextfabric.InvestigationResult) contextfabric.Investigator {
		return investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			return result, nil
		})
	}

	t.Run("not proven", func(t *testing.T) {
		store := memoryinvestigation.NewStore()
		server, token := wholeCandidatesServer(t, wholeCandidatesEngine(t, "unproven", false, store, nil), store)
		if served := serveWholeCandidates(t, server, token, http.MethodPost, ContextFabricInvestigationsPath, investigationRequestBody()); served.header != "" {
			t.Fatalf("header = %q on a clarification nothing proved whole", served.header)
		}
	})
	t.Run("result by id", func(t *testing.T) {
		if served := serveWholeCandidates(t, server, token, http.MethodGet, ContextFabricInvestigationsPath+"/"+seeded.result.ResultID, nil); served.header != "" {
			t.Fatalf("header = %q on the stored result read by id", served.header)
		}
	})
	t.Run("reused answer", func(t *testing.T) {
		reused := fresh
		reused.Reused = true
		server, token := wholeCandidatesServer(t, answering(reused), memoryinvestigation.NewStore())
		served := serveWholeCandidates(t, server, token, http.MethodPost, ContextFabricInvestigationsPath, investigationRequestBody())
		if !served.result.Reused {
			t.Fatal("fixture defect: the served answer is not marked reused")
		}
		if served.header != "" {
			t.Fatalf("header = %q on a reused answer that still holds the in-process proof", served.header)
		}
	})
	t.Run("control: the same answer fresh", func(t *testing.T) {
		server, token := wholeCandidatesServer(t, answering(fresh), memoryinvestigation.NewStore())
		if served := serveWholeCandidates(t, server, token, http.MethodPost, ContextFabricInvestigationsPath, investigationRequestBody()); served.header != contractsv1.ContextFabricSubjectCandidatesCompleteValue {
			t.Fatalf("header = %q on the fresh control, want %q", served.header, contractsv1.ContextFabricSubjectCandidatesCompleteValue)
		}
	})
	t.Run("not a clarification", func(t *testing.T) {
		answered := fresh
		answered.Status = contractsv1.ContextFabricInvestigationNoMatch
		server, token := wholeCandidatesServer(t, answering(answered), memoryinvestigation.NewStore())
		served := serveWholeCandidatesRaw(t, server, token, http.MethodPost, ContextFabricInvestigationsPath, investigationRequestBody())
		if served != "" {
			t.Fatalf("header = %q on a result that is not a clarification", served)
		}
	})
}
