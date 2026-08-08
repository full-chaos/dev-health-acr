package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/episode"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// TestEpisodeReadRoutes_reachTheStoreWithAProductionShapedPrincipal is the
// regression test for review finding B1: internal/episode/service.go's
// authorizeRead requires principal.ProductEntitlements to contain
// "agent_context_runtime", but that field is populated NOWHERE in
// production except handleEpisode's own injection (episode_routes.go) --
// requireEntitlement (runtime.go) verifies the org's entitlement via
// a.runtime.Entitlements.HasEntitlement and then calls next.ServeHTTP
// without ever writing that fact back into principal.ProductEntitlements.
// A principal built the real way -- issueScopedCredential, through
// PrincipalFromContext, exactly as a live credential produces it -- always
// has an empty ProductEntitlements. Before the fix, both read handlers
// never injected the marker, so authorizeRead would fail every single
// authorized read with ErrEntitlementRequired, which writeReadDependencyError
// then turned into a generic 503 "dependency failed" -- indistinguishable
// from a real outage. This test uses a REAL episode.Service (not
// fakeEpisodeReader, which every other read test in this package uses and
// which bypasses authorizeRead entirely -- that's exactly what let this bug
// ship) and a token issued through the same credential path production
// uses, over the real mux (app.Handler().ServeHTTP), so it cannot be
// satisfied by a test double that skips the authorization the fix touches.
func TestEpisodeReadRoutes_reachTheStoreWithAProductionShapedPrincipal(t *testing.T) {
	service, err := episode.NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), episode.ServiceOptions{PacketStore: episodePacketStore{}})
	if err != nil {
		t.Fatal(err)
	}
	app, readToken := newHostedTestApp(t, nil, nil, []string{auth.ScopeEpisodeRead}, nil, nil)
	app.runtime.EpisodeReader = service

	// Seed one episode directly through the real service with a
	// manually-entitled principal (test setup, not exercising the bug --
	// the bug is specifically that the HTTP read handlers never perform
	// this injection themselves).
	seedPrincipal := storage.Principal{
		OrgID: "org_1", RepositoryScopes: []string{hostedTestRepository},
		Permissions: []string{auth.ScopeEpisodeWrite}, ProductEntitlements: []string{"agent_context_runtime"},
	}
	created, _, err := service.Create(context.Background(), seedPrincipal, episodeCreate())
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}

	t.Run("GetByID", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/"+created.EpisodeID, nil)
		request.Header.Set("Authorization", "Bearer "+readToken)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		response := httptest.NewRecorder()

		app.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s -- a production-shaped episode:read credential could not read its own episode", response.Code, response.Body.String())
		}
		var got contractsv1.AgentEpisode
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.EpisodeID != created.EpisodeID {
			t.Fatalf("episode = %#v, want id %q", got, created.EpisodeID)
		}
	})

	t.Run("List", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes", nil)
		request.Header.Set("Authorization", "Bearer "+readToken)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		response := httptest.NewRecorder()

		app.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s -- a production-shaped episode:read credential could not list its own episodes", response.Code, response.Body.String())
		}
		var got []contractsv1.AgentEpisode
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].EpisodeID != created.EpisodeID {
			t.Fatalf("episodes = %#v, want exactly the seeded episode %q", got, created.EpisodeID)
		}
	})
}
