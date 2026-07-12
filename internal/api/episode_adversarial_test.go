package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

func TestCreateEpisodeRejectsMultipleIdempotencyKeyHeaders(t *testing.T) {
	// Given
	app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)
	request := authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01")
	request.Header.Add("Idempotency-Key", "idempotency_01")

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	// Then
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCreateEpisodeReturnsCanonicalEpisodeResponse(t *testing.T) {
	// Given
	app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	var episode contractsv1.AgentEpisode
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &episode) != nil || episode.Validate() != nil {
		t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
	}
}

func TestCreateEpisodeMarksIdempotentReplayInCanonicalResponse(t *testing.T) {
	// Given
	app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode(), duplicate: true}, []string{auth.ScopeEpisodeWrite}, nil)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	var episode contractsv1.AgentEpisode
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &episode) != nil || !episode.Duplicate {
		t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
	}
}

func TestCreateEpisodeMapsServiceValidationToBadRequest(t *testing.T) {
	// Given
	app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{err: errors.New("invalid episode: invalid artifact URI")}, []string{auth.ScopeEpisodeWrite}, nil)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCreateEpisodeReturnsSafeFailureWhenCreatorIsCanceled(t *testing.T) {
	// Given
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	app, token := newHostedTestApp(t, nil, &hooks, []string{auth.ScopeEpisodeWrite}, nil, nil)
	app.runtime.Episodes = &fakeEpisodeCreator{err: context.Canceled}

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if observation := sink.only(t); observation.Operation != observability.OperationEpisode || observation.HTTPStatusClass != observability.HTTPStatus5xx || observation.Outcome != observability.OutcomeFailure {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestCreateEpisodeRejectsContractInvalidRequestBeforeCreator(t *testing.T) {
	// Given
	creator := &fakeEpisodeCreator{episode: storedEpisode()}
	app, token := hostedEpisodeTestApp(t, creator, []string{auth.ScopeEpisodeWrite}, nil)
	create := episodeCreate()
	create.Outcome = "not_a_contract_outcome"

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, create, "idempotency_01"))

	// Then
	if response.Code != http.StatusBadRequest || creator.create.ClientEpisodeID != "" {
		t.Fatalf("status=%d creator=%#v", response.Code, creator.create)
	}
}

func TestCreateEpisodeRejectsContractInvalidCreatorOutput(t *testing.T) {
	// Given
	app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)
	app.runtime.Episodes = &fakeEpisodeCreator{}

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	// Then
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}
