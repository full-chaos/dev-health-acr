package mcp_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-go/authverify"
)

// CHAOS-12094 (F1), through the real login and the real endpoint: with the
// hop required, the user's MCP-bound credential works through acr-mcp (which
// proves its hop) and is refused when its holder calls acr-api directly with
// the MCP resource header set by hand.

type stackHopValidator map[string]authverify.SubjectIdentity

func (v stackHopValidator) Validate(_ context.Context, token string) (authverify.SubjectIdentity, error) {
	identity, ok := v[token]
	if !ok {
		return authverify.SubjectIdentity{}, authverify.ErrSubjectTokenInvalid
	}
	return identity, nil
}

// direct calls acr-api the way a holder of the bearer can: the MCP resource
// header set by hand, and optionally a hop.
func (s *oauthStack) direct(t *testing.T, bearer, hop string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, s.api.URL+"/api/v1/agent-context/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set(auth.ResourceHeader, s.mcpURL)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	if hop != "" {
		request.Header.Set(auth.MCPHopHeader, hop)
	}
	response, err := s.api.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func newHopStack(t *testing.T, required bool) (*oauthStack, string) {
	t.Helper()
	hopFile := filepath.Join(t.TempDir(), "hop-token")
	if err := os.WriteFile(hopFile, []byte("hop-token-acr-mcp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var policy *auth.MCPHopPolicy
	if required {
		var err error
		policy, err = auth.NewMCPHopPolicy(auth.MCPHopPolicyOptions{
			Validator: stackHopValidator{
				"hop-token-acr-mcp": {Namespace: "acr", ServiceAccountName: "acr-mcp", ExpiresAt: time.Now().Add(10 * time.Minute)},
				"hop-token-other":   {Namespace: "acr", ServiceAccountName: "acr-api", ExpiresAt: time.Now().Add(10 * time.Minute)},
			},
			Namespace: "acr", ServiceAccount: "acr-mcp", Issuer: "https://acr.issuer.example.test",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return newOAuthStackWith(t, policy, hopFile), hopFile
}

// Red on main (no hop check): the MCP-bound credential works directly on
// acr-api once its holder sets the MCP resource header.
func TestOAuthMCPBoundBearerWorksDirectlyWithoutTheHopCheck(t *testing.T) {
	s, _ := newHopStack(t, false)
	token := s.manualLogin(t, s.mcpURL, true)
	if got := s.direct(t, token, ""); got != http.StatusOK {
		t.Fatalf("direct call with the caller-set MCP resource and no hop check = %d, want 200 (today's gap)", got)
	}
}

func TestOAuthMCPBoundBearerIsServedOnlyThroughTheEndpointWhenTheHopIsRequired(t *testing.T) {
	s, hopFile := newHopStack(t, true)
	token := s.manualLogin(t, s.mcpURL, true)
	if credential := s.credentialFor(t, token); credential.Resource != s.mcpURL {
		t.Fatalf("credential resource = %q, want %q", credential.Resource, s.mcpURL)
	}

	// Through acr-mcp, which proves its hop: served.
	if status, _ := s.rawMCP(token); status != http.StatusOK {
		t.Fatalf("the MCP-bound credential through acr-mcp = %d, want 200", status)
	}
	// Directly on acr-api, resource header set by hand: refused, with no hop
	// and with a hop of another ServiceAccount; a valid hop is served.
	for name, hop := range map[string]string{"no hop": "", "another serviceaccount": "hop-token-other", "unknown token": "hop-token-guess"} {
		if got := s.direct(t, token, hop); got != http.StatusUnauthorized {
			t.Fatalf("direct call, %s = %d, want 401", name, got)
		}
	}
	if got := s.direct(t, token, "hop-token-acr-mcp"); got != http.StatusOK {
		t.Fatalf("direct call with acr-mcp's valid hop = %d, want 200", got)
	}

	// The endpoint reads the file fresh: a token it cannot prove is refused.
	if err := os.WriteFile(hopFile, []byte("hop-token-other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, challenge := s.rawMCP(token); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("through acr-mcp with another ServiceAccount's token: %d %q, want 401 invalid_token", status, challenge)
	}
	if err := os.WriteFile(hopFile, []byte("hop-token-acr-mcp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _ := s.rawMCP(token); status != http.StatusOK {
		t.Fatalf("through acr-mcp after the token file rotated back = %d, want 200", status)
	}
}
