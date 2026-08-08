package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

// This file is the regression suite for review finding M7: surviving
// mutants on handleGetEpisode/handleListEpisodes' own validation and
// normalization logic (episode_id trim/length, repository slug
// normalization, limit<0 rejection, nil-episodes normalization, and
// CompleteUsage accounting), none of which any existing test distinguished
// from a mutant.

// TestGetEpisode_rejectsEpisodeIDOutsideLengthBounds kills the episode_id
// trim/length mutants: each clause of
// `strings.TrimSpace(episodeID) != episodeID || len(...) < 8 || len(...) > 256`
// is tested independently, so a mutation to any one clause fails only its
// own case, not the others.
func TestGetEpisode_rejectsEpisodeIDOutsideLengthBounds(t *testing.T) {
	tests := []struct {
		name      string
		episodeID string
		reachable bool
	}{
		{name: "too short (7 runes)", episodeID: "1234567", reachable: false},
		{name: "exactly 8 runes", episodeID: "12345678", reachable: true},
		{name: "exactly 256 runes", episodeID: strings.Repeat("a", 256), reachable: true},
		{name: "too long (257 runes)", episodeID: strings.Repeat("a", 257), reachable: false},
		{name: "leading whitespace", episodeID: "%20abcdefgh", reachable: false},
		{name: "trailing whitespace", episodeID: "abcdefgh%20", reachable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeEpisodeReader{episode: storedEpisode()}
			app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/"+test.episodeID, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)

			reached := reader.gotEpisodeID != ""
			if reached != test.reachable {
				t.Fatalf("episode id %q: reader reached = %t, want %t (status=%d body=%s)", test.episodeID, reached, test.reachable, response.Code, response.Body.String())
			}
			if !test.reachable && response.Code != http.StatusNotFound {
				t.Fatalf("episode id %q: status = %d, want 404 (rejected out-of-bounds ids collapse to not-found like every other read denial)", test.episodeID, response.Code)
			}
		})
	}
}

// TestListEpisodes_normalizesRepositoryCaseBeforeReachingTheStore kills the
// NormalizeRepositorySlug mutant: this normalization is load-bearing, not
// cosmetic -- storage/postgres's List matches repo_slug by exact SQL
// equality, so a mixed-case query value only works because the handler
// lowercases it before the store ever sees it. Verified against the fake
// reader (which just records what it received) rather than a live
// Postgres, since the object under test is specifically what the HANDLER
// passes down, not what the store does with it afterward.
func TestListEpisodes_normalizesRepositoryCaseBeforeReachingTheStore(t *testing.T) {
	reader := &fakeEpisodeReader{list: []contractsv1.AgentEpisode{}}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	mixedCase := "Example-Org/Widget-Service"
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes?repository="+mixedCase, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if reader.listRepository != hostedTestRepository {
		t.Fatalf("reader saw repository = %q, want normalized %q (mixed-case %q must be lowercased before reaching the store)", reader.listRepository, hostedTestRepository, mixedCase)
	}
}

// TestListEpisodes_rejectsNegativeLimit kills the `parsed < 0` mutant,
// distinct from TestListEpisodes_rejectsInvalidLimit's non-numeric case:
// strconv.Atoi("-1") succeeds (convErr == nil), so only the parsed<0 clause
// itself rejects a syntactically valid but semantically invalid negative
// limit.
func TestListEpisodes_rejectsNegativeLimit(t *testing.T) {
	reader := &fakeEpisodeReader{}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes?limit=-1", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
	if reader.listLimit != 0 || reader.gotPrincipal.OrgID != "" {
		t.Fatalf("reader was reached with an invalid negative limit: %#v", reader)
	}
}

// TestListEpisodes_normalizesNilResultsToEmptyJSONArray kills the nil->[]
// normalization mutant: a reader returning a nil slice (fakeEpisodeReader's
// zero value) must still serialize as `[]`, never the bare JSON `null` a
// naive encoding of a nil slice would produce.
func TestListEpisodes_normalizesNilResultsToEmptyJSONArray(t *testing.T) {
	reader := &fakeEpisodeReader{} // list left nil
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := strings.TrimSpace(response.Body.String())
	if body != "[]" {
		t.Fatalf("body = %q, want the literal JSON array \"[]\", not null", body)
	}
}

// TestGetEpisode_completeUsageAccountsForResponseBytes and
// TestListEpisodes_completeUsageAccountsForItemCount kill the CompleteUsage
// accounting mutants: a policy with a tight resource budget must actually
// reject when the real response would exceed it, proving the values passed
// to CompleteUsage reflect the real encoded response, not a hardcoded
// placeholder.
func TestGetEpisode_completeUsageAccountsForResponseBytes(t *testing.T) {
	reader := &fakeEpisodeReader{episode: storedEpisode()}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})
	app.limits = tightEpisodeResourceLimitManager(t, limits.ResourceBudget{MaxBytes: 10})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/episode_server_01", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (the real ~hundreds-of-bytes episode response must exceed a 10-byte budget), body = %s", response.Code, response.Body.String())
	}
}

func TestListEpisodes_completeUsageAccountsForItemCount(t *testing.T) {
	reader := &fakeEpisodeReader{list: []contractsv1.AgentEpisode{storedEpisode(), storedEpisode()}}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})
	app.limits = tightEpisodeResourceLimitManager(t, limits.ResourceBudget{MaxItems: 1})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (2 listed episodes must exceed a 1-item budget), body = %s", response.Code, response.Body.String())
	}
}

func tightEpisodeResourceLimitManager(t *testing.T, budget limits.ResourceBudget) *limits.Manager {
	t.Helper()
	manager, err := limits.NewManager(limits.Options{
		Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) },
		Policies: limits.PolicySet{
			Episode: limits.EpisodePolicy{Window: time.Minute, Resources: budget},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// TestListEpisodes_clampsOverflowingPositiveLimitInsteadOfRejecting is the
// regression test for the Codex cross-system review finding X7: the
// OpenAPI contract (L8) documents limit as "0 (or omitted) uses the
// service default; values above the service maximum are silently clamped,
// never rejected" -- true for any in-range value (e.g. limit=10000 clamps
// to maxEpisodeListLimit downstream in the store), but a limit string that
// overflows Go's int range (strconv.Atoi returns math.MaxInt with
// strconv.ErrRange, not a syntax error) was previously treated as
// convErr != nil and rejected with 400, contradicting the documented
// "never rejected" contract for a value that is still, in every meaningful
// sense, just a very large valid limit.
func TestListEpisodes_clampsOverflowingPositiveLimitInsteadOfRejecting(t *testing.T) {
	reader := &fakeEpisodeReader{list: []contractsv1.AgentEpisode{}}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes?limit=999999999999999999999999999999", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an overflowing positive limit must clamp, not reject), body = %s", response.Code, response.Body.String())
	}
	if reader.listLimit != math.MaxInt {
		t.Fatalf("reader saw limit = %d, want math.MaxInt (strconv.Atoi's own overflow clamp, which the store then bounds to its own maximum)", reader.listLimit)
	}
}

// TestListEpisodes_rejectsOverflowingNegativeLimit proves the X7 fix
// doesn't overcorrect: a limit string that overflows AND is negative is
// still a semantically invalid limit (the same class as limit=-1), not
// merely "too large", so it must still be rejected.
func TestListEpisodes_rejectsOverflowingNegativeLimit(t *testing.T) {
	reader := &fakeEpisodeReader{}
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes?limit=-999999999999999999999999999999", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
	}
	if reader.listLimit != 0 || reader.gotPrincipal.OrgID != "" {
		t.Fatalf("reader was reached with an invalid overflowing-negative limit: %#v", reader)
	}
}

// TestGetEpisode_rejectsCorruptedStoredPayload and
// TestListEpisodes_rejectsCorruptedStoredPayload are the regression tests
// for the Codex cross-system review finding X9: handleGetEpisode/
// handleListEpisodes encoded whatever contractsv1.AgentEpisode the reader
// returned directly, without calling .Validate() first -- unlike the write
// path (handleEpisode), which validates its creator's output before
// encoding it (episode_routes.go, "episode creator returned invalid
// output"). A corrupted stored row (e.g. a schema migration gap, a bad
// manual DB edit, or a future store bug) would silently violate the
// agent_episode.v1 response contract instead of failing loudly as an
// internal/dependency error.
func TestGetEpisode_rejectsCorruptedStoredPayload(t *testing.T) {
	reader := &fakeEpisodeReader{episode: contractsv1.AgentEpisode{}} // zero value: fails Validate()
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes/episode_server_01", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a corrupted stored episode must never be served as a valid response), body = %s", response.Code, response.Body.String())
	}
}

func TestListEpisodes_rejectsCorruptedStoredPayload(t *testing.T) {
	reader := &fakeEpisodeReader{list: []contractsv1.AgentEpisode{storedEpisode(), {}}} // second entry: zero value, fails Validate()
	app, token := hostedEpisodeReaderTestApp(t, reader, []string{auth.ScopeEpisodeRead})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/episodes", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a list containing a corrupted stored episode must never be served), body = %s", response.Code, response.Body.String())
	}
}
