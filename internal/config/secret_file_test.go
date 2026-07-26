package config

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretValue_readsTrimmedFile_whenOnlyFileSourceIsConfigured(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("  secret-value\n"), 0o600))
	lookup := testSecretEnvironment(map[string]string{"ACR_TEST_SECRET_FILE": path})

	// When
	value, err := SecretValue(lookup, "ACR_TEST_SECRET")

	// Then
	require.NoError(t, err)
	require.Equal(t, "secret-value", value)
}

func TestSecretValue_rejectsConflictingDirectAndFileSources(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("file-value"), 0o600))
	lookup := testSecretEnvironment(map[string]string{
		"ACR_TEST_SECRET":      "direct-value",
		"ACR_TEST_SECRET_FILE": path,
	})

	// When
	_, err := SecretValue(lookup, "ACR_TEST_SECRET")

	// Then
	require.ErrorIs(t, err, ErrSecretSourceConflict)
}

func TestSecretValue_rejectsGroupWritableFile(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("secret-value"), 0o600))
	require.NoError(t, os.Chmod(path, 0o660))
	lookup := testSecretEnvironment(map[string]string{"ACR_TEST_SECRET_FILE": path})

	// When
	_, err := SecretValue(lookup, "ACR_TEST_SECRET")

	// Then
	require.ErrorIs(t, err, ErrSecretFilePermissions)
}

func TestSecretValue_rejectsSymlinkSource(t *testing.T) {
	// Given
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	path := filepath.Join(directory, "secret")
	require.NoError(t, os.WriteFile(target, []byte("secret-value"), 0o600))
	require.NoError(t, os.Symlink(target, path))
	lookup := testSecretEnvironment(map[string]string{"ACR_TEST_SECRET_FILE": path})

	// When
	_, err := SecretValue(lookup, "ACR_TEST_SECRET")

	// Then
	require.ErrorIs(t, err, ErrSecretFileInvalid)
}

func TestSecretValue_classifiesUnreadableFileWithoutExposingItsPath(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing-secret")
	lookup := testSecretEnvironment(map[string]string{"ACR_TEST_SECRET_FILE": path})

	// When
	_, err := SecretValue(lookup, "ACR_TEST_SECRET")

	// Then
	require.ErrorIs(t, err, ErrSecretFileUnreadable)
	require.False(t, errors.Is(err, os.ErrNotExist))
	require.NotContains(t, err.Error(), path)
}

func TestLoad_readsHostedSecretsFromFileSources(t *testing.T) {
	// Given
	directory := t.TempDir()
	writeSecretFile(t, directory, "postgres", "postgres://acr@example/acr?sslmode=verify-full")
	writeSecretFile(t, directory, "clickhouse", "clickhouse://acr@example/acr?secure=true&skip_verify=false")
	writeSecretFile(t, directory, "kid", "test-kid")
	writeSecretFile(t, directory, "keys", "test-kid="+base64.RawStdEncoding.EncodeToString(make([]byte, 32)))
	writeSecretFile(t, directory, "token", "ops-token")
	lookup := testSecretEnvironment(map[string]string{
		"ACR_ENVIRONMENT":                       "test",
		"ACR_REQUIRE_BACKING_STORES":            "true",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
		"ACR_POSTGRES_DSN_FILE":                 filepath.Join(directory, "postgres"),
		"ACR_CLICKHOUSE_DSN_FILE":               filepath.Join(directory, "clickhouse"),
		"ACR_EVIDENCE_ID_ACTIVE_KID_FILE":       filepath.Join(directory, "kid"),
		"ACR_EVIDENCE_ID_KEYS_FILE":             filepath.Join(directory, "keys"),
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": filepath.Join(directory, "token"),
		"ACR_DEVICE_VERIFICATION_URL":           "https://verify.example.test/device",
	})

	// When
	cfg, err := load(lookup)

	// Then
	require.NoError(t, err)
	require.Equal(t, "postgres://acr@example/acr?sslmode=verify-full", cfg.PostgresDSN)
	require.Equal(t, "clickhouse://acr@example/acr?secure=true&skip_verify=false", cfg.ClickHouseDSN)
	require.Equal(t, "test-kid", cfg.EvidenceIDActiveKID)
	require.Len(t, cfg.EvidenceIDKeys, 1)
}

func testSecretEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeSecretFile(t *testing.T, directory, name, value string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(value), 0o600))
}
