package sidecar

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// CHAOS-12094: with a hop token file the client sends the token, read fresh,
// on every hosted API call; a token it cannot read stops the call before it
// is sent; without the file no hop is sent.
func TestHostedCallsCarryTheHopTokenOnlyWhenConfigured(t *testing.T) {
	var calls atomic.Int64
	var lastHop atomic.Value
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastHop.Store(strings.Join(r.Header.Values(auth.MCPHopHeader), "|"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"capabilities.v1"}`))
	}))
	t.Cleanup(server.Close)
	hopFile := filepath.Join(t.TempDir(), "token")
	cfg := newFixtureConfig(t, server)

	plain, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = plain.Capabilities(t.Context())
	if calls.Load() != 1 || lastHop.Load() != "" {
		t.Fatalf("no hop file: calls=%d hop=%q, want one call and no hop", calls.Load(), lastHop.Load())
	}

	cfg.HopTokenFile = hopFile
	hopped, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = hopped.Capabilities(t.Context())
	if !errors.Is(err, ErrHopTokenUnavailable) || calls.Load() != 1 {
		t.Fatalf("absent hop file: err=%v calls=%d, want ErrHopTokenUnavailable and no call", err, calls.Load())
	}
	if strings.Contains(err.Error(), hopFile) {
		t.Fatalf("the error names the path: %v", err)
	}
	for name, content := range map[string]string{"empty": " \n", "two lines": "a\nb", "oversized": strings.Repeat("x", maxHopTokenBytes+1)} {
		if err := os.WriteFile(hopFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := hopped.Capabilities(t.Context()); !errors.Is(err, ErrHopTokenUnavailable) || calls.Load() != 1 {
			t.Fatalf("%s hop file: err=%v calls=%d, want ErrHopTokenUnavailable and no call", name, err, calls.Load())
		}
	}
	for _, token := range []string{"token-one", "token-two"} {
		if err := os.WriteFile(hopFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = hopped.Capabilities(t.Context())
		if lastHop.Load() != token {
			t.Fatalf("hop sent = %q, want the file's current token %q", lastHop.Load(), token)
		}
	}
}
