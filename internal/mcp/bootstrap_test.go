package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func setFixtureEnv(t *testing.T, fx *fixtureServer, token string) {
	t.Helper()
	cfg := fixtureConfig(t, fx.Server)
	t.Setenv(sidecar.APIURLEnvironment, cfg.APIBaseURL.String())
	t.Setenv(sidecar.CACertPathEnvironment, cfg.CACertPath)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, boolEnv(cfg.AllowInsecureLoopback))
	t.Setenv(sidecar.TokenEnvironment, token)
	t.Setenv(sidecar.SidecarVersionEnvironment, "1.0.0")
}

func boolEnv(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestNewBootstrapSucceedsWithCompatibleFixture(t *testing.T) {
	fx := newFixtureServer(t)
	setFixtureEnv(t, fx, fixtureToken(1))

	boot, err := NewBootstrap(context.Background(), "dev")
	if err != nil {
		t.Fatalf("expected bootstrap to succeed, got: %v", err)
	}
	if boot.Capabilities.Service != "dev-health-acr" {
		t.Fatalf("unexpected capabilities: %#v", boot.Capabilities)
	}
}

func TestNewBootstrapAllowsPreWritebackHostByDefault(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	setFixtureEnv(t, fx, fixtureToken(18))

	// When
	_, err := NewBootstrap(context.Background(), "dev")

	// Then
	if err != nil {
		t.Fatalf("expected default read-only bootstrap to accept a pre-writeback host, got: %v", err)
	}
}

func TestNewBootstrapFailsClosedForWritebackAgainstPreWritebackHost(t *testing.T) {
	// Given
	fx := newFixtureServer(t)
	setFixtureEnv(t, fx, fixtureToken(19))
	t.Setenv(sidecar.EnableWritebackEnvironment, "true")

	// When
	_, err := NewBootstrap(context.Background(), "dev")

	// Then
	if err == nil || !strings.Contains(err.Error(), "writeback") {
		t.Fatalf("expected writeback compatibility failure, got: %v", err)
	}
}

func TestNewBootstrapFailsClearlyOnMissingCredential(t *testing.T) {
	fx := newFixtureServer(t)
	setFixtureEnv(t, fx, "")
	t.Setenv(sidecar.TokenFileEnvironment, "")

	_, err := NewBootstrap(context.Background(), "dev")
	if err == nil {
		t.Fatal("expected bootstrap to fail with no credential configured")
	}
	if strings.Contains(err.Error(), "fcacr_") {
		t.Fatalf("bootstrap error leaked a token shape: %v", err)
	}
}

func TestNewBootstrapFailsOnInvalidConfigWithoutLeakingURL(t *testing.T) {
	secret := "s3cr3t-userinfo"
	t.Setenv(sidecar.APIURLEnvironment, "https://user:"+secret+"@acr.example.test")
	t.Setenv(sidecar.TokenEnvironment, fixtureToken(2))

	_, err := NewBootstrap(context.Background(), "dev")
	if err == nil {
		t.Fatal("expected bootstrap to reject a URL with embedded userinfo")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("bootstrap error leaked embedded userinfo: %v", err)
	}
}

func TestNewBootstrapFailsOnCapabilityMismatchBeforeAnyToolCall(t *testing.T) {
	fx := newFixtureServer(t)
	fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
		caps := validCapabilitiesFixture()
		caps.MinimumSidecarVersion = "99.0.0"
		writeJSONFixture(t, w, http.StatusOK, caps)
	}
	setFixtureEnv(t, fx, fixtureToken(3))

	_, err := NewBootstrap(context.Background(), "dev")
	if err == nil {
		t.Fatal("expected bootstrap to fail on a version-incompatible capabilities response")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected a version incompatibility error, got: %v", err)
	}
}

func TestNewBootstrapFailsOnAuthErrorFromCapabilities(t *testing.T) {
	fx := newFixtureServer(t)
	fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
		writeErrorFixture(t, w, http.StatusUnauthorized, "invalid_token", false)
	}
	setFixtureEnv(t, fx, fixtureToken(4))

	_, err := NewBootstrap(context.Background(), "dev")
	if err == nil {
		t.Fatal("expected bootstrap to fail on an auth error from capabilities")
	}
}

// TestNewBootstrapFailsClosedOnStaleBinaryVersionWhenEnvUnset locks the
// fail-closed fix end to end: with ACR_SIDECAR_VERSION left unset (so
// sidecar.LoadConfig's own "dev" default is in play), a real compiled
// binary version that is genuinely older than the hosted API's minimum
// must still fail bootstrap -- not silently pass the way an unset env
// var used to before effectiveSidecarVersion existed.
func TestNewBootstrapFailsClosedOnStaleBinaryVersionWhenEnvUnset(t *testing.T) {
	fx := newFixtureServer(t)
	cfg := fixtureConfig(t, fx.Server)
	t.Setenv(sidecar.APIURLEnvironment, cfg.APIBaseURL.String())
	t.Setenv(sidecar.CACertPathEnvironment, cfg.CACertPath)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, boolEnv(cfg.AllowInsecureLoopback))
	t.Setenv(sidecar.TokenEnvironment, fixtureToken(5))
	// Deliberately not setting sidecar.SidecarVersionEnvironment: this is
	// the "operator forgot to configure it" scenario the fix targets.
	fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
		caps := validCapabilitiesFixture()
		caps.MinimumSidecarVersion = "1.0.0"
		writeJSONFixture(t, w, http.StatusOK, caps)
	}

	_, err := NewBootstrap(context.Background(), "0.5.0")
	if err == nil {
		t.Fatal("expected bootstrap to fail closed on a stale real binary version with no env override")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected a version incompatibility error, got: %v", err)
	}
}
