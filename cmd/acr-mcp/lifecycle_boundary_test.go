package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

type boundaryRequest struct {
	method        string
	path          string
	authorization string
	headerNames   []string
	headerValues  string
	body          string
}

type boundaryRecorder struct {
	mu       sync.Mutex
	requests []boundaryRequest
}

func (recorder *boundaryRecorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	names := make([]string, 0, len(r.Header))
	var values strings.Builder
	for name, list := range r.Header {
		names = append(names, http.CanonicalHeaderKey(name))
		for _, value := range list {
			values.WriteString(value)
			values.WriteByte('\n')
		}
	}
	sort.Strings(names)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.requests = append(recorder.requests, boundaryRequest{
		method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization"),
		headerNames: names, headerValues: values.String(), body: string(body),
	})
}

func (recorder *boundaryRecorder) snapshot() []boundaryRequest {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]boundaryRequest(nil), recorder.requests...)
}

// newBoundaryServer answers the whole lifecycle surface: device login, live
// capabilities, rotation, acknowledgement, and revocation. It records every
// request; it never decides what is allowed, the test body does.
func newBoundaryServer(t *testing.T, first, second string, recorder *boundaryRecorder) *httptest.Server {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	state := registerLifecycleFixture(t)
	var mu sync.Mutex
	live := first
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recordFixturePanic(state, w)
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		current := live
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			var request contractsv1.DeviceAuthorizationRequest
			if !decodeStrictLifecycleFixtureRequest(t, state, w, r, &request) {
				return
			}
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			var request contractsv1.DeviceTokenRequest
			if !decodeStrictLifecycleFixtureRequest(t, state, w, r, &request) {
				return
			}
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: first, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: deviceLoginCredential(createdAt, "credential-1", &expiresAt)})
		case "/api/v1/agent-context/capabilities":
			if r.Header.Get("Authorization") != "Bearer "+current {
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			writeLifecycleCapabilities(t, w)
		case "/api/v1/auth/credentials/self/ack":
			serveLifecycleAck(t, state, w, r, current, "credential-1")
		case "/api/v1/auth/credentials/self/rotate":
			var request contractsv1.CredentialRotateRequest
			if !decodeStrictLifecycleFixtureRequest(t, state, w, r, &request) {
				return
			}
			mu.Lock()
			live = second
			mu.Unlock()
			writeLifecycleJSON(t, w, contractsv1.CredentialRotateResponse{SchemaVersion: contractsv1.CredentialRotateResponseSchema, AccessToken: second, Credential: lifecycleCredential(createdAt, "credential-2", nil), Receipt: contractsv1.CredentialRotationReceipt{SourceCredentialID: "credential-1", ReplacementCredentialID: "credential-2", RollbackUntil: createdAt.Add(15 * time.Minute)}})
		case "/api/v1/auth/credentials/self/revoke":
			var request contractsv1.CredentialRevokeRequest
			if !decodeStrictLifecycleFixtureRequest(t, state, w, r, &request) {
				return
			}
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-2", &revokedAt)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// credentialShapedValue matches any value that carries a credential-class prefix
// (ACR client, internal service and auth-session, ingest, license) or has the shape of a signed
// token, whatever its secret part.
var credentialShapedValue = regexp.MustCompile(`(fcacr|svc_acr|svc_worker|svc_authsess|fcpush|dhlic)_[A-Za-z0-9._-]{8,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// subjectSentinel builds a workload-token-shaped value at run time.
func subjectSentinel(t *testing.T) string {
	t.Helper()
	segment := func(text string) string { return base64.RawURLEncoding.EncodeToString([]byte(text)) }
	return segment(`{"alg":"RS256","typ":"JWT"}`) + "." + segment(`{"sub":"system:serviceaccount:boundary:probe"}`) + "." + segment(strings.Repeat("z", 48))
}

func TestLifecycleRunReachesOnlyTheConfiguredHostedAPIWithOnlyTheACRCredential(t *testing.T) {
	first := validDoctorToken(131)
	second := validDoctorToken(132)
	recorder := &boundaryRecorder{}
	server := newBoundaryServer(t, first, second, recorder)

	subject := subjectSentinel(t)
	subjectFile := filepath.Join(t.TempDir(), "subject-token")
	if err := os.WriteFile(subjectFile, []byte(subject+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var exchangeMu sync.Mutex
	exchangeHits := 0
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		exchangeMu.Lock()
		exchangeHits++
		exchangeMu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(exchange.Close)

	tokenFile := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, tokenFile)
	t.Setenv(sidecar.SubjectTokenFileEnvironment, subjectFile)
	t.Setenv(sidecar.TokenEndpointEnvironment, exchange.URL+"/api/v1/oauth/token")
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })

	var outputs []string
	run := func(args ...string) {
		t.Helper()
		var stderr string
		code, stdout := captureStdout(t, func() int {
			exit, captured := captureStderr(t, func() int { return runCLI(args) })
			stderr = captured
			return exit
		})
		if code != 0 {
			t.Fatalf("%v exit code = %d, want 0; stderr: %s", args, code, stderr)
		}
		outputs = append(outputs, stdout, stderr)
	}
	run("login", "--no-browser")
	stored, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	run("doctor", "--live")
	run("login", "--refresh")
	run("logout")

	if strings.Contains(string(stored), subject) {
		t.Fatal("the stored credential carries the workload subject token")
	}
	if strings.TrimSpace(string(stored)) != first {
		t.Fatal("login did not persist the redeemed ACR credential")
	}

	allowed := map[string]bool{
		"POST /api/v1/oauth/device_authorization":   true,
		"POST /api/v1/oauth/token":                  true,
		"GET /api/v1/agent-context/capabilities":    true,
		"POST /api/v1/auth/credentials/self/ack":    true,
		"POST /api/v1/auth/credentials/self/rotate": true,
		"POST /api/v1/auth/credentials/self/revoke": true,
	}
	public := map[string]bool{"/api/v1/oauth/device_authorization": true, "/api/v1/oauth/token": true}
	allowedHeaders := map[string]bool{"Accept": true, "Accept-Encoding": true, "Authorization": true, "Content-Length": true, "Content-Type": true, "User-Agent": true, "X-Acr-Client-Version": true}
	seen := map[string]bool{}
	for _, request := range recorder.snapshot() {
		key := request.method + " " + request.path
		seen[key] = true
		if !allowed[key] {
			t.Errorf("lifecycle sent a request outside the closed surface: %s", key)
		}
		if public[request.path] {
			if request.authorization != "" {
				t.Errorf("%s carried an Authorization header", key)
			}
		} else if request.authorization != "Bearer "+first && request.authorization != "Bearer "+second {
			t.Errorf("%s carried an Authorization header that is neither ACR credential of this run", key)
		}
		for _, name := range request.headerNames {
			if !allowedHeaders[name] {
				t.Errorf("%s carried the %s header, which is outside the closed header set", key, name)
			}
		}
		if strings.Contains(request.headerValues, subject) || strings.Contains(request.body, subject) {
			t.Errorf("%s carried the workload subject token", key)
		}
	}
	for key := range allowed {
		if !seen[key] {
			t.Errorf("lifecycle run never exercised %s; the closed-surface assertion would be vacuous for it", key)
		}
	}

	exchangeMu.Lock()
	hits := exchangeHits
	exchangeMu.Unlock()
	if hits != 0 {
		t.Fatalf("the configured token endpoint received %d requests from the lifecycle commands", hits)
	}
	for index, output := range outputs {
		for name, secret := range map[string]string{"first credential": first, "second credential": second, "workload subject token": subject} {
			if strings.Contains(output, secret) {
				t.Errorf("command output %d contains the %s", index, name)
			}
		}
		if match := credentialShapedValue.FindString(output); match != "" {
			t.Errorf("command output %d contains a credential-shaped value starting %q", index, match[:min(6, len(match))])
		}
	}
}
