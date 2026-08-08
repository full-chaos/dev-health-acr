package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
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

// TestEpisodeReadErrors_authRefusalsAreNonRetryableAndNotLoggedAsError is
// the regression test for review finding H2: authorizeRead's auth-refusal
// sentinels (repository scope, episode:read scope, runtime entitlement)
// previously fell into writeReadDependencyError's generic default -- 503
// upstream_unavailable, retryable:true, ERROR "hosted read dependency
// failed" -- indistinguishable from a real outage. RED: this test failed
// (503 instead of 403) against the pre-fix default-case handling for all
// three sentinels; GREEN after writeEpisodeReadError classified them
// explicitly.
func TestEpisodeReadErrors_authRefusalsAreNonRetryableAndNotLoggedAsError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{name: "repository forbidden", err: auth.ErrRepositoryForbidden, wantCode: "repo_forbidden"},
		{name: "insufficient scope", err: auth.ErrInsufficientScope, wantCode: "insufficient_scope"},
		{name: "entitlement required", err: episode.ErrEntitlementRequired, wantCode: "feature_not_enabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer
			reader := &fakeEpisodeReader{getErr: test.err}
			app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})
			app.logger = testLogger(&buffer)

			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/episode_server_01", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.Code)
			}
			var envelope contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", envelope.Error.Code, test.wantCode)
			}
			if envelope.Error.Retryable {
				t.Fatalf("auth refusal envelope retryable = true, want false: %#v", envelope)
			}
			if strings.Contains(buffer.String(), `"level":"ERROR"`) {
				t.Fatalf("auth refusal logged at ERROR level, want non-ERROR: %s", buffer.String())
			}
		})
	}
}

// episodeReadRouteRequests is table-driven across both episode read routes
// so the app.go wiring for each (protectedRuntimeHandler(..., entitlement,
// allowWebAssertions, ...)) is exercised identically -- review findings H4
// and M5 flagged that flipping either bool on either route left the whole
// suite green.
var episodeReadRouteRequests = []struct {
	name string
	path string
}{
	{name: "GetByID", path: "/api/v1/agent-context/episodes/episode_server_01"},
	{name: "List", path: "/api/v1/agent-context/episodes"},
}

// TestEpisodeReadRoutes_requireEntitlement is the regression test for
// review finding H4: app.go wires both episode read routes with
// protectedRuntimeHandler's entitlement argument as true, but nothing
// asserted that -- flipping it to false left the suite green. A credential
// with the episode:read scope but no agent_context_runtime entitlement must
// be rejected before ever reaching the reader.
func TestEpisodeReadRoutes_requireEntitlement(t *testing.T) {
	for _, test := range episodeReadRouteRequests {
		t.Run(test.name, func(t *testing.T) {
			entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return false, nil })
			reader := &fakeEpisodeReader{episode: storedEpisode()}
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeEpisodeRead}, entitlements, nil)
			app.runtime.EpisodeReader = reader

			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			assertErrorResponse(t, response, http.StatusForbidden, "feature_not_enabled")
			if reader.gotEpisodeID != "" || reader.listRepository != "" {
				t.Fatalf("reader was reached without the required entitlement: %#v", reader)
			}
		})
	}
}

// TestEpisodeReadRoutes_rejectWebAssertions is the regression test for
// review finding M5: app.go wires both episode read routes with
// allowWebAssertions=false, but nothing asserted that -- flipping it to
// true left the suite green. A request carrying only a signed web
// assertion (no bearer token) must be rejected the same way
// TestWebAssertion_cannotAuthenticateEpisodeWrite already proves for the
// write route.
func TestEpisodeReadRoutes_rejectWebAssertions(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range episodeReadRouteRequests {
		t.Run(test.name, func(t *testing.T) {
			app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
			app.runtime.EpisodeReader = &fakeEpisodeReader{episode: storedEpisode()}

			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			request.Header.Set(auth.WebAssertionHeader, signAPIAssertionAt(t, private, request, nil, "read_"+test.name, now))

			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
		})
	}
}
