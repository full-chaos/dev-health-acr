package sidecar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The class ids below are a copy of the class_id values in the ops-owned
// contracts/auth/v1/credential-classes.json, taken from dev-health-ops
// origin/main 171b2c8d058cb7003260c2cf8f2c48aff0a80889 (file sha256
// 195f307cc72e0e16..., 36 classes). acr does not vendor that file, so this
// table can drift from it; a follow-up check in ci/checkendpointprofiles
// (which already fetches the ops file) is meant to compare the two.
//
// The transport guard in callWithHeaders accepts exactly one class,
// acr_client_credential. Every other class, and every near-miss of the
// accepted shape, must be refused before a request reaches the transport.
// Values are built at run time from generic parts and are never real
// credentials; each carries a marker that no refusal text may repeat.

const ownCredentialClassID = "acr_client_credential"

type recordingRoundTripper struct {
	mu            sync.Mutex
	calls         int
	authorization []string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	r.authorization = append(r.authorization, req.Header.Get("Authorization"))
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
}

func (r *recordingRoundTripper) snapshot() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string(nil), r.authorization...)
}

type guardRow struct {
	name   string
	value  string
	needle string
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func classMarker(classID string) string { return "ZSENT_" + classID }

func jwtShaped(alg, marker string) string {
	return b64(`{"alg":"`+alg+`","typ":"JWT"}`) + "." + b64(`{"sub":"`+marker+`"}`) + "." + b64("sig-"+marker)
}

func padded43(marker string) string {
	if len(marker) >= 43 {
		return marker
	}
	return marker + strings.Repeat("A", 43-len(marker))
}

func uuidShaped(n byte) string {
	return "5e570000-5e57-4000-8000-5e57000000" + hex.EncodeToString([]byte{n})
}

func guardTestOwnToken(seed byte) string {
	return validTestToken(seed)
}

func otherClassRows() []guardRow {
	m := classMarker
	hexMarker := func(id string) string { return hex.EncodeToString([]byte(m(id))) }
	row := func(id, value string) guardRow {
		needle := m(id)
		if !strings.Contains(value, needle) {
			needle = value
		}
		return guardRow{name: id, value: value, needle: needle}
	}
	hexNeedle := func(id, value string) guardRow {
		return guardRow{name: id, value: value, needle: hexMarker(id)}
	}
	rows := []guardRow{
		row("auth_js_browser_session", jwtShaped("dir", m("auth_js_browser_session"))),
		row("ops_access_token_hs256", jwtShaped("HS256", m("ops_access_token_hs256"))),
		row("ops_refresh_token", jwtShaped("HS256", m("ops_refresh_token"))),
		row("refresh_credential", "dhr1_"+padded43(m("refresh_credential"))),
		row("user_access_token", jwtShaped("EdDSA", m("user_access_token"))),
		row("social_oauth_exchange", m("social_oauth_exchange")),
		row("sso_saml_exchange", base64.StdEncoding.EncodeToString([]byte("<Assertion>"+m("sso_saml_exchange")+"</Assertion>"))),
		row("sso_oidc_exchange", m("sso_oidc_exchange")),
		row("org_context_header", uuidShaped(1)),
		row("impersonation_session", uuidShaped(2)),
		row("internal_svc_acr_token", "svc_acr_"+padded43(m("internal_svc_acr_token"))),
		row("internal_svc_worker_token", "svc_worker_"+padded43(m("internal_svc_worker_token"))),
		row("internal_svc_authsess_token", "svc_authsess_"+padded43(m("internal_svc_authsess_token"))),
		row("worker_operational_bridge_token", m("worker_operational_bridge_token")),
		row("acr_device_flow_code", m("acr_device_flow_code")),
		row("acr_web_assertion", jwtShaped("EdDSA", m("acr_web_assertion"))),
		row("acr_workload_identity_exchange", jwtShaped("RS256", m("acr_workload_identity_exchange"))),
		row("external_push_ingest_token", "fcpush_"+padded43(m("external_push_ingest_token"))),
		row("legacy_ingest_api_key", m("legacy_ingest_api_key")),
		hexNeedle("legacy_ingest_hmac_signature", "sha256="+hexMarker("legacy_ingest_hmac_signature")),
		hexNeedle("stripe_webhook_signature", "t=1700000000,v1="+hexMarker("stripe_webhook_signature")),
		hexNeedle("github_webhook_signature", "sha256="+hexMarker("github_webhook_signature")),
		row("gitlab_webhook_signature", m("gitlab_webhook_signature")),
		hexNeedle("jira_webhook_signature", "sha256="+hexMarker("jira_webhook_signature")),
		row("github_app_installation_token", jwtShaped("RS256", m("github_app_installation_token"))),
		row("pagerduty_oauth_credential", m("pagerduty_oauth_credential")),
		hexNeedle("pagerduty_webhook_signature", "v1="+hexMarker("pagerduty_webhook_signature")),
		row("llm_api_keys", m("llm_api_keys")),
		row("email_verification_token", b64(m("email_verification_token"))+"."+b64("sig")),
		row("password_reset_token", b64(m("password_reset_token"))+"."+b64("sig")),
		row("link_credential", padded43(m("link_credential"))),
		row("org_invite_token", m("org_invite_token")),
		row("license_key", b64(`{"org":"`+m("license_key")+`"}`)+"."+b64("sig")),
		row("infrastructure_deployment_credentials", "postgres"+"://svc:"+m("infrastructure_deployment_credentials")+"@db.invalid:5432/app"),
		row("effective_principal_envelope", jwtShaped("EdDSA", m("effective_principal_envelope"))),
	}
	return rows
}

func ownClassNearMissRows() []guardRow {
	secret := func(n int) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, n))
	}
	valid := guardTestOwnToken(7)
	body := strings.TrimPrefix(valid, auth.TokenPrefix)
	mk := func(name, value string) guardRow {
		return guardRow{name: "own_class_near_miss_" + name, value: value, needle: strings.TrimSpace(value)}
	}
	return []guardRow{
		mk("short_31_bytes", auth.TokenPrefix+secret(31)),
		mk("long_33_bytes", auth.TokenPrefix+secret(33)),
		mk("std_alphabet_padding", auth.TokenPrefix+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))),
		mk("upper_case_prefix", strings.ToUpper(auth.TokenPrefix)+body),
		mk("leading_space", " "+valid),
		mk("prefix_only", auth.TokenPrefix),
		mk("other_prefix_same_body", "fcacx_"+body),
		mk("double_prefix", auth.TokenPrefix+valid),
		mk("bearer_scheme_included", "Bearer "+valid),
	}
}

type guardHarness struct {
	client  *Client
	rec     *recordingRoundTripper
	server  *httptest.Server
	handled func() int
	logs    *bytes.Buffer
}

func newGuardHarness(t *testing.T, token string) *guardHarness {
	t.Helper()
	var mu sync.Mutex
	handled := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		handled++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(newWritebackFixtureConfig(t, server), fixedCredentialSource(token))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingRoundTripper{}
	client.http.Transport = rec

	logs := &bytes.Buffer{}
	previousSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(logs)
	t.Cleanup(func() {
		slog.SetDefault(previousSlog)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return &guardHarness{client: client, rec: rec, server: server, logs: logs, handled: func() int {
		mu.Lock()
		defer mu.Unlock()
		return handled
	}}
}

func assertRefusedBeforeSend(t *testing.T, h *guardHarness, row guardRow, err error) {
	t.Helper()
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("%s: error = %v, want ErrCredentialShapeInvalid", row.name, err)
	}
	if calls, _ := h.rec.snapshot(); calls != 0 {
		t.Fatalf("%s: %d outbound request(s) reached the transport, want 0", row.name, calls)
	}
	if h.handled() != 0 {
		t.Fatalf("%s: the hosted API was contacted", row.name)
	}
	// The fixed refusal text names the accepted prefix, so a value that is
	// only that prefix (or part of the fixed text) cannot be told apart from it.
	fixedText := ErrCredentialShapeInvalid.Error()
	for _, text := range []string{err.Error(), h.logs.String()} {
		if row.value != "" && !strings.Contains(fixedText, row.value) && strings.Contains(text, row.value) {
			t.Fatalf("%s: refusal output repeats the credential value", row.name)
		}
		if row.needle != "" && !strings.Contains(fixedText, row.needle) && strings.Contains(text, row.needle) {
			t.Fatalf("%s: refusal output repeats part of the credential value", row.name)
		}
	}
}

func TestClientCredentialShapeGuardRefusesEveryOtherClassBeforeSend(t *testing.T) {
	rows := append(otherClassRows(), ownClassNearMissRows()...)
	seen := map[string]bool{ownCredentialClassID: true}
	for _, row := range rows {
		if seen[row.name] {
			t.Fatalf("duplicate or own-class row %q", row.name)
		}
		seen[row.name] = true
		if row.value == guardTestOwnToken(7) || auth.IsTokenShapeValid(row.value) {
			t.Fatalf("row %q is shaped like the accepted class", row.name)
		}
	}
	if got, want := len(otherClassRows()), 35; got != want {
		t.Fatalf("other-class rows = %d, want %d (36 ops classes minus the accepted one)", got, want)
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newGuardHarness(t, row.value)
			_, err := h.client.Capabilities(context.Background())
			assertRefusedBeforeSend(t, h, row, err)
		})
	}
}

func TestClientCredentialShapeGuardCoversBodyAndHeaderEntryPoints(t *testing.T) {
	rows := []guardRow{otherClassRows()[0], otherClassRows()[10], ownClassNearMissRows()[0]}
	entryPoints := map[string]func(*Client) error{
		"find_subjects_post_body": func(c *Client) error {
			_, err := c.FindSubjects(context.Background(), contractsv1.MCPFindSubjectsRequest{Kind: "team"})
			return err
		},
		"record_episode_with_headers": func(c *Client) error {
			_, err := c.RecordEpisode(context.Background(), validAgentEpisodeCreate())
			return err
		},
	}
	for name, call := range entryPoints {
		for _, row := range rows {
			t.Run(name+"/"+row.name, func(t *testing.T) {
				h := newGuardHarness(t, row.value)
				assertRefusedBeforeSend(t, h, row, call(h.client))
			})
		}
	}
}

func TestClientCredentialShapeGuardSendsOwnClass(t *testing.T) {
	token := guardTestOwnToken(9)
	h := newGuardHarness(t, token)
	_, err := h.client.Capabilities(context.Background())
	if errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("accepted class refused: %v", err)
	}
	calls, authorization := h.rec.snapshot()
	if calls != 1 || len(authorization) != 1 || authorization[0] != "Bearer "+token {
		t.Fatalf("accepted class: calls = %d, want exactly one request carrying the bearer value", calls)
	}
}
