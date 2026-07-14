package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialCLI_rejectsMalformedTypedFlagsWithoutEchoingValues(t *testing.T) {
	for _, arguments := range [][]string{
		{"credentials", "rotate", "--overlap", "typed-flag-secret"},
		{"credentials", "list", "--json=typed-flag-secret"},
	} {
		t.Run(strings.Join(arguments[1:3], " "), func(t *testing.T) {
			// Given
			secret := "typed-flag-secret"

			// When
			result := runCredentialCLIProcess(t, nil, arguments...)

			// Then
			require.NotZero(t, result.exitCode)
			require.Contains(t, result.stderr, "invalid credential flag value")
			require.NotContains(t, result.stdout, secret)
			require.NotContains(t, result.stderr, secret)
		})
	}
}

func TestCredentialCLI_printsParentHelpWithoutOpeningPostgres(t *testing.T) {
	// Given
	var stdout, stderr bytes.Buffer

	// When
	err := runCredentialCLI(context.Background(), []string{"--help"}, credentialEnvironment(nil), &stdout, &stderr)

	// Then
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "create")
	require.Contains(t, stderr.String(), "list")
	require.Contains(t, stderr.String(), "rotate")
	require.Contains(t, stderr.String(), "revoke")
}

func TestCredentialCLI_passesPoolerAdminDSNToPostgresOpener(t *testing.T) {
	// Given
	lookup := credentialEnvironment(map[string]string{
		postgresDSNEnvironment:            "postgres://localhost/acr?sslmode=verify-full",
		postgresPoolerAdminDSNEnvironment: "%",
	})

	// When
	err := runCredentialCLI(context.Background(), []string{"list", "--org-id", "org"}, lookup, &bytes.Buffer{}, &bytes.Buffer{})

	// Then
	require.ErrorContains(t, err, "invalid PgBouncer administration configuration")
}

func TestCredentialCLIRejectsInsecurePostgreSQLOutsideTestEnvironment(t *testing.T) {
	// Given
	lookup := credentialEnvironment(map[string]string{postgresDSNEnvironment: "postgres://user:sentinel-secret@db.example/acr?sslmode=disable"})

	// When
	err := runCredentialCLI(context.Background(), []string{"list", "--org-id", "11111111-1111-1111-1111-111111111111"}, lookup, &bytes.Buffer{}, &bytes.Buffer{})

	// Then
	require.ErrorContains(t, err, "verified TLS")
	require.NotContains(t, err.Error(), "sentinel-secret")
}

func TestCredentialCLI_rejectsDeclaredConnectionKindContradictions(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "direct with pooler admin DSN", values: map[string]string{
			postgresDSNEnvironment:            "postgres://localhost/acr?sslmode=verify-full",
			postgresPoolerAdminDSNEnvironment: "postgres://pooler-admin",
			postgresConnectionKindEnvironment: "direct",
		}},
		{name: "pgbouncer without pooler admin DSN", values: map[string]string{
			postgresDSNEnvironment:            "postgres://localhost/acr?sslmode=verify-full",
			postgresConnectionKindEnvironment: "pgbouncer",
		}},
		{name: "unknown kind", values: map[string]string{
			postgresDSNEnvironment:            "postgres://localhost/acr?sslmode=verify-full",
			postgresConnectionKindEnvironment: "auto",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			lookup := credentialEnvironment(test.values)

			// When
			err := runCredentialCLI(context.Background(), []string{"list", "--org-id", "org"}, lookup, &bytes.Buffer{}, &bytes.Buffer{})

			// Then
			require.ErrorContains(t, err, "ACR_POSTGRES_CONNECTION_KIND")
		})
	}
}
