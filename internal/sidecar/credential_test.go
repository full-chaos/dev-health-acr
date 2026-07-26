package sidecar

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// validTestToken returns a deterministic, shape-valid ACR API token (the
// auth.TokenPrefix prefix followed by a 32-byte base64url secret) so tests
// can be told apart by seed while still passing auth.IsTokenShapeValid.
func validTestToken(seed byte) string {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = seed
	}
	return auth.TokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
}

var (
	envToken     = validTestToken(1)
	fileToken    = validTestToken(2)
	keyringToken = validTestToken(3)
)

// licenseShapedToken looks like a plausible Dev Health license artifact
// (nonblank, not the ACR token shape) to prove LoadCredential rejects it
// rather than treating "nonblank" as sufficient.
const licenseShapedToken = "dhlic_01J8QK7N5G8ZC3B6F2M4W9YXHT.eyJvcmciOiJhY21lIn0"

func stubKeyringLookup(t *testing.T, fn KeyringLookup) {
	t.Helper()
	original := currentKeyringLookup
	currentKeyringLookup = fn
	t.Cleanup(func() { currentKeyringLookup = original })
}

func writeTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCredentialPrefersEnvironment(t *testing.T) {
	t.Setenv(TokenEnvironment, envToken)
	t.Setenv(TokenFileEnvironment, "/does/not/exist")
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != envToken || result.Source != "environment" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialRequiresConfiguration(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "")
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadCredential(); err == nil {
		t.Fatal("missing credential configuration was accepted")
	}
}

func TestLoadCredentialPrefersEnvironmentOverKeyringAndFile(t *testing.T) {
	t.Setenv(TokenEnvironment, envToken)
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, "/does/not/exist")
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		t.Fatal("keyring must not be consulted when an environment credential is present")
		return "", false, nil
	})
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != envToken || result.Source != "environment" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialUsesKeyringBeforeFile(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenKeyringAccountEnvironment, "agent-a")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(_ context.Context, service, account string) (string, bool, error) {
		if service != "acr-sidecar-test" || account != "agent-a" {
			t.Fatalf("unexpected keyring lookup args: service=%q account=%q", service, account)
		}
		return keyringToken, true, nil
	})
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != keyringToken || result.Source != "keyring" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialFallsThroughToFileWhenKeyringMisses(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, nil
	})
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != fileToken || result.Source != "file" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialRejectsOperationalKeyringFailure_whenFallbackExists(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, errors.New("keyring backend unreachable")
	})
	_, err := LoadCredential()
	if err == nil {
		t.Fatal("operational keyring failure fell through to the credential file")
	}
}

func TestLoadCredentialFallsBackToFile_whenKeyringExecutableIsUnavailable(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, ErrExecutableUnavailable
	})

	// When
	result, err := LoadCredential()

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != fileToken || result.Source != "file" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialRejectsUntrustedKeyringExecutable_whenFallbackExists(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, ErrUntrustedExecutable
	})

	// When
	_, err := LoadCredential()

	// Then
	if !errors.Is(err, ErrUntrustedExecutable) {
		t.Fatalf("credential error = %v, want ErrUntrustedExecutable", err)
	}
}

func TestLoadCredentialRejectsTimedOutKeyringLookup_whenFallbackExists(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, context.DeadlineExceeded
	})

	// When
	_, err := LoadCredential()

	// Then
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("credential error = %v, want context deadline exceeded", err)
	}
}

func TestLoadCredentialKeyringLookupContextIsBounded(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", t.TempDir())
	stubKeyringLookup(t, func(ctx context.Context, _, _ string) (string, bool, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("keyring lookup context must carry a bounded deadline")
		}
		return "", false, nil
	})
	if _, err := LoadCredential(); err == nil {
		t.Fatal("missing credential configuration was accepted")
	}
}

func TestLoadCredentialReturnsMissing_whenDefaultTokenFileIsAbsent(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", t.TempDir())

	// When
	_, err := LoadCredential()

	// Then
	if !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("credential error = %v, want ErrCredentialMissing", err)
	}
}

func TestLoadCredentialRejectsBlankKeyringToken_whenFallbackExists(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "   ", true, nil
	})
	_, err := LoadCredential()
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("credential error = %v, want ErrCredentialShapeInvalid", err)
	}
}

func TestLoadCredentialRejectsShapeInvalidEnvironmentToken(t *testing.T) {
	t.Setenv(TokenEnvironment, licenseShapedToken)
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	_, err := LoadCredential()
	if err == nil {
		t.Fatal("a license-shaped ACR_API_TOKEN value was accepted")
	}
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("expected ErrCredentialShapeInvalid, got %v", err)
	}
}

func TestLoadCredentialRejectsShapeInvalidEnvironmentTokenWithoutFallingThrough(t *testing.T) {
	// A shape-invalid environment override must fail loudly, not silently
	// fall through to a (possibly stale or wrong) file credential: the
	// operator needs to see their misconfiguration, not have it masked.
	t.Setenv(TokenEnvironment, "not-even-license-shaped-just-garbage")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	if _, err := LoadCredential(); err == nil {
		t.Fatal("a shape-invalid ACR_API_TOKEN value silently fell through to the token file")
	}
}

func TestLoadCredentialRejectsShapeInvalidFileToken(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, licenseShapedToken+"\n"))
	_, err := LoadCredential()
	if err == nil {
		t.Fatal("a license-shaped credential file value was accepted")
	}
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("expected ErrCredentialShapeInvalid, got %v", err)
	}
}

func TestLoadCredentialRejectsShapeInvalidKeyringToken_whenFallbackExists(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return licenseShapedToken, true, nil
	})
	_, err := LoadCredential()
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("credential error = %v, want ErrCredentialShapeInvalid", err)
	}
}

func TestLoadCredentialRejectsShapeInvalidTokenEvenWhenNoFallbackExists(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", t.TempDir())
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return licenseShapedToken, true, nil
	})
	if _, err := LoadCredential(); err == nil {
		t.Fatal("a license-shaped credential was accepted when it was the only source configured")
	}
}
