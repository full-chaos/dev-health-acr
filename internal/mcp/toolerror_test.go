package mcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestClassifyMapsContextErrors(t *testing.T) {
	if ce := classify(context.Canceled); ce.category != "cancelled" {
		t.Fatalf("expected cancelled, got %q", ce.category)
	}
	if ce := classify(context.DeadlineExceeded); ce.category != "timeout" {
		t.Fatalf("expected timeout, got %q", ce.category)
	}
}

func TestClassifyWorkspaceErrorsAreValidation(t *testing.T) {
	ce := classify(sidecar.ErrNotGitRepository)
	if ce.category != "validation" {
		t.Fatalf("expected validation, got %q", ce.category)
	}
}

func TestClassifyCredentialShapeIsAuth(t *testing.T) {
	ce := classify(sidecar.ErrCredentialShapeInvalid)
	if ce.category != "auth" {
		t.Fatalf("expected auth, got %q", ce.category)
	}
}

// TestClassifyCredentialMissingIsAuth locks the fix for the "missing
// credential collapses into internal" defect: sidecar.ErrCredentialMissing
// (nothing configured at all -- see internal/sidecar/credential.go's
// loadFromFile) must land in the same "auth" category as
// ErrCredentialShapeInvalid (something configured, but wrong), not the
// generic internal fallback.
func TestClassifyCredentialMissingIsAuth(t *testing.T) {
	ce := classify(sidecar.ErrCredentialMissing)
	if ce.category != "auth" {
		t.Fatalf("expected auth, got %q", ce.category)
	}
}

// TestClassifyMapsHostedAPIErrorCodesToDistinctCategories drives classify()
// through a real hosted API call against an httptest fixture for every wire
// error code this client recognizes, proving CHAOS-2908's required distinct
// categories: repo_forbidden is no longer collapsed into entitlement
// (insufficient_scope and feature_not_enabled remain there), and every
// other code keeps its own established category. Every message is asserted
// sanitized: the fixture's bearer token and the raw fixture detail text
// (see writeErrorFixture) must never appear in ce.Error().
func TestClassifyMapsHostedAPIErrorCodesToDistinctCategories(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		code         string
		wantCategory string
	}{
		{"invalid token", http.StatusUnauthorized, "invalid_token", "auth"},
		{"repo forbidden", http.StatusForbidden, "repo_forbidden", "repo_forbidden"},
		{"insufficient scope", http.StatusForbidden, "insufficient_scope", "entitlement"},
		{"feature not enabled (entitlement)", http.StatusForbidden, "feature_not_enabled", "entitlement"},
		{"no data", http.StatusNotFound, "not_found", "no_data"},
		{"rate limit", http.StatusTooManyRequests, "rate_limited", "rate_limit"},
		{"incompatibility (version mismatch)", http.StatusBadRequest, "version_mismatch", "version"},
		{"unavailable (upstream)", http.StatusServiceUnavailable, "upstream_unavailable", "unavailable"},
		{"unavailable (internal_error code)", http.StatusInternalServerError, "internal_error", "unavailable"},
	}

	token := fixtureToken(0x5A)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixtureServer(t)
			fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
				writeErrorFixture(t, w, tc.status, tc.code, false)
			}
			cfg := fixtureConfig(t, fx.Server)
			client, err := sidecar.NewClient(cfg, fixedCredentialSource(token))
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := client.Capabilities(context.Background())
			if callErr == nil {
				t.Fatal("expected the fixture error response to produce an error")
			}

			ce := classify(callErr)
			if ce.category != tc.wantCategory {
				t.Fatalf("expected category %q, got %q (%v)", tc.wantCategory, ce.category, ce)
			}
			if strings.Contains(ce.Error(), token) {
				t.Fatalf("classified error leaked the bearer token: %v", ce)
			}
			if strings.Contains(ce.Error(), "fixture "+tc.code) {
				t.Fatalf("classified error leaked the raw hosted response detail text: %v", ce)
			}
		})
	}
}

func TestClassifyUnknownErrorFallsBackToSafeInternal(t *testing.T) {
	secret := "fcacr_totally-secret-token-should-never-appear"
	raw := errors.New("load ACR credential: read /Users/whoever/.acr-token: permission denied " + secret)
	ce := classify(raw)
	if ce.category != "internal" {
		t.Fatalf("expected internal, got %q", ce.category)
	}
	if strings.Contains(ce.Error(), secret) {
		t.Fatalf("classified error leaked raw error text: %v", ce)
	}
	if strings.Contains(ce.Error(), "/Users/whoever") {
		t.Fatalf("classified error leaked a filesystem path: %v", ce)
	}
}

func TestToolErrorResultSetsIsError(t *testing.T) {
	result := toolErrorResult(sidecar.ErrNotFound)
	if !result.IsError {
		t.Fatal("expected IsError to be true")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected error content to be populated")
	}
}

// TestClassifyMapsConnectionFailureToUnavailable is the CHAOS-2908
// rereview regression lock: a real transport-level connection failure
// (a closed listener) drives all the way through sidecar.Client.call and
// classify() and must land in "unavailable", never the generic
// "internal" fallback -- a network/TLS/connection problem is not the
// same failure class as an unrecognized local bug.
func TestClassifyMapsConnectionFailureToUnavailable(t *testing.T) {
	fx := newFixtureServer(t)
	cfg := fixtureConfig(t, fx.Server)
	fx.Server.Close()

	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0x5B)))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	if callErr == nil {
		t.Fatal("expected a connection failure against a closed listener")
	}

	ce := classify(callErr)
	if ce.category != "unavailable" {
		t.Fatalf("expected unavailable, got %q", ce.category)
	}
	if strings.Contains(ce.Error(), cfg.APIBaseURL.Host) {
		t.Fatalf("classified error leaked the configured host: %v", ce)
	}
}
