package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestLoginSendsExactOrganizationAndRepositoryHints_whenProvided(t *testing.T) {
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

	// When
	code := runCLI([]string{"login", "--org", organizationIDHint, "--repo", repositoryHints[0], "--repo=" + repositoryHints[1]})

	// Then
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0", code)
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

func TestLoginRevokesIssuedCredential_whenPersistenceFails(t *testing.T) {
	// Given
	token := validDoctorToken(96)
	createdAt := time.Now().UTC().Truncate(time.Second)
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: strings.Repeat("d", 32), UserCode: "ABCDEFGH", VerificationURI: "http://" + r.Host + "/acr/device", ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			credential := lifecycleCredential(createdAt, "credential-issued", nil)
			credential.ExpiresAt = &expiresAt
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: credential})
		case "/api/v1/auth/credentials/self/revoke":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Fatal("issued credential was not used for self-revocation")
			}
			var request contractsv1.CredentialRevokeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			revocations++
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: lifecycleCredential(createdAt, "credential-issued", &revokedAt)})
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	originalPersist := lifecyclePersist
	lifecyclePersist = func(string) (sidecar.CredentialResult, error) {
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
	lifecycleReplace = func(current sidecar.CredentialResult, token string) error {
		if err := sidecar.ReplaceCredential(current, token); err != nil {
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
	lifecycleReplace = func(current sidecar.CredentialResult, token string) error {
		if err := sidecar.ReplaceCredential(current, token); err != nil {
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

func TestLogoutRetainsCredential_when_remoteRevocationFails(t *testing.T) {
	// Given
	token := validDoctorToken(86)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(87), &revocations, true)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, token)

	// When
	code := runCLI([]string{"logout"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
}

func TestLogoutReportsFailure_when_localCleanupFailsAfterRemoteRevocation(t *testing.T) {
	// Given
	token := validDoctorToken(88)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(89), &revocations, false)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, token)
	t.Setenv(sidecar.TokenFileEnvironment, t.TempDir())

	// When
	code := runCLI([]string{"logout"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
}
