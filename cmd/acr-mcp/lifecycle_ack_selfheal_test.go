package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// storedLoginServer serves a login against a credential already in storage.
//
// The stored credential's bearer is `stored`. A device flow it starts issues
// `issued` (which may equal `stored`, to model a login that stopped after
// persisting). Acknowledgements made with the stored bearer answer the
// settable ackStatus; any other bearer is acknowledged at once.
type storedLoginServer struct {
	*httptest.Server
	state *lifecycleFixtureState

	mu           sync.Mutex
	ackStatus    int // 0 = 200
	revokeStatus int // 0 = 200
	omittedIDs   int
}

func (s *storedLoginServer) setAckStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ackStatus = status
}

func (s *storedLoginServer) omittedIDAcks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.omittedIDs
}

func newStoredLoginServer(t *testing.T, stored, issued string, ackStatus, revokeStatus int) *storedLoginServer {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	fake := &storedLoginServer{state: registerLifecycleFixture(t), ackStatus: ackStatus, revokeStatus: revokeStatus}
	state := fake.state
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recordFixturePanic(state, w)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent-context/capabilities":
			state.countCapabilities()
			if r.Header.Get("Authorization") != "Bearer "+stored {
				state.recordProblem("capabilities check did not use the stored credential")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			writeLifecycleCapabilities(t, w)
		case "/api/v1/auth/credentials/self/ack":
			state.countAck()
			bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if bearer != stored && bearer != issued {
				state.recordProblem("acknowledgement used an unknown bearer")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			var request contractsv1.CredentialAckRequest
			if !decodeStrictLifecycleFixtureRequest(t, state, w, r, &request) {
				return
			}
			if request.CredentialID == nil {
				fake.mu.Lock()
				fake.omittedIDs++
				fake.mu.Unlock()
			} else if *request.CredentialID != "credential-new" {
				state.recordProblem("acknowledged credential_id = %q, want credential-new", *request.CredentialID)
				writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
				return
			}
			fake.mu.Lock()
			status := fake.ackStatus
			fake.mu.Unlock()
			if bearer == stored && status != 0 {
				writeLifecycleFixtureRefusal(t, w, status)
				return
			}
			writeLifecycleJSON(t, w, contractsv1.CredentialAckResponse{SchemaVersion: contractsv1.CredentialAckResponseSchema, CredentialID: "credential-new", AcknowledgedAt: time.Now().UTC().Truncate(time.Second)})
		case "/api/v1/auth/credentials/self/revoke":
			state.countRevocation()
			var request contractsv1.CredentialRevokeRequest
			if !decodeStrictLifecycleFixtureRequest(t, state, w, r, &request) {
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+stored {
				state.recordProblem("revocation did not use the stored credential")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			if fake.revokeStatus != 0 {
				writeLifecycleUnavailable(t, w)
				return
			}
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-new", &revokedAt)})
		case "/api/v1/oauth/device_authorization":
			state.countAuthorization()
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
			if _, scripted := state.nextPoll(1); !scripted {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: issued, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: deviceLoginCredential(createdAt, "credential-new", &expiresAt)})
		default:
			state.recordProblem("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.Close)
	return fake
}

// storedLoginEnv points the CLI at the server and a token file, optionally
// pre-written with a stored credential, and returns the file path.
func storedLoginEnv(t *testing.T, server *storedLoginServer, stored string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if stored != "" {
		if err := os.WriteFile(path, []byte(stored+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	t.Setenv(sidecar.ClientVersionEnvironment, "1.0.0")
	withImmediateDevicePoll(t)
	return path
}

func runStoredLogin(t *testing.T) (int, string, string) {
	t.Helper()
	var stdout string
	code, stderr := captureStderr(t, func() int {
		c, out := captureStdout(t, func() int { return runCLI([]string{"login", "--no-browser"}) })
		stdout = out
		return c
	})
	return code, stdout, stderr
}

func assertTokenFile(t *testing.T, path, token string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("credential file missing: %v", err)
	}
	if string(contents) != token+"\n" {
		t.Fatalf("credential file = %q, want the expected token", strings.TrimSpace(string(contents)))
	}
}

func TestLoginReportsAlreadyLoggedIn_whenTheStoredCredentialAcknowledgementIsConfirmed(t *testing.T) {
	stored := validDoctorToken(141)
	server := newStoredLoginServer(t, stored, validDoctorToken(142), 0, 0)
	path := storedLoginEnv(t, server, stored)

	code, stdout, stderr := runStoredLogin(t)

	if code != 0 || !strings.Contains(stdout, "already logged in") {
		t.Fatalf("login = %d stdout=%q stderr=%q, want 0 and already logged in", code, stdout, stderr)
	}
	if got := server.state.ackCount(); got != 1 {
		t.Fatalf("acknowledgements = %d, want 1", got)
	}
	if got := server.omittedIDAcks(); got != 1 {
		t.Fatalf("acknowledgements without credential_id = %d, want 1", got)
	}
	if authorizations, _, revocations, _ := server.state.counts(); authorizations != 0 || revocations != 0 {
		t.Fatalf("authorizations=%d revocations=%d, want 0 0", authorizations, revocations)
	}
	assertTokenFile(t, path, stored)
}

func TestLoginReportsAlreadyLoggedIn_whenTheServerKnowsNoAcknowledgementOwed(t *testing.T) {
	stored := validDoctorToken(143)
	server := newStoredLoginServer(t, stored, validDoctorToken(144), http.StatusNotFound, 0)
	path := storedLoginEnv(t, server, stored)

	code, stdout, stderr := runStoredLogin(t)

	if code != 0 || !strings.Contains(stdout, "already logged in") {
		t.Fatalf("login = %d stdout=%q stderr=%q, want 0 and already logged in", code, stdout, stderr)
	}
	if got := server.state.ackCount(); got != 1 {
		t.Fatalf("acknowledgements = %d, want 1 (a definite 404 is not retried)", got)
	}
	if authorizations, _, revocations, _ := server.state.counts(); authorizations != 0 || revocations != 0 {
		t.Fatalf("authorizations=%d revocations=%d, want 0 0", authorizations, revocations)
	}
	assertTokenFile(t, path, stored)
}

func TestLoginRevokesThenStartsANewLogin_whenTheStoredCredentialWindowClosed(t *testing.T) {
	stored := validDoctorToken(145)
	issued := validDoctorToken(146)
	server := newStoredLoginServer(t, stored, issued, http.StatusConflict, 0)
	path := storedLoginEnv(t, server, stored)

	code, stdout, stderr := runStoredLogin(t)

	if code != 0 {
		t.Fatalf("login = %d stdout=%q stderr=%q, want 0", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "never confirmed and is revoked; starting a new login") || !strings.Contains(stdout, "login successful") {
		t.Fatalf("stdout = %q, want the revoked notice and a successful new login", stdout)
	}
	authorizations, polls, revocations, _ := server.state.counts()
	if authorizations != 1 || polls != 1 || revocations != 1 {
		t.Fatalf("counts = auth %d poll %d revoke %d, want 1 1 1", authorizations, polls, revocations)
	}
	// One ack for the stored credential (409 ends attempts) and one for the new one.
	if got := server.state.ackCount(); got != 2 {
		t.Fatalf("acknowledgements = %d, want 2", got)
	}
	assertTokenFile(t, path, issued)
}

func TestLoginRetainsTheStoredCredential_whenTheWindowClosedAndRevocationFails(t *testing.T) {
	stored := validDoctorToken(147)
	server := newStoredLoginServer(t, stored, validDoctorToken(148), http.StatusConflict, http.StatusServiceUnavailable)
	path := storedLoginEnv(t, server, stored)

	code, stdout, stderr := runStoredLogin(t)

	if code != lifecycleExitFailure {
		t.Fatalf("login = %d, want %d; stdout=%q stderr=%q", code, lifecycleExitFailure, stdout, stderr)
	}
	if !strings.Contains(stderr, "never confirmed and could not be revoked") {
		t.Fatalf("stderr = %q, want the could-not-be-revoked message", stderr)
	}
	authorizations, _, revocations, _ := server.state.counts()
	if authorizations != 0 || revocations == 0 {
		t.Fatalf("authorizations=%d revocations=%d, want 0 and a revoke attempt", authorizations, revocations)
	}
	assertTokenFile(t, path, stored)
}

func TestLoginRetainsTheStoredCredentialAndStartsNothing_whenTheAcknowledgementIsAmbiguous(t *testing.T) {
	stored := validDoctorToken(149)
	server := newStoredLoginServer(t, stored, validDoctorToken(150), http.StatusServiceUnavailable, 0)
	path := storedLoginEnv(t, server, stored)

	code, stdout, stderr := runStoredLogin(t)

	if code != lifecycleExitFailure {
		t.Fatalf("login = %d, want %d; stdout=%q stderr=%q", code, lifecycleExitFailure, stdout, stderr)
	}
	if !strings.Contains(stderr, "could not be confirmed with the server") {
		t.Fatalf("stderr = %q, want the could-not-be-confirmed message", stderr)
	}
	if got := server.state.ackCount(); got != deviceAckAttempts {
		t.Fatalf("acknowledgement attempts = %d, want %d", got, deviceAckAttempts)
	}
	if authorizations, _, revocations, _ := server.state.counts(); authorizations != 0 || revocations != 0 {
		t.Fatalf("authorizations=%d revocations=%d, want 0 0", authorizations, revocations)
	}
	assertTokenFile(t, path, stored)
}

// A login that stored its credential but never got the acknowledgement through
// leaves the credential in place; the next login acknowledges it and finishes.
func TestLoginSelfHeals_whenTheFirstRunStoredTheCredentialButCouldNotAcknowledgeIt(t *testing.T) {
	token := validDoctorToken(151)
	server := newStoredLoginServer(t, token, token, http.StatusServiceUnavailable, 0)
	path := storedLoginEnv(t, server, "")

	// First run: device flow succeeds, every acknowledgement fails.
	code, stdout, stderr := runStoredLogin(t)
	if code != lifecycleExitFailure || strings.Contains(stdout, "login successful") {
		t.Fatalf("first login = %d stdout=%q stderr=%q, want failure without success message", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "it was kept") {
		t.Fatalf("first login stderr = %q, want the kept notice", stderr)
	}
	if got := server.state.ackCount(); got != deviceAckAttempts {
		t.Fatalf("first run acknowledgement attempts = %d, want %d", got, deviceAckAttempts)
	}
	assertTokenFile(t, path, token)

	// Second run: the server answers acknowledgements again.
	server.setAckStatus(0)
	code, stdout, stderr = runStoredLogin(t)

	if code != 0 || !strings.Contains(stdout, "already logged in") {
		t.Fatalf("second login = %d stdout=%q stderr=%q, want 0 and already logged in", code, stdout, stderr)
	}
	if got := server.state.ackCount(); got != deviceAckAttempts+1 {
		t.Fatalf("total acknowledgements = %d, want %d", got, deviceAckAttempts+1)
	}
	if got := server.omittedIDAcks(); got != 1 {
		t.Fatalf("acknowledgements without credential_id = %d, want 1 (the second run's)", got)
	}
	if authorizations, polls, revocations, _ := server.state.counts(); authorizations != 1 || polls != 1 || revocations != 0 {
		t.Fatalf("counts = auth %d poll %d revoke %d, want 1 1 0 (no new authorization on the second run)", authorizations, polls, revocations)
	}
	assertTokenFile(t, path, token)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential file missing")
	}
}
