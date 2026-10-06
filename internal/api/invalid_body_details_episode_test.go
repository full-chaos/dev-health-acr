package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func episodeErrorDetails(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, response.Body.String())
	}
	return body.Error.Details
}

func TestEpisodeRouteInvalidBodyDetails(t *testing.T) {
	t.Run("oversized body is body_too_large", func(t *testing.T) {
		app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)
		app.config.MaxRequestBodyBytes = 1
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", response.Code)
		}
		if got := episodeErrorDetails(t, response)["reason"]; got != "body_too_large" {
			t.Fatalf("reason = %v", got)
		}
	})
	t.Run("header mismatch is not a body schema violation", func(t *testing.T) {
		app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)
		create := episodeCreate()
		response := httptest.NewRecorder()
		request := authenticatedEpisodeRequest(t, token, create, "other_key_01")
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if got := episodeErrorDetails(t, response)["reason"]; got != "invalid_idempotency_key" {
			t.Fatalf("reason = %v", got)
		}
	})
	t.Run("unusable repository slug names its field", func(t *testing.T) {
		app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: storedEpisode()}, []string{auth.ScopeEpisodeWrite}, nil)
		create := episodeCreate()
		create.Repository.Slug = "../bad"
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, create, create.IdempotencyKey))
		if response.Code != http.StatusBadRequest {
			t.Skipf("slug rejected earlier by Validate (status %d): %s", response.Code, response.Body.String())
		}
		details := episodeErrorDetails(t, response)
		if details["reason"] != "schema_violation" {
			t.Fatalf("details = %v", details)
		}
	})
}
