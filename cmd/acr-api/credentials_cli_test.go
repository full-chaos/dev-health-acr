package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const credentialProcessEnvironment = "ACR_API_CREDENTIAL_PROCESS"

func TestCredentialCLIProcess(t *testing.T) {
	if os.Getenv(credentialProcessEnvironment) != "1" {
		return
	}
	arguments := os.Args
	for index, argument := range arguments {
		if argument == "--" {
			if err := run(arguments[index+1:]); err != nil {
				_, _ = os.Stderr.WriteString(err.Error() + "\n")
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestCredentialCLI_rejectsMissingRequiredCreateFlags(t *testing.T) {
	// Given
	var stdout, stderr bytes.Buffer

	// When
	err := runCredentialCLI(context.Background(), []string{"create"}, credentialEnvironment(nil), &stdout, &stderr)

	// Then
	require.Error(t, err)
	require.ErrorContains(t, err, "org")
}

func TestCredentialCLI_printsHelpWithoutOpeningPostgres(t *testing.T) {
	// Given
	var stdout, stderr bytes.Buffer

	// When
	err := runCredentialCLI(context.Background(), []string{"create", "--help"}, credentialEnvironment(nil), &stdout, &stderr)

	// Then
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "-org-id")
}

func TestCredentialCLI_printsOnlyCommandSpecificHelp(t *testing.T) {
	tests := []struct {
		command       string
		expectedFlag  string
		forbiddenFlag string
	}{
		{"create", "-repository-scope", "-credential-id"},
		{"list", "-org-id", "-repository-scope"},
		{"rotate", "-overlap", ""},
		{"revoke", "-credential-id", "-repository-scope"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			// Given
			var stdout, stderr bytes.Buffer

			// When
			err := runCredentialCLI(context.Background(), []string{test.command, "--help"}, credentialEnvironment(nil), &stdout, &stderr)

			// Then
			require.NoError(t, err)
			require.Contains(t, stderr.String(), test.expectedFlag)
			if test.forbiddenFlag != "" {
				require.NotContains(t, stderr.String(), test.forbiddenFlag)
			}
		})
	}
}

func TestCredentialCLI_rejectsDSNFlagWithoutEchoingIt(t *testing.T) {
	// Given
	secret := "postgres://acr:super-secret@db/acr"

	// When
	result := runCredentialCLIProcess(t, nil, "credentials", "create", "--dsn", secret)

	// Then
	require.NotZero(t, result.exitCode)
	require.NotContains(t, result.stdout, secret)
	require.NotContains(t, result.stderr, secret)
}

func TestCredentialCLI_rejectsInvalidInputsBeforeOpeningPostgres(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
	}{
		{"expired timestamp", []string{"create", "--org-id", "org", "--repository-scope", "acme/widgets", "--scope", "context:read", "--name", "test", "--actor", "actor", "--expires-at", "2020-01-01T00:00:00Z"}},
		{"unknown scope", []string{"create", "--org-id", "org", "--repository-scope", "acme/widgets", "--scope", "admin:all", "--name", "test", "--actor", "actor"}},
		{"invalid repository", []string{"create", "--org-id", "org", "--repository-scope", "../../escape", "--scope", "context:read", "--name", "test", "--actor", "actor"}},
		{"irrelevant overlap", []string{"create", "--org-id", "org", "--repository-scope", "acme/widgets", "--scope", "context:read", "--name", "test", "--actor", "actor", "--overlap", "1m"}},
		{"negative rotation overlap", []string{"rotate", "--org-id", "org", "--credential-id", "credential", "--repository-scope", "acme/widgets", "--scope", "context:read", "--name", "test", "--actor", "actor", "--overlap", "-1s"}},
		{"excessive rotation overlap", []string{"rotate", "--org-id", "org", "--credential-id", "credential", "--repository-scope", "acme/widgets", "--scope", "context:read", "--name", "test", "--actor", "actor", "--overlap", "16m"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			var stdout, stderr bytes.Buffer
			lookup := func(string) (string, bool) {
				t.Fatal("invalid CLI input opened PostgreSQL")
				return "", false
			}

			// When
			err := runCredentialCLI(context.Background(), test.arguments, lookup, &stdout, &stderr)

			// Then
			require.Error(t, err)
		})
	}
}

func TestCredentialCLI_createsOnceAndListsSecretFreeMetadata(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newCredentialTestDatabase(t, ctx)
	environment := []string{"ACR_POSTGRES_DSN=" + dsn}

	// When
	created := runCredentialCLIProcess(t, environment, "credentials", "create",
		"--org-id", "11111111-1111-1111-1111-111111111111",
		"--repository-scope", "acme/widgets",
		"--scope", "context:read,evidence:read",
		"--name", "operator-test",
		"--actor", "22222222-2222-2222-2222-222222222222")

	// Then
	require.Zero(t, created.exitCode, created.stderr)
	token := issuedTokenLine(t, created.stdout)
	require.NotContains(t, created.stderr, token)

	// When
	listed := runCredentialCLIProcess(t, environment, "credentials", "list",
		"--org-id", "11111111-1111-1111-1111-111111111111")

	// Then
	require.Zero(t, listed.exitCode, listed.stderr)
	require.Contains(t, listed.stdout, "operator-test")
	require.NotContains(t, listed.stdout, token)
	require.NotContains(t, listed.stderr, token)
	require.NotContains(t, listed.stdout, dsn)
	require.NotContains(t, listed.stderr, dsn)
}

func TestCredentialCLI_rotatesWithOverlapAndRevokes(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newCredentialTestDatabase(t, ctx)
	environment := []string{"ACR_POSTGRES_DSN=" + dsn}
	created := runCredentialCLIProcess(t, environment, "credentials", "create",
		"--org-id", "11111111-1111-1111-1111-111111111111",
		"--repository-scope", "acme/widgets",
		"--scope", "context:read",
		"--name", "operator-test",
		"--actor", "22222222-2222-2222-2222-222222222222")
	require.Zero(t, created.exitCode, created.stderr)
	credentialID := credentialIDFromList(t, environment)

	// When
	rotated := runCredentialCLIProcess(t, environment, "credentials", "rotate",
		"--org-id", "11111111-1111-1111-1111-111111111111",
		"--credential-id", credentialID,
		"--repository-scope", "acme/widgets",
		"--scope", "context:read",
		"--name", "operator-test-rotated",
		"--actor", "22222222-2222-2222-2222-222222222222",
		"--overlap", "5m")

	// Then
	require.Zero(t, rotated.exitCode, rotated.stderr)
	rotatedToken := issuedTokenLine(t, rotated.stdout)
	require.NotContains(t, rotated.stderr, rotatedToken)

	// When
	revoked := runCredentialCLIProcess(t, environment, "credentials", "revoke",
		"--org-id", "11111111-1111-1111-1111-111111111111",
		"--credential-id", credentialID,
		"--actor", "22222222-2222-2222-2222-222222222222")

	// Then
	require.Zero(t, revoked.exitCode, revoked.stderr)
	require.NotContains(t, revoked.stdout, strings.TrimSpace(created.stdout))
}

func TestCredentialCLI_rejectsUnsafeInputsWithoutSecretLeakage(t *testing.T) {
	// Given
	secret := "postgres://acr:super-secret@db/acr"
	environment := []string{"ACR_POSTGRES_DSN=" + secret}

	// When
	result := runCredentialCLIProcess(t, environment, "credentials", "create",
		"--org-id", "wrong-org",
		"--repository-scope", "../../escape",
		"--scope", "admin:all",
		"--name", "operator-test",
		"--actor", "actor",
		"--expires-at", "yesterday")

	// Then
	require.NotZero(t, result.exitCode)
	require.NotContains(t, result.stdout, secret)
	require.NotContains(t, result.stderr, secret)
}

type credentialCLIResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runCredentialCLIProcess(t *testing.T, environment []string, arguments ...string) credentialCLIResult {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCredentialCLIProcess$", "--")
	command.Args = append(command.Args, arguments...)
	command.Env = append(os.Environ(), credentialProcessEnvironment+"=1")
	command.Env = append(command.Env, environment...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := credentialCLIResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return result
	}
	exitError, ok := err.(*exec.ExitError)
	require.True(t, ok, err)
	result.exitCode = exitError.ExitCode()
	return result
}

func newCredentialTestDatabase(t *testing.T, ctx context.Context) string {
	t.Helper()
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase("acr"),
		tcpostgres.WithUsername("acr"),
		tcpostgres.WithPassword("acr"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	applyCredentialMigrations(t, ctx, db)
	return strings.TrimSpace(dsn)
}

func applyCredentialMigrations(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
}

func credentialIDFromList(t *testing.T, environment []string) string {
	t.Helper()
	result := runCredentialCLIProcess(t, environment, "credentials", "list", "--org-id", "11111111-1111-1111-1111-111111111111", "--json")
	require.Zero(t, result.exitCode, result.stderr)
	fields := strings.Split(result.stdout, "\"")
	for index := range fields {
		if fields[index] == "credential_id" && index+2 < len(fields) {
			return fields[index+2]
		}
	}
	t.Fatal("credential_id missing from list output")
	return ""
}

func credentialEnvironment(values map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func issuedTokenLine(t *testing.T, output string) string {
	t.Helper()
	lines := strings.FieldsFunc(output, func(r rune) bool { return r == '\n' || r == '\r' })
	require.Len(t, lines, 1)
	require.True(t, auth.IsTokenShapeValid(lines[0]))
	return lines[0]
}
