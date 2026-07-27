package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// deviceVerificationURI is the address every lifecycle fixture issues. It is a
// real, safe, launchable https address, because login now validates the
// verification address before printing it and a deliberately unlaunchable one
// would be refused there rather than at the opener.
//
// Nothing launches it. In-process tests run with lifecycleBrowserOpen stubbed
// by TestMain; compiled tests pass --no-browser, which skips opener resolution
// entirely while still printing and validating the address.
const deviceVerificationURI = "https://web.fullchaos.dev/acr/device"

// lifecycleFixtureState records what a fake lifecycle server observed.
//
// Handlers run on httptest's own goroutines, where t.Fatal is invalid: it
// calls runtime.Goexit on a goroutine the test does not own, so the request
// dies mid-response and the test keeps running against a truncated reply. The
// real assertion is then lost behind whatever the client makes of it -- a
// timeout, a decode error, or, worst of all, a pass. Handlers record their
// problems here and answer with an explicit HTTP status; assertProblems runs
// on the test's own goroutine and turns each one into a named failure.
type lifecycleFixtureState struct {
	mu             sync.Mutex
	problems       []string
	authorizations int
	polls          int
	revocations    int
	capabilities   int
}

func (state *lifecycleFixtureState) recordProblem(format string, args ...any) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.problems = append(state.problems, fmt.Sprintf(format, args...))
}

// nextPoll returns the zero-based index of this poll and reports whether the
// fixture still has a scripted result for it. Indexing a scripted slice
// directly panicked inside the handler goroutine when a budget regression
// polled once too often, which surfaces as a crashed test binary rather than
// as the assertion that actually failed.
func (state *lifecycleFixtureState) nextPoll(scripted int) (int, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	index := state.polls
	state.polls++
	if index >= scripted {
		state.problems = append(state.problems, fmt.Sprintf("device token polled %d times, want at most %d", index+1, scripted))
		return index, false
	}
	return index, true
}

func (state *lifecycleFixtureState) countAuthorization() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.authorizations++
}

func (state *lifecycleFixtureState) countRevocation() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.revocations++
}

func (state *lifecycleFixtureState) countCapabilities() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.capabilities++
}

// counts exposes the observed HTTP activity so a test can assert what the
// server actually served, not merely what the command printed.
func (state *lifecycleFixtureState) counts() (authorizations, polls, revocations, capabilities int) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.authorizations, state.polls, state.revocations, state.capabilities
}

func (state *lifecycleFixtureState) assertProblems(t *testing.T) {
	t.Helper()
	state.mu.Lock()
	problems := append([]string(nil), state.problems...)
	state.mu.Unlock()
	for _, problem := range problems {
		t.Errorf("lifecycle fixture: %s", problem)
	}
}

// registerLifecycleFixture wires the fixture's recorded problems into the
// test's own result. Every constructor below registers one, so a handler-side
// expectation failure is reported even when the command under test survives it.
func registerLifecycleFixture(t *testing.T) *lifecycleFixtureState {
	t.Helper()
	state := &lifecycleFixtureState{}
	t.Cleanup(func() { state.assertProblems(t) })
	return state
}

func writeLifecycleFixtureRefusal(t *testing.T, w http.ResponseWriter, status int) {
	t.Helper()
	w.WriteHeader(status)
	writeLifecycleError(t, w, status)
}

func newLifecycleServer(t *testing.T, token string, polls []string) *httptest.Server {
	server, _ := newLifecycleServerWithState(t, token, polls, nil)
	return server
}

type deviceAuthorizationExpectation struct {
	organizationIDHint *string
	repositoryHints    *[]string
}

func newLifecycleServerWithAuthorizationExpectation(t *testing.T, token string, polls []string, want *deviceAuthorizationExpectation) *httptest.Server {
	server, _ := newLifecycleServerWithState(t, token, polls, want)
	return server
}

func newLifecycleServerWithState(t *testing.T, token string, polls []string, want *deviceAuthorizationExpectation) (*httptest.Server, *lifecycleFixtureState) {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	state := registerLifecycleFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			state.countAuthorization()
			if r.Header.Get("Authorization") != "" {
				state.recordProblem("device authorization request unexpectedly had bearer authorization")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			if want != nil {
				var request contractsv1.DeviceAuthorizationRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					state.recordProblem("decode device authorization request: %v", err)
					writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
					return
				}
				if err := request.Validate(); err != nil {
					state.recordProblem("validate device authorization request: %v", err)
					writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
					return
				}
				if !reflect.DeepEqual(request.OrganizationIDHint, want.organizationIDHint) || !reflect.DeepEqual(request.RepositoryHints, want.repositoryHints) {
					state.recordProblem("device authorization hints = org=%#v repos=%#v, want org=%#v repos=%#v", request.OrganizationIDHint, request.RepositoryHints, want.organizationIDHint, want.repositoryHints)
					writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
					return
				}
			}
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			if r.Header.Get("Authorization") != "" {
				state.recordProblem("device token request unexpectedly had bearer authorization")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			index, scripted := state.nextPoll(len(polls))
			if !scripted {
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorAccessDenied})
				return
			}
			result := polls[index]
			if result != "success" {
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorCode(result)})
				return
			}
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: contractsv1.ClientCredential{SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: "credential-1", Name: "device credential", TokenPrefix: "fcacr_abcd1234", OrgID: "org-1", RepositoryScopes: []string{"owner/repo"}, Scopes: []string{"context:read", "evidence:read"}, CreatedAt: createdAt, ExpiresAt: &expiresAt}})
		case "/api/v1/agent-context/capabilities":
			state.countCapabilities()
			if r.Header.Get("Authorization") != "Bearer "+token {
				state.recordProblem("live doctor did not use the persisted credential")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			writeLifecycleCapabilities(t, w)
		case "/api/v1/auth/credentials/self/revoke":
			state.countRevocation()
			if r.Header.Get("Authorization") != "Bearer "+token {
				state.recordProblem("issued credential was not used for self-revocation")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-issued", &revokedAt)})
		default:
			state.recordProblem("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

// deviceAuthorizationTrace records what the device fixture actually issued and
// what the client actually redeemed. Counting authorizations is what bounds the
// restart budget; recording a distinct device code per authorization is what
// proves a restarted flow burned the previous code instead of replaying it.
type deviceAuthorizationTrace struct {
	mu             sync.Mutex
	authorizations int
	issuedCodes    []string
	polledCodes    []string
}

func (trace *deviceAuthorizationTrace) issue() string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.authorizations++
	code := strings.Repeat(string(rune('a'+trace.authorizations-1)), 32)
	trace.issuedCodes = append(trace.issuedCodes, code)
	return code
}

func (trace *deviceAuthorizationTrace) redeem(code string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.polledCodes = append(trace.polledCodes, code)
}

func (trace *deviceAuthorizationTrace) snapshot() (int, []string, []string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return trace.authorizations, append([]string(nil), trace.issuedCodes...), append([]string(nil), trace.polledCodes...)
}

func newLifecycleRetryServer(t *testing.T, token string, polls []string, trace *deviceAuthorizationTrace) *httptest.Server {
	server, _ := newLifecycleRetryServerWithState(t, token, polls, trace)
	return server
}

func newLifecycleRetryServerWithState(t *testing.T, token string, polls []string, trace *deviceAuthorizationTrace) (*httptest.Server, *lifecycleFixtureState) {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	state := registerLifecycleFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			state.countAuthorization()
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: trace.issue(), UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			var request contractsv1.DeviceTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				state.recordProblem("decode device token request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			trace.redeem(request.DeviceCode)
			index, scripted := state.nextPoll(len(polls))
			if !scripted {
				// A budget regression must surface as a named assertion
				// failure, never as an index panic or a hang that reads
				// like a timeout.
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorAccessDenied})
				return
			}
			result := polls[index]
			if result == "transport" {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					state.recordProblem("transport fixture response is not hijackable")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					state.recordProblem("hijack transport fixture connection: %v", err)
					return
				}
				_ = connection.Close()
				return
			}
			if result != "success" {
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorCode(result)})
				return
			}
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			credential := lifecycleCredential(createdAt, "credential-1", nil)
			credential.ExpiresAt = &expiresAt
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: credential})
		default:
			state.recordProblem("unexpected retry fixture request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

func newCredentialLifecycleServer(t *testing.T, original, successor string, revocations *int, revokeFails bool) *httptest.Server {
	server, _ := newCredentialLifecycleServerWithState(t, original, successor, revocations, revokeFails)
	return server
}

func newCredentialLifecycleServerWithState(t *testing.T, original, successor string, revocations *int, revokeFails bool) (*httptest.Server, *lifecycleFixtureState) {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	fixture := registerLifecycleFixture(t)
	state := credentialLifecycleState{
		originalToken:      original,
		replacementToken:   successor,
		originalID:         "credential-original",
		replacementID:      "credential-replacement",
		rotationReceiptTTL: createdAt.Add(15 * time.Minute),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/credentials/self/rotate":
			if r.Header.Get("Authorization") != "Bearer "+state.originalToken || state.replacementIssued || state.originalRevoked {
				fixture.recordProblem("refresh did not use the original credential exactly once")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			state.replacementIssued = true
			writeLifecycleJSON(t, w, contractsv1.CredentialRotateResponse{SchemaVersion: contractsv1.CredentialRotateResponseSchema, AccessToken: state.replacementToken, Credential: lifecycleCredential(createdAt, state.replacementID, nil), Receipt: contractsv1.CredentialRotationReceipt{SourceCredentialID: state.originalID, ReplacementCredentialID: state.replacementID, RollbackUntil: state.rotationReceiptTTL}})
		case "/api/v1/auth/credentials/self/revoke":
			fixture.countRevocation()
			(*revocations)++
			var request contractsv1.CredentialRevokeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				fixture.recordProblem("decode credential revoke request: %v", err)
				writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
				return
			}
			if revokeFails {
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			revokedAt := createdAt.Add(time.Minute)
			if request.RollbackReceipt != nil {
				if r.Header.Get("Authorization") != "Bearer "+state.replacementToken || !state.replacementIssued || state.replacementRevoked {
					fixture.recordProblem("refresh rollback did not use the replacement credential exactly once")
					writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
					return
				}
				if request.RollbackReceipt.SourceCredentialID != state.originalID || request.RollbackReceipt.ReplacementCredentialID != state.replacementID || !request.RollbackReceipt.RollbackUntil.Equal(state.rotationReceiptTTL) {
					fixture.recordProblem("refresh rollback did not provide the issued rotation receipt")
					writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
					return
				}
				state.replacementRevoked = true
				writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, state.replacementID, &revokedAt)})
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+state.originalToken || state.originalRevoked {
				fixture.recordProblem("ordinary logout did not use the original credential exactly once")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			state.originalRevoked = true
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, state.originalID, &revokedAt)})
		default:
			fixture.recordProblem("unexpected credential lifecycle request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, fixture
}

type credentialLifecycleState struct {
	originalToken      string
	replacementToken   string
	originalID         string
	replacementID      string
	rotationReceiptTTL time.Time
	replacementIssued  bool
	replacementRevoked bool
	originalRevoked    bool
}

func lifecycleCredential(createdAt time.Time, credentialID string, revokedAt *time.Time) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: credentialID, Name: "MCP sidecar", TokenPrefix: "fcacr_abcd1234", OrgID: "org-1", RepositoryScopes: []string{"owner/repo"}, Scopes: []string{"context:read", "evidence:read"}, CreatedAt: createdAt, RevokedAt: revokedAt}
}

func writeLifecycleError(t *testing.T, w http.ResponseWriter, status int) {
	t.Helper()
	writeLifecycleJSON(t, w, contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: "request-1", Error: contractsv1.ErrorDetail{Code: "invalid_token", Message: "credential rejected", HTTPStatus: status, Retryable: false}})
}

func writeLifecycleCapabilities(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	_, err := w.Write([]byte(`{"schema_version":"capabilities.v1","service":"dev-health-acr","service_version":"dev","minimum_sidecar_version":"1.0.0","supported_schema_versions":` + schemaVersionsJSON() + `,"enabled_tools":["context_for_task","source_evidence"],"entitlements":{"agent_context_runtime":true},"permissions":{"context_read":true,"evidence_read":true,"episode_write":false},"limits":{"max_items":30,"max_output_tokens":4000,"max_serialized_bytes":262144,"requests_per_minute":60},"generated_at":"2026-07-25T00:00:00Z"}`))
	if err != nil {
		t.Errorf("write capabilities: %v", err)
	}
}

func writeLifecycleJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write lifecycle JSON: %v", err)
	}
}
