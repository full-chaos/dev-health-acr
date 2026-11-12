package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type organizationWideReadRow struct {
	name      string
	scopes    []string
	absent    bool
	perms     []string
	method    string
	path      string
	query     string
	accept    bool
	noLog     bool
	logScope  string
	logReason string
}

// CHAOS-13612: a lone "*" is accepted on the three read routes with exactly
// the permission set web sends there, and refused everywhere else.
func organizationWideReadRows() []organizationWideReadRow {
	const (
		capabilities = "/api/v1/agent-context/capabilities"
		packets      = "/api/v1/agent-context/context-packets"
		evidence     = "/api/v1/agent-context/evidence/"
		readReason   = "read route's exact read permissions"
		emptyReason  = "no repository scope"
	)
	C, E, I := ScopeContextRead, ScopeEvidenceRead, WebAssertionPermissionCredentialIssue
	get, post := http.MethodGet, http.MethodPost
	all := []string{"*"}
	named := "example-org/widget-service"
	many := make([]string, 256)
	for i := range many {
		many[i] = fmt.Sprintf("example-org/repo-%03d", i)
	}
	refuse := func(name string, scopes, perms []string, method, path string) organizationWideReadRow {
		return organizationWideReadRow{name: name, scopes: scopes, perms: perms, method: method, path: path, logScope: "*", logReason: readReason}
	}
	rows := []organizationWideReadRow{
		{name: "R2 packets C", scopes: all, perms: []string{C}, method: post, path: packets, accept: true},
		{name: "R3 evidence E acr ref", scopes: all, perms: []string{E}, method: get, path: evidence + "acr%3Av1%3Aevidence%3Aabc123", accept: true},
		{name: "R3 evidence E catalog ref", scopes: all, perms: []string{E}, method: get, path: evidence + "evidence-ref-00000001", accept: true},
		{name: "R1 capabilities C+E", scopes: all, perms: []string{C, E}, method: get, path: capabilities, accept: true},
		{name: "R1 capabilities E+C order", scopes: all, perms: []string{E, C}, method: get, path: capabilities, accept: true},
		{name: "issue device approval I", scopes: all, perms: []string{I}, method: post, path: "/api/v1/oauth/device_approval", accept: true},
		{name: "issue consent I", scopes: all, perms: []string{I}, method: post, path: "/authorize/consent", accept: true},
		{name: "issue first-party I", scopes: all, perms: []string{I}, method: post, path: "/authorize/first-party", accept: true},
		{name: "one name C on R2", scopes: []string{named}, perms: []string{C}, method: post, path: packets, accept: true},
		{name: "256 names C on R2", scopes: many, perms: []string{C}, method: post, path: packets, accept: true},
		{name: "256 names E on R3", scopes: many, perms: []string{E}, method: get, path: evidence + "evidence-ref-00000001", accept: true},

		refuse("R1 capabilities C alone", all, []string{C}, get, capabilities),
		refuse("R1 capabilities E alone", all, []string{E}, get, capabilities),
		refuse("R2 packets E", all, []string{E}, post, packets),
		refuse("R2 packets C+E", all, []string{C, E}, post, packets),
		refuse("R3 evidence C", all, []string{C}, get, evidence+"evidence-ref-00000001"),
		refuse("R3 evidence C+E", all, []string{C, E}, get, evidence+"evidence-ref-00000001"),
		refuse("R1 capabilities I", all, []string{I}, get, capabilities),
		refuse("R2 packets I", all, []string{I}, post, packets),
		refuse("R3 evidence I", all, []string{I}, get, evidence+"evidence-ref-00000001"),
		refuse("R2 packets C+I", all, []string{C, I}, post, packets),
		refuse("R2 packets episode:write", all, []string{ScopeEpisodeWrite}, post, packets),
		refuse("R2 packets data:read", all, []string{ScopeDataRead}, post, packets),
		refuse("R2 packets admin:all", all, []string{"admin:all"}, post, packets),
		refuse("R3 evidence data:read", all, []string{ScopeDataRead}, get, evidence+"evidence-ref-00000001"),
		refuse("R2 packets C+C duplicate", all, []string{C, C}, post, packets),
		refuse("R2 path with GET", all, []string{C}, get, packets),
		refuse("R1 path with POST", all, []string{C, E}, post, capabilities),
		refuse("R3 path with POST", all, []string{E}, post, evidence+"evidence-ref-00000001"),
		refuse("R3 empty ref", all, []string{E}, get, evidence),
		refuse("R3 two segments", all, []string{E}, get, evidence+"a/b"),
		refuse("R3 dot", all, []string{E}, get, evidence+"."),
		refuse("R3 dot dot", all, []string{E}, get, evidence+".."),
		refuse("episodes C", all, []string{C}, post, "/api/v1/agent-context/episodes"),
		refuse("context fabric data catalog C", all, []string{C}, get, "/api/v1/context-fabric/data/catalog"),
		refuse("other path C", all, []string{C}, get, "/other"),
		{name: "R3 with query", scopes: all, perms: []string{E}, method: get, path: evidence + "evidence-ref-00000001", query: "result_id=x", noLog: true},
		{name: "space star C on R2", scopes: []string{" *"}, perms: []string{C}, method: post, path: packets, logScope: " *"},
		{name: "star slash star C on R2", scopes: []string{"*/*"}, perms: []string{C}, method: post, path: packets, logScope: "*/*"},
		{name: "star then name C on R2", scopes: []string{"*", named}, perms: []string{C}, method: post, path: packets, logScope: "*"},
		{name: "name then star E on R3", scopes: []string{named, "*"}, perms: []string{E}, method: get, path: evidence + "evidence-ref-00000001", logScope: "*"},
		{name: "star then name C+E on R1", scopes: []string{"*", named}, perms: []string{C, E}, method: get, path: capabilities, logScope: "*"},
		{name: "star then name I on device approval", scopes: []string{"*", named}, perms: []string{I}, method: post, path: "/api/v1/oauth/device_approval", logScope: "*"},
		{name: "empty C on R2", scopes: []string{}, perms: []string{C}, method: post, path: packets, logReason: emptyReason},
		{name: "empty E on R3", scopes: []string{}, perms: []string{E}, method: get, path: evidence + "evidence-ref-00000001", logReason: emptyReason},
		{name: "empty C+E on R1", scopes: []string{}, perms: []string{C, E}, method: get, path: capabilities, logReason: emptyReason},
		{name: "empty I on device approval", scopes: []string{}, perms: []string{I}, method: post, path: "/api/v1/oauth/device_approval", logReason: emptyReason},
		{name: "absent C on R2", absent: true, perms: []string{C}, method: post, path: packets, logReason: emptyReason},
		{name: "absent E on R3", absent: true, perms: []string{E}, method: get, path: evidence + "evidence-ref-00000001", logReason: emptyReason},
		{name: "absent C+E on R1", absent: true, perms: []string{C, E}, method: get, path: capabilities, logReason: emptyReason},
	}
	for _, path := range []string{"/api/v1/oauth/device_approval", "/authorize/consent", "/authorize/first-party"} {
		for _, perms := range [][]string{{C}, {E}, {C, E}} {
			rows = append(rows, refuse("issue path "+path+" "+strings.Join(perms, "+"), all, perms, post, path))
		}
	}
	return rows
}

func TestWebAssertionVerifier_organizationWideRead(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks := writeTestJWKS(t, "current", public)
	for _, row := range organizationWideReadRows() {
		for _, impersonating := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/impersonating=%t", row.name, impersonating), func(t *testing.T) {
				handler := &recordingHandler{}
				verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
					Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: jwks,
					Now: func() time.Time { return now }, Logger: slog.New(handler),
				})
				if err != nil {
					t.Fatal(err)
				}
				body := []byte{}
				if row.method == http.MethodPost {
					body = []byte(`{}`)
				}
				target := row.path
				if row.query != "" {
					target += "?" + row.query
				}
				request := httptest.NewRequest(row.method, target, bytes.NewReader(body))
				claims := webAssertionClaims(now, request, body)
				claims["permissions"] = row.perms
				if row.absent {
					delete(claims, "repository_scopes")
				} else {
					claims["repository_scopes"] = row.scopes
				}
				if impersonating {
					claims["sid"] = "session_123"
					claims["impersonating"] = true
				}
				request.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", claims))

				principal, err := verifier.Verify(request)

				if row.accept {
					if err != nil {
						t.Fatalf("assertion refused: %v", err)
					}
					if len(handler.records) != 0 {
						t.Fatalf("accepted assertion logged %d records", len(handler.records))
					}
					wantScopes := slices.Clone(row.scopes)
					slices.Sort(wantScopes)
					wantPerms := slices.Clone(row.perms)
					slices.Sort(wantPerms)
					if !slices.Equal(principal.RepositoryScopes, wantScopes) || !slices.Equal(principal.Permissions, wantPerms) {
						t.Fatalf("principal scopes %v permissions %v, want %v %v", principal.RepositoryScopes, principal.Permissions, wantScopes, wantPerms)
					}
					if principal.Impersonating != impersonating || principal.OrgID != "org_123" || principal.Subject != "user_123" {
						t.Fatalf("principal identity = %#v", principal)
					}
					if slices.Equal(row.scopes, []string{"*"}) && ClassifyGrant(principal) != GrantUniversal {
						t.Fatalf("grant class = %s, want %s", ClassifyGrant(principal), GrantUniversal)
					}
					return
				}
				if err == nil {
					t.Fatal("assertion accepted")
				}
				if IsWebAssertionReplay(err) {
					t.Fatalf("refusal is a replay: %v", err)
				}
				if row.noLog {
					if len(handler.records) != 0 {
						t.Fatalf("log records = %d, want 0", len(handler.records))
					}
					return
				}
				if len(handler.records) != 1 {
					t.Fatalf("log records = %d, want 1", len(handler.records))
				}
				attrs := map[string]string{}
				handler.records[0].Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
				if handler.records[0].Level != slog.LevelWarn || attrs["class"] != "web_assertion_repository_scope_invalid" || attrs["scope"] != row.logScope {
					t.Fatalf("log = %v %#v, want WARN class web_assertion_repository_scope_invalid scope %q", handler.records[0].Level, attrs, row.logScope)
				}
				if row.logReason != "" && !strings.Contains(attrs["reason"], row.logReason) {
					t.Fatalf("log reason = %q, want it to contain %q", attrs["reason"], row.logReason)
				}
			})
		}
	}
}
