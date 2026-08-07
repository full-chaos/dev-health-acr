package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type fakeEpisodeReader struct {
	episode        contractsv1.AgentEpisode
	getErr         error
	list           []contractsv1.AgentEpisode
	listErr        error
	gotPrincipal   storage.Principal
	gotEpisodeID   string
	listRepository string
	listLimit      int
}

func (f *fakeEpisodeReader) GetByID(_ context.Context, principal storage.Principal, episodeID string) (contractsv1.AgentEpisode, error) {
	f.gotPrincipal, f.gotEpisodeID = principal, episodeID
	return f.episode, f.getErr
}

func (f *fakeEpisodeReader) List(_ context.Context, principal storage.Principal, repositorySlug string, limit int) ([]contractsv1.AgentEpisode, error) {
	f.gotPrincipal, f.listRepository, f.listLimit = principal, repositorySlug, limit
	return f.list, f.listErr
}

func hostedEpisodeReaderTestApp(t *testing.T, reader *fakeEpisodeReader, scopes []string) (*App, string) {
	t.Helper()
	app, token := newHostedTestApp(t, nil, nil, scopes, nil, nil)
	app.runtime.EpisodeReader = reader
	return app, token
}

func TestGetEpisode_returnsAuthorizedEpisode(t *testing.T) {
	reader := &fakeEpisodeReader{episode: storedEpisode()}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/episode_server_01", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got contractsv1.AgentEpisode
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.EpisodeID != storedEpisode().EpisodeID {
		t.Fatalf("episode = %#v", got)
	}
	if reader.gotEpisodeID != "episode_server_01" {
		t.Fatalf("reader saw episode id = %q", reader.gotEpisodeID)
	}
}

func TestGetEpisode_missingEpisodeIsNotFound(t *testing.T) {
	reader := &fakeEpisodeReader{getErr: storage.ErrNotFound}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/does-not-exist", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestGetEpisode_requiresEpisodeReadScope(t *testing.T) {
	reader := &fakeEpisodeReader{episode: storedEpisode()}
	// A credential with only episode:write must not be able to read episodes
	// back -- read and write are independent grants.
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeWrite})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/episode_server_01", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (insufficient_scope), body = %s", response.Code, response.Body.String())
	}
}

func TestGetEpisode_returnsUnavailableWithoutReader(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeEpisodeRead}, nil, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/episode_server_01", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body = %s", response.Code, response.Body.String())
	}
}

func TestListEpisodes_returnsAuthorizedList(t *testing.T) {
	reader := &fakeEpisodeReader{list: []contractsv1.AgentEpisode{storedEpisode()}}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes?repository="+hostedTestRepository+"&limit=5", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got []contractsv1.AgentEpisode
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].EpisodeID != storedEpisode().EpisodeID {
		t.Fatalf("list = %#v", got)
	}
	if reader.listRepository != hostedTestRepository || reader.listLimit != 5 {
		t.Fatalf("reader saw repository=%q limit=%d", reader.listRepository, reader.listLimit)
	}
}

func TestListEpisodes_rejectsInvalidLimit(t *testing.T) {
	reader := &fakeEpisodeReader{}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes?limit=not-a-number", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
}

func TestListEpisodes_requiresEpisodeReadScope(t *testing.T) {
	reader := &fakeEpisodeReader{}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeWrite})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", response.Code, response.Body.String())
	}
}
