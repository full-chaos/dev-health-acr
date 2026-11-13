package api

import (
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// CHAOS-13612: web #1956 signs repository_scopes ["*"] for an org-wide reader
// with exactly these permission sets (web src/lib/acr/client.ts capabilities,
// contextPacket, evidence).
func TestWebAssertionGolden_organizationWideRead(t *testing.T) {
	h := newGoldenHarness(t)
	packet := goldenPacketBody(t, hostedTestRepository)
	const (
		capabilities = "/api/v1/agent-context/capabilities"
		evidence     = "/api/v1/agent-context/evidence/evidence-ref-00000001"
		acrEvidence  = "/api/v1/agent-context/evidence/acr%3Av1%3Aevidence%3Aabc123"
	)
	C, E, I := auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.WebAssertionPermissionCredentialIssue
	wildcard := func(permissions []string, impersonating bool) func(map[string]any) {
		return func(c map[string]any) {
			c["repository_scopes"] = []string{"*"}
			c["permissions"] = permissions
			if impersonating {
				c["sid"] = "session_123"
				c["impersonating"] = true
			}
		}
	}
	for _, impersonating := range []bool{false, true} {
		accepted := []struct {
			name    string
			request *http.Request
			exact   int
		}{
			{"R1 capabilities C+E", h.signed(http.MethodGet, capabilities, nil, wildcard([]string{C, E}, impersonating)), http.StatusOK},
			{"R2 packets C", h.signed(http.MethodPost, goldenPacket, packet, wildcard([]string{C}, impersonating)), http.StatusOK},
			{"R3 evidence E catalog ref", h.signed(http.MethodGet, evidence, nil, wildcard([]string{E}, impersonating)), 0},
			{"R3 evidence E acr ref", h.signed(http.MethodGet, acrEvidence, nil, wildcard([]string{E}, impersonating)), 0},
		}
		for _, row := range accepted {
			t.Run(row.name+impersonationSuffix(impersonating), func(t *testing.T) {
				response := h.do(row.request)
				t.Logf("status=%d", response.Code)
				if response.Code == http.StatusUnauthorized || response.Code == http.StatusForbidden {
					t.Fatalf("org-wide read refused: %d %s", response.Code, response.Body.String())
				}
				if row.exact != 0 && response.Code != row.exact {
					t.Fatalf("status = %d, want %d: %s", response.Code, row.exact, response.Body.String())
				}
			})
		}
		refused := []struct {
			name    string
			request *http.Request
		}{
			{"R1 capabilities C alone", h.signed(http.MethodGet, capabilities, nil, wildcard([]string{C}, impersonating))},
			{"R1 capabilities E alone", h.signed(http.MethodGet, capabilities, nil, wildcard([]string{E}, impersonating))},
			{"R2 packets E", h.signed(http.MethodPost, goldenPacket, packet, wildcard([]string{E}, impersonating))},
			{"R2 packets C+E", h.signed(http.MethodPost, goldenPacket, packet, wildcard([]string{C, E}, impersonating))},
			{"R3 evidence C", h.signed(http.MethodGet, evidence, nil, wildcard([]string{C}, impersonating))},
			{"R1 capabilities I", h.signed(http.MethodGet, capabilities, nil, wildcard([]string{I}, impersonating))},
			{"R2 packets I", h.signed(http.MethodPost, goldenPacket, packet, wildcard([]string{I}, impersonating))},
			{"R3 evidence I", h.signed(http.MethodGet, evidence, nil, wildcard([]string{I}, impersonating))},
		}
		for _, row := range refused {
			t.Run(row.name+impersonationSuffix(impersonating), func(t *testing.T) {
				assertGoldenUniform401(t, h.do(row.request))
			})
		}
	}
}

// An impersonating reader's named-scope read assertion is answered exactly as
// before CHAOS-13612: the read routes do not act on the impersonating claim.
func TestWebAssertionGolden_impersonatingNamedReaderUnchanged(t *testing.T) {
	h := newGoldenHarness(t)
	packet := goldenPacketBody(t, hostedTestRepository)
	request := h.signed(http.MethodPost, goldenPacket, packet, func(c map[string]any) {
		c["permissions"] = []string{auth.ScopeContextRead}
		c["sid"] = "session_123"
		c["impersonating"] = true
	})
	if response := h.do(request); response.Code != http.StatusOK {
		t.Fatalf("impersonating named reader status = %d %s", response.Code, response.Body.String())
	}
}

func impersonationSuffix(impersonating bool) string {
	if impersonating {
		return " impersonating"
	}
	return ""
}
