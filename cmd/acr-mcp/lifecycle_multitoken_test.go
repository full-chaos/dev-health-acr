package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// multiCredentialServer is a revocation endpoint with more than one live
// credential.
//
// The single-credential fixtures cannot express the defect under test: they
// accept exactly one bearer, so a logout that revoked only the highest-
// precedence credential looked identical to one that revoked all of them. This
// fixture holds a set of active credentials and records which were actually
// revoked, so the assertion is the server's own state rather than the command's
// output.
type multiCredentialServer struct {
	mu      sync.Mutex
	active  map[string]bool
	revoked []string
	failFor map[string]int
}

func newMultiCredentialServer(t *testing.T, tokens []string) (*httptest.Server, *multiCredentialServer) {
	t.Helper()
	createdAt := time.Now().UTC().Truncate(time.Second)
	fixture := registerLifecycleFixture(t)
	state := &multiCredentialServer{active: map[string]bool{}, failFor: map[string]int{}}
	for _, token := range tokens {
		state.active[token] = true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recordFixturePanic(fixture, w)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/credentials/self/revoke" {
			fixture.recordProblem("unexpected multi-credential request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fixture.countRevocation()
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer == "" || bearer == r.Header.Get("Authorization") {
			fixture.recordProblem("revocation request did not carry a bearer credential")
			writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
			return
		}
		status, revoked := state.revoke(bearer)
		switch status {
		case revokeUnknownCredential:
			// A credential this server never issued must never be accepted:
			// silently succeeding here would let a logout that revoked the
			// wrong value, or a fabricated one, pass.
			fixture.recordProblem("revocation presented a credential this fixture never issued")
			writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
		case revokeScriptedFailure:
			w.WriteHeader(http.StatusServiceUnavailable)
			writeLifecycleJSON(t, w, contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: "request-1", Error: contractsv1.ErrorDetail{Code: "upstream_unavailable", Message: "revocation is temporarily unavailable", HTTPStatus: http.StatusServiceUnavailable, Retryable: true}})
		case revokeAlreadyInactive:
			// An established credential the server no longer recognizes is
			// already in the goal state.
			writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
		default:
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-"+revoked, &createdAt)})
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

type revokeOutcome int

const (
	revokeAccepted revokeOutcome = iota
	revokeUnknownCredential
	revokeScriptedFailure
	revokeAlreadyInactive
)

// failRevocationFor makes the next attempt against token report an operational
// failure.
func (s *multiCredentialServer) failRevocationFor(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failFor[token] = 1
}

// retireWithoutRevoking marks a credential as one the server has already
// forgotten, so a revocation of it answers invalid_token.
func (s *multiCredentialServer) retireWithoutRevoking(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[token] = false
}

func (s *multiCredentialServer) revoke(token string) (revokeOutcome, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFor[token] > 0 {
		s.failFor[token]--
		return revokeScriptedFailure, ""
	}
	state, known := s.active[token]
	if !known {
		return revokeUnknownCredential, ""
	}
	if !state {
		return revokeAlreadyInactive, ""
	}
	s.active[token] = false
	s.revoked = append(s.revoked, token)
	return revokeAccepted, token
}

func (s *multiCredentialServer) revokedTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sorted := append([]string(nil), s.revoked...)
	sort.Strings(sorted)
	return sorted
}

func (s *multiCredentialServer) stillActive() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := make([]string, 0, len(s.active))
	for token, active := range s.active {
		if active {
			live = append(live, token)
		}
	}
	sort.Strings(live)
	return live
}

type multiCredentialFixture struct {
	server       *multiCredentialServer
	environment  string
	keyring      string
	file         string
	filePath     string
	keyringStore *sidecar.MemoryKeyring
	keyringAddr  sidecar.KeyringAddress
}

// setUpMultiCredentialLogout configures three distinct, server-active
// credentials in the three local locations logout must reach: the process
// environment, the OS keyring seam, and the token file.
func setUpMultiCredentialLogout(t *testing.T) *multiCredentialFixture {
	t.Helper()
	environmentToken := validDoctorToken(101)
	keyringToken := validDoctorToken(102)
	fileToken := validDoctorToken(103)
	server, state := newMultiCredentialServer(t, []string{environmentToken, keyringToken, fileToken})
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	address := sidecar.KeyringAddress{Service: lifecycleKeyringService, Account: lifecycleKeyringAccount}
	keyring := installLifecycleMemoryKeyring(t, map[sidecar.KeyringAddress]string{address: keyringToken})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, environmentToken)
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "false")
	t.Setenv(sidecar.TokenKeyringServiceEnvironment, lifecycleKeyringService)
	t.Setenv(sidecar.TokenKeyringAccountEnvironment, lifecycleKeyringAccount)
	t.Setenv(sidecar.TokenFileEnvironment, path)
	return &multiCredentialFixture{
		server:       state,
		environment:  environmentToken,
		keyring:      keyringToken,
		file:         fileToken,
		filePath:     path,
		keyringStore: keyring,
		keyringAddr:  address,
	}
}

// Logout resolved one credential through LoadCredential, revoked that one, and
// then deleted every local location. With an exported ACR_API_TOKEN over a
// keyring entry over a token file, the two lower-precedence credentials were
// deleted locally while they stayed live on the server: anyone holding a copy
// kept a working credential the operator believed logout had ended.
//
// The assertion is the fixture's own credential state, so skipping either the
// keyring or the file revocation fails here regardless of what logout printed.
func TestLogoutRevokesEveryDistinctLocalCredentialBeforeRemovingAnything(t *testing.T) {
	// Given
	fixture := setUpMultiCredentialLogout(t)

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"logout"}) })

	// Then
	want := []string{fixture.environment, fixture.file, fixture.keyring}
	sort.Strings(want)
	if got := fixture.server.revokedTokens(); !equalStrings(got, want) {
		t.Fatalf("revoked credentials = %d of %d; the environment, keyring, and file credentials must all be revoked", len(got), len(want))
	}
	if live := fixture.server.stillActive(); len(live) != 0 {
		t.Fatalf("%d credential(s) remain live on the server after logout", len(live))
	}
	if _, err := os.Stat(fixture.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file survived logout: %v", err)
	}
	if _, remains := fixture.keyringStore.Entries()[fixture.keyringAddr]; remains {
		t.Fatal("keyring entry survived logout")
	}
	// ACR_API_TOKEN cannot be unset in the parent shell, so logout reports it
	// and exits nonzero even though every revocation and removal succeeded.
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d while ACR_API_TOKEN is still exported", code, lifecycleExitFailure)
	}
	if !strings.Contains(stderr, sidecar.TokenEnvironment) {
		t.Fatalf("logout stderr = %q, want the exported variable named", stderr)
	}
	for _, token := range want {
		if strings.Contains(stderr, token) {
			t.Fatal("logout stderr leaked credential material")
		}
	}
}

// Deletion is ordered strictly after every revocation. A failure against any
// one credential -- here the lowest-precedence one, which a first-credential-
// only implementation would never even attempt -- must retain all local
// material, because a local copy may be the last thing pointing at a credential
// that is still live.
func TestLogoutRetainsEveryLocalLocation_whenAnyRemoteRevocationFails(t *testing.T) {
	// Given
	fixture := setUpMultiCredentialLogout(t)
	fixture.server.failRevocationFor(fixture.file)

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"logout"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d after a failed revocation", code, lifecycleExitFailure)
	}
	if !strings.Contains(stderr, "local credential material was retained") {
		t.Fatalf("logout stderr = %q, want the retention reported", stderr)
	}
	contents, err := os.ReadFile(fixture.filePath)
	if err != nil {
		t.Fatalf("token file was removed after a failed revocation: %v", err)
	}
	if strings.TrimSpace(string(contents)) != fixture.file {
		t.Fatalf("retained token file = %q, want the original credential", contents)
	}
	if fixture.keyringStore.Entries()[fixture.keyringAddr] != fixture.keyring {
		t.Fatal("keyring entry was removed after a failed revocation elsewhere")
	}
	if len(fixture.keyringStore.Deletes()) != 0 {
		t.Fatalf("keyring deletions = %v, want none before every revocation succeeded", fixture.keyringStore.Deletes())
	}
}

// An established credential the server no longer recognizes is already in the
// goal state, so a typed invalid_token must not block cleanup of the rest.
// Treating it as a failure stranded readable local material for a credential
// that could never be used again.
func TestLogoutTreatsAnUnrecognizedCredentialAsAlreadyInactive(t *testing.T) {
	// Given
	fixture := setUpMultiCredentialLogout(t)
	fixture.server.retireWithoutRevoking(fixture.keyring)

	// When
	_, stderr := captureStderr(t, func() int { return runCLI([]string{"logout"}) })

	// Then
	if strings.Contains(stderr, "revocation failed") {
		t.Fatalf("logout stderr = %q, want an already-inactive credential accepted", stderr)
	}
	if _, err := os.Stat(fixture.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file survived logout behind an already-inactive credential: %v", err)
	}
	if _, remains := fixture.keyringStore.Entries()[fixture.keyringAddr]; remains {
		t.Fatal("keyring entry survived logout behind an already-inactive credential")
	}
}

// Enumeration fails closed: a keyring the client cannot read conclusively might
// hold a live credential, so nothing local may be deleted around it.
func TestLogoutRemovesNothing_whenCredentialMaterialCannotBeEnumerated(t *testing.T) {
	// Given
	fixture := setUpMultiCredentialLogout(t)
	fixture.keyringStore.FailLookup(fixture.keyringAddr, errors.New("secret collection is locked"))

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"logout"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d for an unreadable keyring", code, lifecycleExitFailure)
	}
	if !strings.Contains(stderr, "nothing was removed") {
		t.Fatalf("logout stderr = %q, want the fail-closed enumeration reported", stderr)
	}
	if len(fixture.server.revokedTokens()) != 0 {
		t.Fatalf("revocations = %v, want none before enumeration succeeded", len(fixture.server.revokedTokens()))
	}
	if _, err := os.Stat(fixture.filePath); err != nil {
		t.Fatalf("token file was removed despite an unreadable keyring: %v", err)
	}
	if fixture.keyringStore.Entries()[fixture.keyringAddr] != fixture.keyring {
		t.Fatal("keyring entry was removed despite an unreadable keyring")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
