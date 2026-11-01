package authprojection

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const secretToken = "tok-SECRET-never-logged"

type fakeTokens struct {
	mu        sync.Mutex
	audiences []string
	scopes    [][]string
	err       error
	token     string
}

func (f *fakeTokens) Token(_ context.Context, audience string, scope ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audiences = append(f.audiences, audience)
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return "", f.err
	}
	if f.token != "" {
		return f.token, nil
	}
	return secretToken, nil
}

type recordedRequest struct {
	method, path, auth, contentType string
	body                            string
}

func newServer(t *testing.T, status int, header http.Header, answer string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, recordedRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)})
		mu.Unlock()
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func newClient(t *testing.T, origin string, tokens TokenSource) *Client {
	t.Helper()
	client, err := New(origin, tokens)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPostSendsTheStoredBytesToTheFixedRouteWithTheWorkloadToken(t *testing.T) {
	server, seen := newServer(t, http.StatusNoContent, nil, "")
	tokens := &fakeTokens{}
	body := `{"schema_version":"acr-credential-lifecycle-event.v1","idempotency_key":"acr_credential:cred_a:1"}`
	result := newClient(t, server.URL, tokens).Post(context.Background(), []byte(body))
	if result.Outcome != OutcomeApplied {
		t.Fatalf("outcome = %q", result.Outcome)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %d", len(*seen))
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.path != "/internal/v1/acr/credential-events" {
		t.Errorf("route = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+secretToken || got.contentType != "application/json" {
		t.Errorf("headers: auth set=%v content-type=%q", got.auth == "Bearer "+secretToken, got.contentType)
	}
	if got.body != body {
		t.Errorf("body is not the stored bytes: %q", got.body)
	}
	if len(tokens.audiences) != 1 || tokens.audiences[0] != "auth-service" || len(tokens.scopes[0]) != 0 {
		t.Errorf("token asked for audience %v scope %v (the audience alone: the token has no scope claim)", tokens.audiences, tokens.scopes)
	}
}

func TestPostClassifiesEveryAnswer(t *testing.T) {
	retry := `{"schema_version":"error.v1","status":409,"reason_code":"projection_retry","request_id":"req_AAAAAAAAAAAAAAAAAAAAAAAAAA","occurred_at":"2026-11-01T00:00:00Z"}`
	conflict := strings.ReplaceAll(retry, `"projection_retry"`, `"projection_conflict"`)
	finalRefused := strings.ReplaceAll(retry, `"projection_retry"`, `"projection_refused"`)
	oldGap := strings.ReplaceAll(retry, `"projection_retry"`, `"gap"`)
	oldDigest := strings.ReplaceAll(retry, `"projection_retry"`, `"digest_mismatch"`)
	unknown := strings.ReplaceAll(retry, `"projection_retry"`, `"something_else"`)
	cases := []struct {
		name   string
		status int
		answer string
		want   Outcome
	}{
		{"204 applied or duplicate", 204, "", OutcomeApplied},
		{"400 schema refusal", 400, "", OutcomeSchema},
		{"401", 401, "", OutcomeAuthRefused},
		{"403", 403, "", OutcomeAuthRefused},
		{"409 projection_retry", 409, retry, OutcomeRetry},
		{"409 projection_conflict is final", 409, conflict, OutcomeConflict},
		{"409 projection_refused is final", 409, finalRefused, OutcomeFinalRefused},
		{"409 the retired gap reason is not read", 409, oldGap, OutcomeOther},
		{"409 the retired digest_mismatch reason is not read", 409, oldDigest, OutcomeOther},
		{"409 unknown reason", 409, unknown, OutcomeOther},
		{"409 no body", 409, "", OutcomeOther},
		{"409 not json", 409, "projection_retry", OutcomeOther},
		{"429", 429, "", OutcomeBusy},
		{"503", 503, "", OutcomeBusy},
		{"200 is not the contract", 200, "", OutcomeOther},
		{"201", 201, "", OutcomeOther},
		{"404", 404, "", OutcomeOther},
		{"422", 422, "", OutcomeOther},
		{"500", 500, "", OutcomeOther},
		{"502", 502, "", OutcomeOther},
		{"302 is not followed", 302, "", OutcomeOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newServer(t, tc.status, http.Header{"Location": {"/elsewhere"}}, tc.answer)
			if got := newClient(t, server.URL, &fakeTokens{}).Post(context.Background(), []byte("{}")); got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
		})
	}
}

func TestARedirectIsNeverFollowedSoTheTokenStaysOnTheNamedHost(t *testing.T) {
	var other int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other++ }))
	defer target.Close()
	server, _ := newServer(t, 307, http.Header{"Location": {target.URL}}, "")
	if got := newClient(t, server.URL, &fakeTokens{}).Post(context.Background(), []byte("{}")); got.Outcome != OutcomeOther {
		t.Fatalf("outcome = %q", got.Outcome)
	}
	if other != 0 {
		t.Fatal("the redirect target received a request")
	}
}

func TestBusyHonoursRetryAfterAndCapsItAtFiveMinutes(t *testing.T) {
	envelope := func(seconds string) string {
		return `{"schema_version":"error.v1","status":503,"reason_code":"unavailable","retry_after_seconds":` + seconds + `}`
	}
	cases := []struct {
		name   string
		header http.Header
		answer string
		want   time.Duration
	}{
		{"header seconds", http.Header{"Retry-After": {"42"}}, "", 42 * time.Second},
		{"envelope seconds", nil, envelope("17"), 17 * time.Second},
		{"header wins over the envelope", http.Header{"Retry-After": {"9"}}, envelope("17"), 9 * time.Second},
		{"capped at five minutes", http.Header{"Retry-After": {"86400"}}, "", 5 * time.Minute},
		{"exactly the cap", http.Header{"Retry-After": {"300"}}, "", 5 * time.Minute},
		{"just under the cap", http.Header{"Retry-After": {"299"}}, "", 299 * time.Second},
		{"envelope capped", nil, envelope("86400"), 5 * time.Minute},
		{"none", nil, "", 0},
		{"zero", http.Header{"Retry-After": {"0"}}, "", 0},
		{"negative", http.Header{"Retry-After": {"-5"}}, "", 0},
		{"a past HTTP date is no wait", http.Header{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}, "", 0},
	}
	for _, status := range []int{429, 503} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				server, _ := newServer(t, status, tc.header, tc.answer)
				got := newClient(t, server.URL, &fakeTokens{}).Post(context.Background(), []byte("{}"))
				if got.Outcome != OutcomeBusy || got.RetryAfter != tc.want {
					t.Fatalf("result = %+v, want busy %v", got, tc.want)
				}
			})
		}
	}
}

func TestRetryAfterIsCarriedOnlyByABusyAnswer(t *testing.T) {
	server, _ := newServer(t, 400, http.Header{"Retry-After": {"30"}}, "")
	if got := newClient(t, server.URL, &fakeTokens{}).Post(context.Background(), []byte("{}")); got.RetryAfter != 0 {
		t.Fatalf("retry after = %v", got.RetryAfter)
	}
}

func TestNoUsableTokenSendsNoRequest(t *testing.T) {
	server, seen := newServer(t, 204, nil, "")
	for name, tokens := range map[string]*fakeTokens{"error": {err: errors.New("no usable token")}} {
		if got := newClient(t, server.URL, tokens).Post(context.Background(), []byte("{}")); got.Outcome != OutcomeNoToken {
			t.Errorf("%s: outcome = %q", name, got.Outcome)
		}
	}
	if len(*seen) != 0 {
		t.Fatalf("a request was sent without a token: %d", len(*seen))
	}
	empty := &emptyTokens{}
	if got := newClient(t, server.URL, empty).Post(context.Background(), []byte("{}")); got.Outcome != OutcomeNoToken || len(*seen) != 0 {
		t.Fatalf("an empty token must send nothing: %+v, %d requests", got, len(*seen))
	}
}

type emptyTokens struct{}

func (emptyTokens) Token(context.Context, string, ...string) (string, error) { return "", nil }

func TestATransportFailureAndATimeoutAreNoAnswer(t *testing.T) {
	server, _ := newServer(t, 204, nil, "")
	client := newClient(t, server.URL, &fakeTokens{})
	server.Close()
	if got := client.Post(context.Background(), []byte("{}")); got.Outcome != OutcomeNoAnswer {
		t.Fatalf("closed server: %q", got.Outcome)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if got := newClient(t, slow.URL, &fakeTokens{}).Post(ctx, []byte("{}")); got.Outcome != OutcomeNoAnswer {
		t.Fatalf("timeout: %q", got.Outcome)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("the call outlived its context")
	}
}

func TestTheRequestTimeoutIsFiveSeconds(t *testing.T) {
	if RequestTimeout != 5*time.Second || MaxRetryAfter != 5*time.Minute {
		t.Fatalf("RequestTimeout=%v MaxRetryAfter=%v", RequestTimeout, MaxRetryAfter)
	}
}

func TestNewRefusesAnythingButAnOrigin(t *testing.T) {
	for _, origin := range []string{"", "auth-service", "ftp://auth.example.test", "https://", "https://auth.example.test/api", "https://auth.example.test?x=1", "https://auth.example.test#f", "https://user:pw@auth.example.test"} {
		if _, err := New(origin, &fakeTokens{}); err == nil {
			t.Errorf("origin %q was accepted", origin)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("the error echoes the value: %v", err)
		}
	}
	for _, origin := range []string{"https://auth.example.test", "https://auth.example.test/", "http://127.0.0.1:8080"} {
		if _, err := New(origin, &fakeTokens{}); err != nil {
			t.Errorf("origin %q refused: %v", origin, err)
		}
	}
	if _, err := New("https://auth.example.test", nil); err == nil {
		t.Error("a nil token source was accepted")
	}
}

func TestTheClientDialsWithATransportThatNeverUsesTheEnvironmentProxy(t *testing.T) {
	client, err := New("https://auth.example.test", &fakeTokens{})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("transport = %T, proxy set = %v: the token must reach only the host named", client.http.Transport, ok && transport.Proxy != nil)
	}
}
