package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestLoginPersistsCredentialAndDoctorDiscoversIt_when_deviceGrantRedeems(t *testing.T) {
	// Given
	token := validDoctorToken(81)
	server := newLifecycleServerWithAuthorizationExpectation(t, token, []string{"success"}, &deviceAuthorizationExpectation{})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return errors.New("browser unavailable") }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })

	// When
	code := runCLI([]string{"login"})
	report := runDoctorLive()

	// Then
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
	}
	if report.CredentialSource != "file" || !report.CredentialShapeValid || report.LiveCheck == nil || !report.LiveCheck.Reachable {
		t.Fatalf("persisted credential was not discovered by bare live doctor: %#v", report)
	}
	contents, err := os.ReadFile(os.Getenv(sidecar.TokenFileEnvironment))
	if err != nil {
		t.Fatalf("read persisted credential: %v", err)
	}
	if strings.TrimSpace(string(contents)) != token {
		t.Fatal("persisted credential did not match the redeemed token")
	}
}

func TestLoginIsIdempotentWhenPersistedCredentialIsAccepted(t *testing.T) {
	// Given a credential that is both locally well-formed and accepted by the
	// hosted API. A local shape check alone is not evidence of that second
	// property; this fixture makes the command prove it over the real
	// capabilities boundary.
	token := validDoctorToken(111)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := registerLifecycleFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent-context/capabilities":
			state.countCapabilities()
			if r.Header.Get("Authorization") != "Bearer "+token {
				state.recordProblem("credential validation did not use the persisted credential")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			writeLifecycleCapabilities(t, w)
		case "/api/v1/oauth/device_authorization":
			state.countAuthorization()
			state.recordProblem("idempotent login started a new device authorization")
			writeLifecycleFixtureRefusal(t, w, http.StatusConflict)
		default:
			state.recordProblem("unexpected idempotent-login request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	t.Setenv(sidecar.ClientVersionEnvironment, "1.0.0")

	// When
	code := runCLI([]string{"login", "--no-browser"})

	// Then the command succeeds without mutating the credential or starting a
	// second login flow.
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != token+"\n" {
		t.Fatal("idempotent login mutated the accepted credential")
	}
	authorizations, polls, revocations, capabilities := state.counts()
	if authorizations != 0 || polls != 0 || revocations != 0 || capabilities != 1 {
		t.Fatalf("HTTP counts = auth %d poll %d revoke %d capabilities %d, want 0 0 0 1", authorizations, polls, revocations, capabilities)
	}
}

func TestLoginReplacesPersistedCredentialInOneRunWhenHostedAPIRejectsIt(t *testing.T) {
	// Given a locally well-formed token that the API definitively identifies as
	// inactive, plus a device flow that can issue its replacement.
	staleToken := validDoctorToken(112)
	replacementToken := validDoctorToken(113)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(staleToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Truncate(time.Second)
	state := registerLifecycleFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent-context/capabilities":
			state.countCapabilities()
			if r.Header.Get("Authorization") != "Bearer "+staleToken {
				state.recordProblem("stale credential validation used the wrong bearer")
			}
			w.WriteHeader(http.StatusUnauthorized)
			writeLifecycleError(t, w, http.StatusUnauthorized)
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
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: replacementToken, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: deviceLoginCredential(createdAt, "credential-replacement", &expiresAt)})
		default:
			state.recordProblem("unexpected stale-login request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	t.Setenv(sidecar.ClientVersionEnvironment, "1.0.0")
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })

	// When
	code := runCLI([]string{"login", "--no-browser"})

	// Then the same invocation removes only the rejected material and completes
	// a fresh device login.
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != replacementToken+"\n" {
		t.Fatal("login did not replace the rejected persisted credential")
	}
	authorizations, polls, revocations, capabilities := state.counts()
	if authorizations != 1 || polls != 1 || revocations != 0 || capabilities != 1 {
		t.Fatalf("HTTP counts = auth %d poll %d revoke %d capabilities %d, want 1 1 0 1", authorizations, polls, revocations, capabilities)
	}
}

func TestLoginRetainsPersistedCredentialWhenHostedValidityIsAmbiguous(t *testing.T) {
	// Given a locally well-formed token and a configured API origin whose
	// listener is unavailable. A network failure proves nothing about whether
	// the credential is still live, so recovery must fail closed.
	token := validDoctorToken(114)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	apiURL := server.URL
	server.Close()
	t.Setenv(sidecar.APIURLEnvironment, apiURL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	t.Setenv(sidecar.ClientVersionEnvironment, "1.0.0")

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	// Then the command refuses to destroy an active credential on ambiguous
	// evidence and explains that it retained the local material.
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if !strings.Contains(stderr, "could not be verified") || !strings.Contains(stderr, "was retained") {
		t.Fatalf("stderr = %q, want safe ambiguous-validation guidance", stderr)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != token+"\n" {
		t.Fatal("ambiguous validation failure mutated the persisted credential")
	}
}

func TestLifecyclePersistUsesTheActiveSession_whenCredentialIsIssued(t *testing.T) {
	// Given
	token := validDoctorToken(101)
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// When
	persisted, err := lifecyclePersist(session, token)

	// Then
	if err != nil {
		t.Fatalf("persist through active lifecycle session: %v", err)
	}
	if persisted.Token != token || persisted.Source != "file" {
		t.Fatalf("persisted credential = %#v, want file credential for issued token", persisted)
	}
}

func TestLifecycleReplaceUsesTheActiveSession_whenRefreshingCredential(t *testing.T) {
	// Given
	original := validDoctorToken(102)
	successor := validDoctorToken(103)
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	if err := os.WriteFile(path, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	current, err := session.LoadCredential()
	if err != nil {
		t.Fatal(err)
	}

	// When
	err = lifecycleReplace(session, current, successor)

	// Then
	if err != nil {
		t.Fatalf("replace through active lifecycle session: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != successor+"\n" {
		t.Fatalf("replaced credential = %q, want successor", contents)
	}
}

func TestCredentialLifecycleContentionExplainsLiveOwnerAndRecovery(t *testing.T) {
	// Given
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for _, command := range []string{"login", "logout"} {
		t.Run(command, func(t *testing.T) {
			// When
			code, stderr := captureStderr(t, func() int { return runCLI([]string{command}) })

			// Then
			if code != lifecycleExitFailure {
				t.Fatalf("%s exit code = %d, want %d", command, code, lifecycleExitFailure)
			}
			for _, required := range []string{
				"another acr-mcp login, login --refresh, or logout process is still running",
				"stop that process or wait for it to finish, then retry",
			} {
				if !strings.Contains(stderr, required) {
					t.Fatalf("%s stderr = %q, want %q", command, stderr, required)
				}
			}
		})
	}
}

func TestCredentialLifecycleStartErrorDoesNotMislabelUnsafeLockFailure(t *testing.T) {
	// Given
	unsafeFailure := errors.New("unsafe lock fixture")

	// When
	message := credentialLifecycleStartError("login", unsafeFailure)

	// Then
	if !strings.Contains(message, "credential lifecycle lock could not be acquired safely") {
		t.Fatalf("message = %q, want safe lock-acquisition failure", message)
	}
	if strings.Contains(message, "another acr-mcp") || strings.Contains(message, unsafeFailure.Error()) {
		t.Fatalf("message = %q, mislabeled or leaked the underlying failure", message)
	}
}

func TestLoginSendsExactOrganizationAndRepositoryHintsFromDirectoryWithoutGit(t *testing.T) {
	// Given
	token := validDoctorToken(92)
	organizationIDHint := "org_fullchaos"
	repositoryHints := []string{"full-chaos/dev-health-acr", "full-chaos/dev-health"}
	server := newLifecycleServerWithAuthorizationExpectation(t, token, []string{"success"}, &deviceAuthorizationExpectation{
		organizationIDHint: &organizationIDHint,
		repositoryHints:    &repositoryHints,
	})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return errors.New("browser unavailable") }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	// When
	code := runCLI([]string{"login", "--org", organizationIDHint, "--repo", repositoryHints[0], "--repo=" + repositoryHints[1]})

	// Then
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
	}
}

func TestLogoutThenFreshLoginReauthorizesExactSnapshotCredential(t *testing.T) {
	oldToken := validDoctorToken(118)
	newToken := validDoctorToken(119)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(oldToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")

	revocations := 0
	logoutServer := newCredentialLifecycleServer(t, oldToken, validDoctorToken(120), &revocations, false)
	t.Setenv(sidecar.APIURLEnvironment, logoutServer.URL)
	if code := runCLI([]string{"logout"}); code != 0 {
		t.Fatalf("logout exit code = %d, want 0", code)
	}
	if revocations != 1 {
		t.Fatalf("old exact-snapshot credential revocations = %d, want 1", revocations)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old exact-snapshot credential remains after logout: %v", err)
	}

	loginServer := newLifecycleServerWithAuthorizationExpectation(t, newToken, []string{"success"}, &deviceAuthorizationExpectation{})
	t.Setenv(sidecar.APIURLEnvironment, loginServer.URL)
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return nil }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })
	if code := runCLI([]string{"login"}); code != 0 {
		t.Fatalf("fresh login exit code = %d, want 0", code)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reauthorized credential: %v", err)
	}
	if strings.TrimSpace(string(contents)) != newToken {
		t.Fatal("fresh organization-wide credential was not persisted")
	}
}

func TestLoginAddsFiveSecondsAfterSlowDown_when_pollingDeviceGrant(t *testing.T) {
	// Given
	token := validDoctorToken(82)
	server := newLifecycleServer(t, token, []string{"slow_down", "success"})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	originalWait := lifecycleWait
	var waits []time.Duration
	lifecycleWait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	t.Cleanup(func() { lifecycleWait = originalWait })
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return errors.New("browser unavailable") }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
	}
	if got, want := waits, []time.Duration{5 * time.Second, 10 * time.Second}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("poll waits = %v, want %v", got, want)
	}
}

func TestLoginRefusesNewCredential_when_validCredentialAlreadyExists(t *testing.T) {
	// Given
	token := validDoctorToken(83)
	t.Setenv(sidecar.TokenEnvironment, token)

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
	}
}

func TestLoginStopsBeforeDeviceAuthorization_whenLocalCredentialCannotBeVerified(t *testing.T) {
	// Given
	server := newLifecycleServer(t, validDoctorToken(93), []string{"success"})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	if err := os.WriteFile(os.Getenv(sidecar.TokenFileEnvironment), []byte("not-an-acr-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
	}
}

func TestLoginFails_when_deviceAuthorizationIsDeniedOrExpired(t *testing.T) {
	for _, outcome := range []string{"access_denied", "expired_token"} {
		t.Run(outcome, func(t *testing.T) {
			// Given
			server := newLifecycleServer(t, validDoctorToken(90), []string{outcome})
			t.Setenv(sidecar.APIURLEnvironment, server.URL)
			t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
			t.Setenv(sidecar.TokenEnvironment, "")
			t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
			t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
			originalWait := lifecycleWait
			lifecycleWait = func(context.Context, time.Duration) error { return nil }
			t.Cleanup(func() { lifecycleWait = originalWait })
			originalBrowserOpen := lifecycleBrowserOpen
			lifecycleBrowserOpen = func(string) error { return errors.New("browser unavailable") }
			t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

			// When
			code := runCLI([]string{"login"})

			// Then
			if code != lifecycleExitFailure {
				t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
			}
		})
	}
}

func TestLoginRestartsDeviceAuthorizationOnlyForInvalidGrants(t *testing.T) {
	tests := []struct {
		name         string
		polls        []string
		wantCode     int
		wantTerminal string
		wantStored   bool
	}{
		{name: "invalid_then_success", polls: []string{"invalid_grant", "success"}, wantCode: 0, wantStored: true},
		{name: "invalid_then_invalid", polls: []string{"invalid_grant", "invalid_grant"}, wantCode: lifecycleExitFailure, wantTerminal: "device authorization was invalidated twice"},
		{name: "transport_fails_without_restart", polls: []string{"transport"}, wantCode: lifecycleExitFailure, wantTerminal: "may have been redeemed but its result was lost"},
		{name: "invalid_then_transport_fails_without_another_restart", polls: []string{"invalid_grant", "transport"}, wantCode: lifecycleExitFailure, wantTerminal: "may have been redeemed but its result was lost"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token := validDoctorToken(71)
			var trace deviceAuthorizationTrace
			server := newLifecycleRetryServer(t, token, tc.polls, &trace)
			defer server.Close()
			path := filepath.Join(t.TempDir(), "token")
			t.Setenv(sidecar.APIURLEnvironment, server.URL)
			t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
			t.Setenv(sidecar.TokenEnvironment, "")
			t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
			t.Setenv(sidecar.TokenFileEnvironment, path)
			originalWait := lifecycleWait
			lifecycleWait = func(context.Context, time.Duration) error { return nil }
			t.Cleanup(func() { lifecycleWait = originalWait })
			originalBrowserOpen := lifecycleBrowserOpen
			opens := 0
			lifecycleBrowserOpen = func(uri string) error {
				opens++
				if uri == "" {
					t.Fatal("empty verification URI")
				}
				return nil
			}
			t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

			code, stderr := captureStderr(t, func() int { return runCLI([]string{"login"}) })

			if code != tc.wantCode {
				t.Fatalf("login exit code = %d, want %d; stderr=%s", code, tc.wantCode, stderr)
			}
			if tc.wantTerminal != "" && !strings.Contains(stderr, tc.wantTerminal) {
				t.Fatalf("terminal stderr = %q, want %q", stderr, tc.wantTerminal)
			}
			authorizations, issued, polled := trace.snapshot()
			if authorizations > 2 || opens > 2 || authorizations != opens {
				t.Fatalf("authorizations=%d opener calls=%d, want matching counts no greater than 2", authorizations, opens)
			}
			// A restart must burn the previous device code: every authorization
			// issues a distinct code, and every poll redeems the newest one.
			if len(issued) != authorizations {
				t.Fatalf("issued device codes = %v, want one per authorization (%d)", issued, authorizations)
			}
			distinct := map[string]bool{}
			for _, code := range issued {
				if distinct[code] {
					t.Fatalf("authorization reused device code %q", code)
				}
				distinct[code] = true
			}
			if len(polled) != len(tc.polls) {
				t.Fatalf("redeemed device codes = %v, want one per scripted poll (%d)", polled, len(tc.polls))
			}
			for index, code := range polled {
				if code != issued[index] {
					t.Fatalf("poll %d redeemed %q, want the code issued by authorization %d (%q)", index+1, code, index+1, issued[index])
				}
			}
			_, statErr := os.Stat(path)
			if tc.wantStored && statErr != nil {
				t.Fatalf("persisted credential missing after success: %v", statErr)
			}
			if !tc.wantStored && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("credential persisted after terminal failure: %v", statErr)
			}
		})
	}
}

func TestLogoutRemovesFileCredentialWithoutKeyringWhenDisabled(t *testing.T) {
	token := validDoctorToken(70)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(72), &revocations, false)
	defer server.Close()
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(sidecar.TokenKeyringAccountEnvironment, "agent-a")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := runCLI([]string{"logout"}); code != 0 {
		t.Fatalf("logout exit code = %d, want 0", code)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file credential remains after logout: %v", err)
	}
}

func TestLoginRevokesIssuedCredential_whenPersistenceFails(t *testing.T) {
	// Given
	token := validDoctorToken(96)
	createdAt := time.Now().UTC().Truncate(time.Second)
	revocations := 0
	fixture := registerLifecycleFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			credential := lifecycleCredential(createdAt, "credential-issued", nil)
			credential.ExpiresAt = &expiresAt
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: credential})
		case "/api/v1/auth/credentials/self/revoke":
			if r.Header.Get("Authorization") != "Bearer "+token {
				fixture.recordProblem("issued credential was not used for self-revocation")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			var request contractsv1.CredentialRevokeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				fixture.recordProblem("decode credential revoke request: %v", err)
				writeLifecycleFixtureRefusal(t, w, http.StatusBadRequest)
				return
			}
			revocations++
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-issued", &revokedAt)})
		default:
			fixture.recordProblem("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	originalPersist := lifecyclePersist
	lifecyclePersist = func(*sidecar.CredentialLifecycleSession, string) (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{}, errors.New("persistence failed")
	}
	t.Cleanup(func() { lifecyclePersist = originalPersist })
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return nil }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != lifecycleExitFailure || revocations != 1 {
		t.Fatalf("login code=%d revocations=%d, want failure and one revoke", code, revocations)
	}
}

func TestLoginRevokesIssuedCredential_whenTokenEndpointReturnsMalformedSuccess(t *testing.T) {
	// Given
	token := validDoctorToken(104)
	createdAt := time.Now().UTC().Truncate(time.Second)
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			_, _ = w.Write([]byte(`{"schema_version":"unsupported.v2","access_token":"` + token + `"}`))
		case "/api/v1/auth/credentials/self/revoke":
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			revocations++
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-issued", &revokedAt)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := sidecar.NewLifecycleClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })

	// When
	outcome := runDeviceLoginAttempt(context.Background(), session, client, cfg, loginArgs{noBrowser: true})

	// Then
	if outcome != deviceLoginFailed {
		t.Fatalf("malformed issued-token response outcome = %v, want failure", outcome)
	}
	if revocations != 1 {
		t.Fatalf("issued token revocations = %d, want 1", revocations)
	}
}

// A persistence failure whose file write already landed is the case that
// used to strand an issued credential: sidecar.PersistCredential reported the
// failure with an empty result, so login revoked the server-side credential
// and then had nothing to purge. The persisted locator returned alongside the
// error (see credential_persistence_sync_unix_test.go, which drives the real
// post-rename directory fsync failure) is what closes that gap; this test
// drives the same contract end to end through the login command.
func TestLoginRevokesThenPurgesIssuedCredential_whenPersistenceFailsAfterWritingTheFile(t *testing.T) {
	// Given
	token := validDoctorToken(99)
	createdAt := time.Now().UTC().Truncate(time.Second)
	revocations := 0
	path := filepath.Join(t.TempDir(), "token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			credential := lifecycleCredential(createdAt, "credential-issued", nil)
			credential.ExpiresAt = &expiresAt
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: credential})
		case "/api/v1/auth/credentials/self/revoke":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Errorf("issued credential was not used for self-revocation")
				w.WriteHeader(http.StatusUnauthorized)
				writeLifecycleError(t, w, http.StatusUnauthorized)
				return
			}
			revocations++
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-issued", &revokedAt)})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	originalPersist := lifecyclePersist
	lifecyclePersist = func(session *sidecar.CredentialLifecycleSession, value string) (sidecar.CredentialResult, error) {
		persisted, err := originalPersist(session, value)
		if err != nil {
			return persisted, err
		}
		// Same shape sidecar.PersistCredential returns when the rename
		// landed but the directory fsync did not: a real locator plus a
		// failure.
		return persisted, errors.New("credential file replacement could not be confirmed durable")
	}
	t.Cleanup(func() { lifecyclePersist = originalPersist })
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return nil }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want exactly one", revocations)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("issued credential remains on disk after revocation: %v", err)
	}
}

func TestLoginPurgesPersistedCredential_afterWrongSourceVerificationFailure(t *testing.T) {
	// Given
	token := validDoctorToken(98)
	server := newLifecycleServer(t, token, []string{"success"})
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	originalPersist := lifecyclePersist
	lifecyclePersist = func(session *sidecar.CredentialLifecycleSession, value string) (sidecar.CredentialResult, error) {
		credential, err := originalPersist(session, value)
		if err == nil {
			t.Setenv(sidecar.TokenEnvironment, value)
		}
		return credential, err
	}
	t.Cleanup(func() { lifecyclePersist = originalPersist })
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return nil }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("persisted issued credential remains after successful self-revocation: %v", err)
	}
}

func TestRevokeIssuedCredentialReturnsFalse_whenServerRejectsRevocation(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		writeLifecycleError(t, w, http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	// When
	ok := revokeIssuedCredential(context.Background(), cfg, validDoctorToken(97))

	// Then
	if ok {
		t.Fatal("rejected issued credential revocation reported success")
	}
}

func TestRevokeIssuedCredentialReturnsFalse_whenServerReturnsInvalidSemanticSuccess(t *testing.T) {
	// Given
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema_version":"unsupported.v2",
			"credential":{
				"schema_version":"acr_client_credential.v1",
				"credential_id":"cred_01J0ACR001",
				"name":"Chris local sidecar",
				"token_prefix":"fcacr_abcd1234",
				"org_id":"org_fullchaos",
				"repository_scopes":["full-chaos/dev-health-acr"],
				"scopes":["context:read","evidence:read"],
				"created_at":"2026-07-10T14:00:00Z",
				"expires_at":"2026-10-08T14:00:00Z",
				"revoked_at":"2026-07-11T14:00:00Z",
				"last_used_at":null
			}
		}`))
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	// When
	ok := revokeIssuedCredential(context.Background(), cfg, validDoctorToken(98))

	// Then
	if ok {
		t.Fatal("invalid semantic self-revocation response reported success")
	}
	if calls != 1 {
		t.Fatalf("revocation requests = %d, want 1", calls)
	}
}

func TestLifecycleCommandsPrintHelpAndRejectUnknownFlags(t *testing.T) {
	// Given
	cases := []struct {
		args []string
		want int
	}{
		{args: []string{"login", "--help"}, want: 0},
		{args: []string{"logout", "--help"}, want: 0},
		{args: []string{"login", "--unknown"}, want: 2},
		{args: []string{"logout", "--unknown"}, want: 2},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			// When
			code := runCLI(tc.args)

			// Then
			if code != tc.want {
				t.Fatalf("%v exit code = %d, want %d", tc.args, code, tc.want)
			}
		})
	}
}

func TestRefreshRestoresOriginalCredential_afterSuccessfulRollback(t *testing.T) {
	// Given
	original := validDoctorToken(84)
	successor := validDoctorToken(85)
	revocations := 0
	server := newCredentialLifecycleServer(t, original, successor, &revocations, false)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	if err := os.WriteFile(os.Getenv(sidecar.TokenFileEnvironment), []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalReplace := lifecycleReplace
	lifecycleReplace = func(session *sidecar.CredentialLifecycleSession, current sidecar.CredentialResult, token string) error {
		if err := session.ReplaceCredential(current, token); err != nil {
			return err
		}
		return errors.New("persistence failed after local replacement")
	}
	t.Cleanup(func() { lifecycleReplace = originalReplace })

	// When
	code := runCLI([]string{"login", "--refresh"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("refresh exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("successor revocations = %d, want 1", revocations)
	}
	contents, err := os.ReadFile(os.Getenv(sidecar.TokenFileEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != original+"\n" {
		t.Fatalf("restored credential = %q, want original", contents)
	}
}

func TestRefreshRetainsSuccessor_whenRollbackFails(t *testing.T) {
	// Given
	original := validDoctorToken(94)
	successor := validDoctorToken(95)
	revocations := 0
	server := newCredentialLifecycleServer(t, original, successor, &revocations, true)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	if err := os.WriteFile(os.Getenv(sidecar.TokenFileEnvironment), []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalReplace := lifecycleReplace
	lifecycleReplace = func(session *sidecar.CredentialLifecycleSession, current sidecar.CredentialResult, token string) error {
		if err := session.ReplaceCredential(current, token); err != nil {
			return err
		}
		return errors.New("persistence failed after local replacement")
	}
	t.Cleanup(func() { lifecycleReplace = originalReplace })

	// When
	code := runCLI([]string{"login", "--refresh"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("refresh exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("successor revocations = %d, want 1", revocations)
	}
	contents, err := os.ReadFile(os.Getenv(sidecar.TokenFileEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != successor+"\n" {
		t.Fatalf("credential after failed rollback = %q, want successor", contents)
	}
}

// Retention was previously asserted against an ACR_API_TOKEN credential, which
// has nothing removable behind it: the test passed whether or not logout would
// have deleted anything. This drives real removable material -- a token file on
// disk -- and asserts the file, the served HTTP activity, and the keyring seam
// all show that nothing local was touched after the remote revocation failed.
func TestLogoutRetainsCredential_when_remoteRevocationFails(t *testing.T) {
	// Given
	token := validDoctorToken(86)
	revocations := 0
	server, fixture := newCredentialLifecycleServerWithState(t, token, validDoctorToken(87), &revocations, true)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)

	// When
	code := runCLI([]string{"logout"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
	if _, _, served, _ := fixture.counts(); served != 1 {
		t.Fatalf("revocation requests served = %d, want exactly one attempt", served)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("local credential was removed after a failed remote revocation: %v", err)
	}
	if strings.TrimSpace(string(contents)) != token {
		t.Fatalf("retained credential = %q, want the original token", contents)
	}
}

func TestLogoutRetainsCredential_whenSelfRevokeReturnsMalformedSuccess(t *testing.T) {
	// Given
	token := validDoctorToken(105)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/credentials/self/revoke" || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"unsupported.v2"}`))
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)

	// When
	code := runCLI([]string{"logout"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want failure", code)
	}
	if requests != 1 {
		t.Fatalf("self-revoke requests = %d, want 1", requests)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("credential removed after malformed self-revoke response: %v", err)
	}
	if string(contents) != token+"\n" {
		t.Fatalf("retained credential = %q, want original", contents)
	}
}

// An exported ACR_API_TOKEN used to end the purge before it started, so a
// stale token file underneath it survived logout while the operator was told
// cleanup had failed at "the configured ACR keyring entry" -- a location the
// purge never touched. Cleanup must continue past the environment source and
// the message must name exactly what actually failed.
//
// The environment and the file hold the same credential here, which is the
// ordinary case after a login. Distinct values are covered by the combined
// multi-token logout test; the deduplication that keeps this case a single
// revocation is asserted below.
func TestLogoutPurgesFileMaterialAndNamesEnvironment_whenEnvironmentCredentialIsSelected(t *testing.T) {
	// Given
	token := validDoctorToken(90)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(91), &revocations, false)
	defer server.Close()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, token)
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"logout"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d; stderr=%s", code, lifecycleExitFailure, stderr)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file survived logout behind an environment credential: %v", err)
	}
	if !strings.Contains(stderr, sidecar.TokenEnvironment) {
		t.Fatalf("logout stderr = %q, want the exact unremovable location %q", stderr, sidecar.TokenEnvironment)
	}
	if strings.Contains(stderr, "keyring") {
		t.Fatalf("logout stderr = %q, want no derived keyring location", stderr)
	}
	if strings.Contains(stderr, token) || strings.Contains(stderr, "fcacr_") {
		t.Fatal("logout stderr leaked credential material")
	}
}

// The previous version of this test pointed ACR_API_TOKEN at the credential,
// so LoadCredential resolved source "environment" and PurgeCredentialMaterial
// short-circuited before reaching any cleanup path: it passed even with the
// purge targets deleted outright. This drives a real removable file credential
// whose parent directory denies the unlink instead.
func TestLogoutNamesCleanupLocation_whenFileRemovalFailsAfterRemoteRevocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential file permission fixture is POSIX-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}

	// Given
	token := validDoctorToken(88)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(89), &revocations, false)
	defer server.Close()
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"logout"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d; stderr=%s", code, lifecycleExitFailure, stderr)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
	if !strings.Contains(stderr, path) {
		t.Fatalf("logout stderr = %q, want the exact cleanup location %q", stderr, path)
	}
	if strings.Contains(stderr, token) || strings.Contains(stderr, "fcacr_") {
		t.Fatal("logout stderr leaked credential material")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credential must be retained when local cleanup fails: %v", err)
	}
}

func captureStderr(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = original })
	code := fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return code, string(output)
}

// The validated grant carries its own lifetime, and polling must be bounded by
// it. Without a deadline the loop only ended when the server said so, so an
// unresponsive or endlessly pending server kept login running against a code
// the server had already expired.
func TestLoginBoundsDevicePollingByTheValidatedAuthorizationLifetime(t *testing.T) {
	// Given
	token := validDoctorToken(93)
	server := newLifecycleServer(t, token, []string{"authorization_pending", "success"})
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	originalWait := lifecycleWait
	var deadlines []time.Duration
	lifecycleWait = func(ctx context.Context, _ time.Duration) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadlines = append(deadlines, 0)
			return nil
		}
		deadlines = append(deadlines, time.Until(deadline))
		return nil
	}
	t.Cleanup(func() { lifecycleWait = originalWait })
	originalBrowserOpen := lifecycleBrowserOpen
	lifecycleBrowserOpen = func(string) error { return nil }
	t.Cleanup(func() { lifecycleBrowserOpen = originalBrowserOpen })

	// When
	code := runCLI([]string{"login"})

	// Then
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
	}
	if len(deadlines) != 2 {
		t.Fatalf("poll waits = %d, want one pending poll and one success", len(deadlines))
	}
	for index, remaining := range deadlines {
		if remaining <= 0 {
			t.Fatalf("poll %d ran without an authorization deadline", index)
		}
		if remaining > 600*time.Second || remaining < 590*time.Second {
			t.Fatalf("poll %d deadline = %v, want the validated 600s grant lifetime", index, remaining)
		}
	}
}

// An expired lifetime is terminal: restarting would spend the bounded restart
// budget on an authorization the server has already expired.
func TestLoginStopsWithoutRestarting_whenTheAuthorizationLifetimeExpires(t *testing.T) {
	// Given
	token := validDoctorToken(94)
	var trace deviceAuthorizationTrace
	path := filepath.Join(t.TempDir(), "token")
	server := newLifecycleRetryServer(t, token, nil, &trace)
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	// The grant context is made to expire for real, and the real
	// waitForDevicePoll runs against it. Faking the expiry by returning a
	// DeadlineExceeded from the wait seam tested nothing: that is exactly the
	// error value that cannot distinguish a spent grant from a slow request, so
	// the assertion below passed whichever of the two the code concluded.
	originalGrant := lifecycleGrantContext
	lifecycleGrantContext = func(ctx context.Context, _ int) (context.Context, context.CancelFunc) {
		grantCtx, cancel := context.WithCancel(ctx)
		deadlineCtx, deadlineCancel := context.WithTimeout(grantCtx, time.Millisecond)
		return deadlineCtx, func() { deadlineCancel(); cancel() }
	}
	t.Cleanup(func() { lifecycleGrantContext = originalGrant })

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d; stderr=%s", code, lifecycleExitFailure, stderr)
	}
	if !strings.Contains(stderr, "device authorization expired") {
		t.Fatalf("logout stderr = %q, want the expiry to be named rather than reported as a cancellation", stderr)
	}
	authorizations, _, polled := trace.snapshot()
	if authorizations != 1 {
		t.Fatalf("authorizations = %d, want exactly one: an expired grant must not spend the restart budget", authorizations)
	}
	if len(polled) != 0 {
		t.Fatalf("device codes polled = %v, want none after the lifetime expired", polled)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential persisted after an expired authorization: %v", err)
	}
}
