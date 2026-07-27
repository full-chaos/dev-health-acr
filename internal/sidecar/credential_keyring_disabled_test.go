package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertInvalidKeyringDisabledSetting(t *testing.T, err error, raw string) {
	t.Helper()
	if err == nil {
		t.Fatal("invalid keyring disable setting was accepted")
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("expected ConfigError, got %T: %v", err, err)
	}
	if configErr.Field != TokenKeyringDisabledEnvironment {
		t.Fatalf("unexpected config field: %q", configErr.Field)
	}
	if configErr.Detail != "must be \"true\" or \"false\"" {
		t.Fatalf("unexpected config error detail: %q", configErr.Detail)
	}
	if strings.Contains(err.Error(), raw) {
		t.Fatalf("keyring disable error leaked configured value: %v", err)
	}
}

func writeDefaultCredentialFile(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".acr", "token")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCredentialKeyringDisabledSettingControlsLookup(t *testing.T) {
	cases := []struct {
		name             string
		value            string
		wantSource       string
		wantLookupCalled bool
		wantInvalid      bool
	}{
		{name: "true skips lookup", value: "true", wantSource: "file"},
		{name: "false permits lookup", value: "false", wantSource: "keyring", wantLookupCalled: true},
		{name: "invalid fails before lookup", value: "invalid", wantInvalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			home := t.TempDir()
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenKeyringDisabledEnvironment, tc.value)
			t.Setenv(TokenKeyringServiceEnvironment, "")
			t.Setenv(TokenKeyringAccountEnvironment, "")
			t.Setenv(TokenFileEnvironment, "")
			t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
			t.Setenv("HOME", home)
			writeDefaultCredentialFile(t, home)
			lookupCalled := false
			stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
				lookupCalled = true
				return keyringToken, true, nil
			})

			// When
			credential, err := LoadCredential()

			// Then
			if tc.wantInvalid {
				assertInvalidKeyringDisabledSetting(t, err, tc.value)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if credential.Source != tc.wantSource {
					t.Fatalf("unexpected credential source: %q", credential.Source)
				}
			}
			if lookupCalled != tc.wantLookupCalled {
				t.Fatalf("keyring lookup called=%t, want %t", lookupCalled, tc.wantLookupCalled)
			}
		})
	}
}

// ACR_API_TOKEN_KEYRING_DISABLED governs only the keyring seam, which sits
// strictly below the environment in the documented precedence. Reading that
// flag before resolving ACR_API_TOKEN inverted the precedence for its own
// failure mode: a malformed flag rejected a perfectly valid environment
// credential and took down every agent client that supplies one.
func TestLoadCredentialPrefersEnvironmentToken_whenKeyringDisableFlagIsMalformed(t *testing.T) {
	// Given
	home := t.TempDir()
	environmentToken := validTestToken(59)
	t.Setenv(TokenEnvironment, environmentToken)
	t.Setenv(TokenKeyringDisabledEnvironment, "1")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	writeDefaultCredentialFile(t, home)
	lookupCalled := false
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		lookupCalled = true
		return keyringToken, true, nil
	})

	// When
	credential, err := LoadCredential()

	// Then
	if err != nil {
		t.Fatalf("a malformed keyring flag rejected a valid environment credential: %v", err)
	}
	if credential.Source != "environment" || credential.Token != environmentToken {
		t.Fatalf("credential = %+v, want the environment token", credential)
	}
	if lookupCalled {
		t.Fatal("an unreadable keyring flag still authorized a keyring lookup")
	}
}

// The environment keeps its precedence, not its validation: a malformed
// ACR_API_TOKEN is still an error rather than a silent fall-through to a
// lower-precedence source, whatever the keyring flag says.
func TestLoadCredentialRejectsMalformedEnvironmentToken_beforeReadingTheKeyringFlag(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv(TokenEnvironment, "not-an-acr-token")
	t.Setenv(TokenKeyringDisabledEnvironment, "1")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", home)
	writeDefaultCredentialFile(t, home)
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		t.Error("an unreadable keyring flag still authorized a keyring lookup")
		return "", false, nil
	})

	// When
	_, err := LoadCredential()

	// Then
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("load error = %v, want ErrCredentialShapeInvalid", err)
	}
}

func TestPersistCredentialKeyringDisabledSettingControlsWriter(t *testing.T) {
	cases := []struct {
		name             string
		value            string
		wantSource       string
		wantWriterCalled bool
		wantInvalid      bool
	}{
		{name: "true skips writer", value: "true", wantSource: "file"},
		{name: "false permits writer", value: "false", wantSource: "keyring", wantWriterCalled: true},
		{name: "invalid fails before writer", value: "invalid", wantInvalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			t.Setenv(TokenKeyringDisabledEnvironment, tc.value)
			t.Setenv(TokenKeyringServiceEnvironment, "")
			t.Setenv(TokenKeyringAccountEnvironment, "")
			t.Setenv(TokenFileEnvironment, "")
			t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
			t.Setenv("HOME", t.TempDir())
			writerCalled := false
			stubKeyringWriter(t, func(context.Context, string, string, string) error {
				writerCalled = true
				return nil
			})

			// When
			credential, err := PersistCredential(fileToken)

			// Then
			if tc.wantInvalid {
				assertInvalidKeyringDisabledSetting(t, err, tc.value)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if credential.Source != tc.wantSource {
					t.Fatalf("unexpected persistence source: %q", credential.Source)
				}
			}
			if writerCalled != tc.wantWriterCalled {
				t.Fatalf("keyring writer called=%t, want %t", writerCalled, tc.wantWriterCalled)
			}
		})
	}
}

func TestDeleteCredentialKeyringDisabledSettingControlsLookupAndDeletion(t *testing.T) {
	cases := []struct {
		name              string
		value             string
		wantLookupCalled  bool
		wantDeleterCalled bool
		wantInvalid       bool
	}{
		{name: "true skips lookup and deletion", value: "true"},
		{name: "false permits lookup and deletion", value: "false", wantLookupCalled: true, wantDeleterCalled: true},
		{name: "invalid fails before lookup and deletion", value: "invalid", wantInvalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			home := t.TempDir()
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenKeyringDisabledEnvironment, tc.value)
			t.Setenv(TokenKeyringServiceEnvironment, "")
			t.Setenv(TokenKeyringAccountEnvironment, "")
			t.Setenv(TokenFileEnvironment, "")
			t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
			t.Setenv("HOME", home)
			writeDefaultCredentialFile(t, home)
			lookupCalled := false
			deleterCalled := false
			stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
				lookupCalled = true
				return keyringToken, true, nil
			})
			stubKeyringDeleter(t, func(context.Context, string, string) error {
				deleterCalled = true
				return nil
			})

			// When
			err := DeleteCredential()

			// Then
			if tc.wantInvalid {
				assertInvalidKeyringDisabledSetting(t, err, tc.value)
			} else if err != nil {
				t.Fatal(err)
			}
			if lookupCalled != tc.wantLookupCalled {
				t.Fatalf("keyring lookup called=%t, want %t", lookupCalled, tc.wantLookupCalled)
			}
			if deleterCalled != tc.wantDeleterCalled {
				t.Fatalf("keyring deleter called=%t, want %t", deleterCalled, tc.wantDeleterCalled)
			}
		})
	}
}
