package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

const (
	accessOrg     = "8f6b9b8e-2d0b-4a6e-9a3e-1c2d3e4f5a6b"
	accessUser    = "11111111-2222-4333-8444-555555555555"
	accessSession = "ses_AAAAAAAAAAAAAAAAAAAAAA"
)

func accessDocument(session bool, scope string) string {
	body := `{"schema_version":"acr_principal_access.v1","org_id":"` + accessOrg + `","user_id":"` + accessUser + `","member":true,"impersonating":false,`
	if session {
		body += `"session":{"live":true,"impersonating":false},`
	}
	return body + `"entitlements":{"ask_dev":true,"agent_context_runtime":false},"repository_scope":` + scope + `}`
}

// accessServer answers every request with status and body, and records the
// request path, query and bearer.
func accessServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int64, *atomic.Value) {
	t.Helper()
	var calls atomic.Int64
	var last atomic.Value
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		last.Store(r.Method + " " + r.URL.EscapedPath() + "?" + r.URL.RawQuery + " " + r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &calls, &last
}

func newAccessClient(t *testing.T, server *httptest.Server) *entitlements.Client {
	t.Helper()
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestPrincipalAccessReadsTheLiveAnswerWithNoCache(t *testing.T) {
	server, calls, last := accessServer(t, http.StatusOK, accessDocument(true, `{"kind":"repos","repos":["acme/api","acme/web"]}`))
	client := newAccessClient(t, server)
	for range 2 {
		access, err := client.PrincipalAccess(context.Background(), accessOrg, accessUser, accessSession)
		if err != nil {
			t.Fatal(err)
		}
		if !access.Member || access.Impersonating || access.Session == nil || !access.Session.Live || access.Session.Impersonating ||
			!access.AskDev || access.AgentContextRuntime || !slices.Equal(access.Repositories, []string{"acme/api", "acme/web"}) {
			t.Fatalf("access = %+v (session %+v)", access, access.Session)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("reads = %d, want 2: the answer is never cached", calls.Load())
	}
	want := "GET /api/v1/internal/acr/orgs/" + accessOrg + "/principals/" + accessUser + "/access?sid=" + accessSession + " Bearer " + testToken
	if got := last.Load(); got != want {
		t.Fatalf("request = %q, want %q", got, want)
	}
}

func TestPrincipalAccessWithoutASessionSendsNoSid(t *testing.T) {
	server, _, last := accessServer(t, http.StatusOK, accessDocument(false, `{"kind":"none"}`))
	client := newAccessClient(t, server)
	access, err := client.PrincipalAccess(context.Background(), accessOrg, accessUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if access.Session != nil || access.Repositories != nil {
		t.Fatalf("access = %+v, want no session and an empty inventory", access)
	}
	if got := last.Load().(string); !strings.Contains(got, "/access? ") {
		t.Fatalf("request = %q, want no query", got)
	}
}

func TestPrincipalAccessRefusalsAndFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		session string
		want    error
	}{
		{"404 unknown organization", http.StatusNotFound, `{"detail":"Not found","code":"acr_organization_unknown"}`, "", entitlements.ErrPrincipalUnknown},
		{"404 not a uuid", http.StatusNotFound, `{"detail":"Not found"}`, "", entitlements.ErrPrincipalUnknown},
		{"400 invalid sid", http.StatusBadRequest, `{"detail":"Invalid request","code":"acr_access_invalid_sid"}`, accessSession, entitlements.ErrSessionInvalid},
	} {
		server, _, _ := accessServer(t, test.status, test.body)
		_, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, test.session)
		if !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", test.name, err, test.want)
		}
	}
	// Every other answer is unavailable: never an answer, never unknown.
	for _, test := range []struct {
		name    string
		status  int
		body    string
		session string
	}{
		{"400 invalid query", http.StatusBadRequest, `{"detail":"Invalid request","code":"acr_access_invalid_query"}`, ""},
		{"400 invalid sid without a session asked", http.StatusBadRequest, `{"code":"acr_access_invalid_sid"}`, ""},
		{"401", http.StatusUnauthorized, `{}`, ""},
		{"403", http.StatusForbidden, `{}`, ""},
		{"429", http.StatusTooManyRequests, `{}`, ""},
		{"503", http.StatusServiceUnavailable, `{"detail":"Service unavailable"}`, ""},
		{"wrong schema", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), "acr_principal_access.v1", "acr_principal_access.v2", 1), ""},
		{"other org", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), accessOrg, "00000000-0000-4000-8000-000000000000", 1), ""},
		{"other user", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), accessUser, "00000000-0000-4000-8000-000000000000", 1), ""},
		{"member missing", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"member":true,`, "", 1), ""},
		{"member null", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"member":true`, `"member":null`, 1), ""},
		{"member a string", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"member":true`, `"member":"true"`, 1), ""},
		{"impersonating missing", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"impersonating":false,`, "", 1), ""},
		{"entitlements missing", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"entitlements":{"ask_dev":true,"agent_context_runtime":false},`, "", 1), ""},
		{"ask_dev missing", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"ask_dev":true,`, "", 1), ""},
		{"unknown field", http.StatusOK, strings.Replace(accessDocument(false, `{"kind":"none"}`), `"member":true`, `"member":true,"admin":true`, 1), ""},
		{"session when none asked", http.StatusOK, accessDocument(true, `{"kind":"none"}`), ""},
		{"no session when one asked", http.StatusOK, accessDocument(false, `{"kind":"none"}`), accessSession},
		{"session live missing", http.StatusOK, strings.Replace(accessDocument(true, `{"kind":"none"}`), `"live":true,`, "", 1), accessSession},
		{"kind missing", http.StatusOK, accessDocument(false, `{}`), ""},
		{"kind unknown", http.StatusOK, accessDocument(false, `{"kind":"all"}`), ""},
		{"repos with kind none", http.StatusOK, accessDocument(false, `{"kind":"none","repos":["acme/api"]}`), ""},
		{"repos missing", http.StatusOK, accessDocument(false, `{"kind":"repos"}`), ""},
		{"repos empty", http.StatusOK, accessDocument(false, `{"kind":"repos","repos":[]}`), ""},
		{"repos blank entry", http.StatusOK, accessDocument(false, `{"kind":"repos","repos":[" "]}`), ""},
		{"trailing JSON", http.StatusOK, accessDocument(false, `{"kind":"none"}`) + `{}`, ""},
		{"not an object", http.StatusOK, `[]`, ""},
		// A non-200 status is never an answer, whatever its body says.
		{"503 with a document", http.StatusServiceUnavailable, accessDocument(false, `{"kind":"repos","repos":["acme/api"]}`), ""},
		{"403 with a document", http.StatusForbidden, accessDocument(false, `{"kind":"repos","repos":["acme/api"]}`), ""},
		{"202 with a document", http.StatusAccepted, accessDocument(false, `{"kind":"repos","repos":["acme/api"]}`), ""},
	} {
		server, _, _ := accessServer(t, test.status, test.body)
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, test.session)
		if err == nil || errors.Is(err, entitlements.ErrPrincipalUnknown) || errors.Is(err, entitlements.ErrSessionInvalid) || access.Member {
			t.Errorf("%s: access %+v err %v, want an unavailable rejection", test.name, access, err)
		}
	}
}

// An id that is not a canonical UUID is never sent: the principal is unknown.
func TestPrincipalAccessRefusesANonUUIDWithoutARead(t *testing.T) {
	server, calls, _ := accessServer(t, http.StatusOK, accessDocument(false, `{"kind":"none"}`))
	client := newAccessClient(t, server)
	for _, ids := range [][2]string{{"org_1", accessUser}, {accessOrg, "user_1"}, {strings.ToUpper(accessOrg), accessUser}} {
		if _, err := client.PrincipalAccess(context.Background(), ids[0], ids[1], ""); !errors.Is(err, entitlements.ErrPrincipalUnknown) {
			t.Errorf("ids %v: err = %v, want ErrPrincipalUnknown", ids, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("reads = %d, want 0", calls.Load())
	}
}
