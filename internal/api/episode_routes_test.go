package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestHostedReadRuntimeRemainsAvailableWithoutEpisodeCreator(t *testing.T) {
	// Given
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	request := authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusForbidden {
		t.Fatalf("episode status = %d", response.Code)
	}
	capabilitiesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	capabilitiesRequest.Header.Set("Authorization", "Bearer "+token)
	capabilitiesRequest.Header.Set("X-ACR-Client-Version", "1.0.0")
	capabilities := httptest.NewRecorder()
	app.Handler().ServeHTTP(capabilities, capabilitiesRequest)
	if capabilities.Code != http.StatusOK || strings.Contains(capabilities.Body.String(), "record_episode") {
		t.Fatalf("read runtime or capabilities changed: status=%d body=%s", capabilities.Code, capabilities.Body.String())
	}
}

func TestCreateEpisodeReturnsUnavailableWithoutCreator(t *testing.T) {
	// Given
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeEpisodeWrite}, nil, nil)
	request := authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestCreateEpisodeReturnsCreatedAndRecordsAuthenticatedPrincipal(t *testing.T) {
	// Given
	creator := &fakeEpisodeCreator{episode: storedEpisode()}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	request := authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusCreated || creator.principal.OrgID != "org_1" || creator.create.IdempotencyKey != "idempotency_01" {
		t.Fatalf("status=%d principal=%#v create=%#v", response.Code, creator.principal, creator.create)
	}
}

func TestCreateEpisodeReturnsExpectedStatusForCreatorOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		duplicate bool
		err       error
		retention string
		status    int
	}{
		{name: "duplicate", duplicate: true, status: http.StatusOK},
		{name: "no persist", err: episode.ErrNoPersistAccepted, retention: "no_persist", status: http.StatusNoContent},
		{name: "opaque packet linkage", err: storage.ErrNotFound, status: http.StatusNotFound},
		{name: "missing packet is never no persist", err: storage.ErrNotFound, retention: "no_persist", status: http.StatusNotFound},
		{name: "conflict", err: storage.ErrConflict, status: http.StatusConflict},
		{name: "timeout", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout},
		{name: "unavailable", err: errors.New("database unavailable"), status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			creator := &fakeEpisodeCreator{episode: storedEpisode(), duplicate: test.duplicate, err: test.err}
			app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
			create := episodeCreate()
			if test.retention != "" {
				create.RetentionClass = test.retention
			}
			request := authenticatedEpisodeRequest(t, token, create, "idempotency_01")

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			// Then
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestCreateEpisodeRejectsInvalidIdempotencyAndBodies(t *testing.T) {
	tests := []struct {
		name   string
		header string
		body   string
		status int
	}{
		{name: "missing header", body: episodeJSON(t, episodeCreate()), status: http.StatusBadRequest},
		{name: "mismatched header", header: "different_01", body: episodeJSON(t, episodeCreate()), status: http.StatusBadRequest},
		{name: "short header", header: "short", body: episodeJSON(t, episodeCreate()), status: http.StatusBadRequest},
		{name: "long header", header: strings.Repeat("x", 257), body: episodeJSON(t, episodeCreate()), status: http.StatusBadRequest},
		{name: "multiple JSON values", header: "idempotency_01", body: episodeJSON(t, episodeCreate()) + " {}", status: http.StatusBadRequest},
		{name: "unknown field", header: "idempotency_01", body: strings.TrimSuffix(episodeJSON(t, episodeCreate()), "}") + `,"identity":"untrusted"}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/episodes", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			request.Header.Set("Content-Type", "application/json")
			if test.header != "" {
				request.Header.Set("Idempotency-Key", test.header)
			}

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			// Then
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

type fakeEpisodeCreator struct {
	episode   contractsv1.AgentEpisode
	duplicate bool
	err       error
	principal storage.Principal
	create    contractsv1.AgentEpisodeCreate
}

func (f *fakeEpisodeCreator) Create(_ context.Context, principal storage.Principal, create contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	f.principal, f.create = principal, create
	return f.episode, f.duplicate, f.err
}

func hostedEpisodeTestApp(t *testing.T, creator *fakeEpisodeCreator, scopes []string, entitlements EntitlementProvider) (*App, string) {
	t.Helper()
	app, token := newHostedTestApp(t, nil, nil, scopes, entitlements, nil)
	app.runtime.Episodes = creator
	return app, token
}

func authenticatedEpisodeRequest(t *testing.T, token string, create contractsv1.AgentEpisodeCreate, idempotencyKey string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/episodes", strings.NewReader(episodeJSON(t, create)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	return request
}

func episodeJSON(t *testing.T, create contractsv1.AgentEpisodeCreate) string {
	t.Helper()
	encoded, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func episodeCreate() contractsv1.AgentEpisodeCreate {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	return contractsv1.AgentEpisodeCreate{SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01", Goal: "bounded goal", Summary: "bounded summary", Repository: contractsv1.RepositoryRef{Slug: hostedTestRepository}, Client: contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"}, StartedAt: now, EndedAt: now, Outcome: "succeeded", RetentionClass: "default_90d", Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"}}
}

func storedEpisode() contractsv1.AgentEpisode {
	create := episodeCreate()
	create.SchemaVersion = contractsv1.AgentEpisodeSchema
	return contractsv1.AgentEpisode{AgentEpisodeCreate: create, EpisodeID: "episode_server_01", CreatedAt: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC), RedactionState: "active"}
}

// TestCreateEpisode_writebackNotEnabledForOrg_isNonRetryableAndNotLoggedAsError
// is the regression test for review finding L6: ErrEpisodeWritebackNotEnabledForOrg
// previously fell into writeEpisodeError's default case, which is meant for
// genuine dependency failures -- it logged ERROR "episode recording
// dependency failed" and marked the response retryable:true, even though
// this is a permanent, expected denial (retrying the identical request
// cannot succeed until the org is added to the cohort). RED: this test
// failed (retryable=true, an ERROR log line present) before the dedicated
// switch case was added.
func TestCreateEpisode_writebackNotEnabledForOrg_isNonRetryableAndNotLoggedAsError(t *testing.T) {
	var buffer bytes.Buffer
	creator := &fakeEpisodeCreator{err: ErrEpisodeWritebackNotEnabledForOrg}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	app.logger = testLogger(&buffer)
	request := authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01")

	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Retryable {
		t.Fatalf("cohort-denial error envelope retryable = true, want false (this is a permanent, expected denial, not a transient dependency failure): %#v", envelope)
	}
	if strings.Contains(buffer.String(), `"level":"ERROR"`) {
		t.Fatalf("cohort-denial logged at ERROR level, want a non-ERROR (or absent) log for an expected, permanent denial: %s", buffer.String())
	}
}

// TestCreateEpisode_writebackNotEnabledForOrg_matchesDisabledCreatorResponseBody
// is the parity guard the L6 fix depends on: the cohort-denial response
// body must be byte-identical to what a nil a.runtime.Episodes already
// produces (TestCreateEpisodeReturnsUnavailableWithoutCreator), or a caller
// could distinguish "writeback off entirely" from "writeback on, you're
// just not in the cohort" purely by observing the response -- exactly the
// leak the cohort decorator's own doc comment says must never happen.
func TestCreateEpisode_writebackNotEnabledForOrg_matchesDisabledCreatorResponseBody(t *testing.T) {
	disabledApp, disabledToken := newHostedTestApp(t, nil, nil, []string{auth.ScopeEpisodeWrite}, nil, nil)
	disabledRequest := authenticatedEpisodeRequest(t, disabledToken, episodeCreate(), "idempotency_01")
	disabledResponse := httptest.NewRecorder()
	disabledApp.Handler().ServeHTTP(disabledResponse, disabledRequest)

	deniedApp, deniedToken := hostedEpisodeTestApp(t, &fakeEpisodeCreator{err: ErrEpisodeWritebackNotEnabledForOrg}, []string{auth.ScopeEpisodeWrite}, nil)
	deniedRequest := authenticatedEpisodeRequest(t, deniedToken, episodeCreate(), "idempotency_01")
	deniedResponse := httptest.NewRecorder()
	deniedApp.Handler().ServeHTTP(deniedResponse, deniedRequest)

	var disabledEnvelope, deniedEnvelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(disabledResponse.Body.Bytes(), &disabledEnvelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(deniedResponse.Body.Bytes(), &deniedEnvelope); err != nil {
		t.Fatal(err)
	}
	disabledEnvelope.RequestID, deniedEnvelope.RequestID = "", ""
	if disabledResponse.Code != deniedResponse.Code || !reflect.DeepEqual(disabledEnvelope, deniedEnvelope) {
		t.Fatalf("writeback-disabled response (status=%d, %#v) != cohort-denied response (status=%d, %#v) -- cohort membership leaked", disabledResponse.Code, disabledEnvelope, deniedResponse.Code, deniedEnvelope)
	}
}
