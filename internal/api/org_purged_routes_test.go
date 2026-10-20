package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// purgedOrgWriteRefusal is the error a store returns when the database
// refuses a write for a purged organization, wrapped the way stores wrap it.
func purgedOrgWriteRefusal() error {
	return fmt.Errorf("save row: %w", storage.ErrOrgPurged)
}

func assertPurgedOrgAnswer(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	var body struct {
		Error struct {
			Retryable bool `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, response.Body.String())
	}
	if body.Error.Retryable {
		t.Fatalf("a purged organization's refusal must not be retryable: %s", response.Body.String())
	}
}

func TestEpisodeCreateForAnOrganizationPurgedInFlightAnswersInvalidToken(t *testing.T) {
	creator := &fakeEpisodeCreator{err: purgedOrgWriteRefusal()}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	if creator.create.ClientEpisodeID == "" {
		t.Fatal("the request must reach the episode writer")
	}
	assertPurgedOrgAnswer(t, response)
}

func TestEpisodeCreateStorageOutageIsStillUnavailable(t *testing.T) {
	creator := &fakeEpisodeCreator{err: fmt.Errorf("save row: %w", storage.ErrUnavailable)}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
}

func TestContextPacketForAnOrganizationPurgedInFlightAnswersInvalidToken(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	app.runtime.Assembler = failingAssembler{err: fmt.Errorf("save packet snapshot: %w", purgedOrgWriteRefusal())}
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))

	assertPurgedOrgAnswer(t, response)
}

// purgedResultStore is a result store whose database refuses every save.
type purgedResultStore struct {
	contextfabric.InvestigationResultStore
	saves int
}

func (s *purgedResultStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity, contextfabric.ReusePromptVersions, contextfabric.ReuseVersionAuthorities, int64, string, contextfabric.SemanticStateWrite) error {
	s.saves++
	return purgedOrgWriteRefusal()
}

// The real engine runs a whole investigation (interpretation, graph, facts,
// synthesis) and the organization is purged before the result is saved.
func TestInvestigationForAnOrganizationPurgedDuringTheModelCallAnswersInvalidToken(t *testing.T) {
	fixture := singleSubjectFixture()
	project := suppliedRouteProject()
	model := &clientRouteModel{interpreted: fixture.interpreted}
	store := &purgedResultStore{InvestigationResultStore: memoryinvestigation.NewStore()}
	nextID := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model},
		Graph:       clientRouteGraph{authorizingGraph: authorizingGraph{liveGraphReader: liveGraphReader{project: project}, outcome: contextfabric.StoredSubjectAdmitted}},
		Facts:       liveFactReader{bundle: fixture.facts},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{
			Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "purged-route", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
		},
		Results: store,
	}, contextfabric.EngineOptions{
		ServiceVersion: "purged-route",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			nextID++
			return fmt.Sprintf("result_purged_route%02d", nextID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rig := &clientRouteRig{model: model, logs: &bytes.Buffer{}}
	rig.app, rig.token = newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, rig.logs)
	rig.app.config.RequestTimeout = time.Minute
	rig.app.runtime.Entitlements = &serviceModelGrants{feature: true, fallback: true}

	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)

	if interprets, synthesizes := model.counts(); interprets != 1 || synthesizes != 1 {
		t.Fatalf("model calls = interpret %d synthesize %d, want the whole investigation to run before the save", interprets, synthesizes)
	}
	if store.saves == 0 {
		t.Fatal("the engine must reach the result save")
	}
	assertPurgedOrgAnswer(t, recorder)
}

func TestTokenExchangeForAnOrganizationPurgedInFlightIsAnInvalidGrant(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: fmt.Errorf("issue workload access token: %w", storage.ErrOrgPurged)})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))

	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorInvalidGrant)
}
