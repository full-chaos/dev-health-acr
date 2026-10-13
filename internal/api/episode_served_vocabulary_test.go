package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

func TestCreateEpisodeRejectsCreatorOutputWithAnUnservedRedactionState(t *testing.T) {
	episode := storedEpisode()
	episode.RedactionState = "value_added_after_the_client_cached_the_schema"
	app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{episode: episode}, []string{auth.ScopeEpisodeWrite}, nil)

	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: the episode route served a redaction_state outside its own vocabulary", response.Code, http.StatusInternalServerError)
	}
}
