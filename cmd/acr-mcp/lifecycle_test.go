package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestLoginPersistsCredentialAndDoctorDiscoversIt_when_deviceGrantRedeems(t *testing.T) {
	// Given
	token := validDoctorToken(81)
	server := newLifecycleServer(t, token, []string{"success"})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
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

func TestLoginAddsFiveSecondsAfterSlowDown_when_pollingDeviceGrant(t *testing.T) {
	// Given
	token := validDoctorToken(82)
	server := newLifecycleServer(t, token, []string{"slow_down", "success"})
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
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

func TestLoginFails_when_deviceAuthorizationIsDeniedOrExpired(t *testing.T) {
	for _, outcome := range []string{"access_denied", "expired_token"} {
		t.Run(outcome, func(t *testing.T) {
			// Given
			server := newLifecycleServer(t, validDoctorToken(90), []string{outcome})
			t.Setenv(sidecar.APIURLEnvironment, server.URL)
			t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
			t.Setenv(sidecar.TokenEnvironment, "")
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

func TestRefreshRevokesSuccessor_when_localPersistenceFails(t *testing.T) {
	// Given
	original := validDoctorToken(84)
	successor := validDoctorToken(85)
	revocations := 0
	server := newCredentialLifecycleServer(t, original, successor, &revocations, false)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, original)
	originalPersist := lifecyclePersist
	lifecyclePersist = func(string) (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{}, errors.New("persistence failure")
	}
	t.Cleanup(func() { lifecyclePersist = originalPersist })

	// When
	code := runCLI([]string{"login", "--refresh"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("refresh exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 {
		t.Fatalf("successor revocations = %d, want 1", revocations)
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
	originalDelete := lifecycleDelete
	deleted := false
	lifecycleDelete = func() error { deleted = true; return nil }
	t.Cleanup(func() { lifecycleDelete = originalDelete })

	// When
	code := runCLI([]string{"logout"})

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("logout exit code = %d, want %d", code, lifecycleExitFailure)
	}
	if revocations != 1 || deleted {
		t.Fatalf("logout remote-first behavior revocations=%d deleted=%t", revocations, deleted)
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
	originalDelete := lifecycleDelete
	lifecycleDelete = func() error { return errors.New("local delete failure") }
	t.Cleanup(func() { lifecycleDelete = originalDelete })

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
