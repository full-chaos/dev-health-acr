package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

const (
	postgresDSNEnvironment            = "ACR_POSTGRES_DSN"
	postgresDSNFileEnvironment        = "ACR_POSTGRES_DSN_FILE"
	postgresPoolerAdminDSNEnvironment = "ACR_POSTGRES_POOLER_ADMIN_DSN"
	postgresConnectionKindEnvironment = "ACR_POSTGRES_CONNECTION_KIND"
	maximumCredentialLifetime         = 365 * 24 * time.Hour
	maximumCredentialOverlap          = 15 * time.Minute
)

var errCredentialHelp = errors.New("credential help requested")

type lookupEnv func(string) (string, bool)

func runCredentialCLI(ctx context.Context, arguments []string, lookup lookupEnv, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("credentials command is required: use create, list, rotate, or revoke")
	}
	if arguments[0] == "--help" || arguments[0] == "-h" {
		_, err := fmt.Fprintln(stderr, "usage: acr-api credentials <command> [flags]\n\ncommands: create, list, rotate, revoke")
		return err
	}
	command := arguments[0]
	parsed, err := parseCredentialArguments(command, arguments[1:], stderr)
	if err != nil {
		if errors.Is(err, errCredentialHelp) {
			return nil
		}
		return err
	}
	expiresAt, err := parseCredentialExpiry(parsed.expiresAt, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := validateCredentialCommand(command, parsed, expiresAt); err != nil {
		return err
	}
	dsn, err := credentialDSN(lookup)
	if err != nil {
		return err
	}
	poolerAdminDSN, _ := lookup(postgresPoolerAdminDSNEnvironment)
	if err := validateDeclaredConnectionKind(lookup, poolerAdminDSN); err != nil {
		return err
	}
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn, PoolerAdminDSN: poolerAdminDSN})
	if err != nil {
		return fmt.Errorf("open PostgreSQL for credential command: %w", err)
	}
	defer db.Close()
	audit, err := storagepostgres.NewAuditStore(db)
	if err != nil {
		return fmt.Errorf("initialize audit store: %w", err)
	}
	store, err := storagepostgres.NewCredentialStore(db, audit)
	if err != nil {
		return fmt.Errorf("initialize credential store: %w", err)
	}
	service, err := auth.NewService(store, auth.ServiceOptions{})
	if err != nil {
		return fmt.Errorf("initialize credential service: %w", err)
	}
	switch command {
	case "create":
		issued, err := service.Create(ctx, auth.CreateCredentialRequest{
			OrgID: parsed.orgID, Name: parsed.name, RepositoryScopes: splitCSV(parsed.repositoryScopes),
			Scopes: splitCSV(parsed.scopes), CreatedBy: parsed.actor, ExpiresAt: expiresAt,
		})
		if err != nil {
			return fmt.Errorf("create credential: %w", err)
		}
		return writeIssuedCredential(stdout, stderr, issued, parsed.json)
	case "list":
		credentials, err := service.List(ctx, parsed.orgID)
		if err != nil {
			return fmt.Errorf("list credentials: %w", err)
		}
		return writeCredentialMetadata(stdout, credentials, parsed.json)
	case "rotate":
		issued, err := service.Rotate(ctx, auth.RotateCredentialRequest{
			OrgID: parsed.orgID, CredentialID: parsed.credentialID, Name: parsed.name,
			RepositoryScopes: splitCSV(parsed.repositoryScopes), Scopes: splitCSV(parsed.scopes),
			CreatedBy: parsed.actor, ExpiresAt: expiresAt, Overlap: parsed.overlap,
		})
		if err != nil {
			return fmt.Errorf("rotate credential: %w", err)
		}
		return writeIssuedCredential(stdout, stderr, issued, parsed.json)
	case "revoke":
		credential, err := service.Revoke(ctx, parsed.orgID, parsed.credentialID, parsed.actor)
		if err != nil {
			return fmt.Errorf("revoke credential: %w", err)
		}
		return writeCredentialMetadata(stdout, credential, parsed.json)
	default:
		return fmt.Errorf("unknown credentials command %q; use create, list, rotate, or revoke", command)
	}
}

func validateCredentialCommand(command string, arguments credentialCommandArguments, expiresAt *time.Time) error {
	switch command {
	case "create":
		if err := requireCredentialCreateArguments(arguments); err != nil {
			return err
		}
		return auth.ValidateCreateCredentialRequest(auth.CreateCredentialRequest{
			OrgID: arguments.orgID, Name: arguments.name, RepositoryScopes: splitCSV(arguments.repositoryScopes),
			Scopes: splitCSV(arguments.scopes), CreatedBy: arguments.actor, ExpiresAt: expiresAt,
		})
	case "list":
		return requireCredentialArguments(arguments, "org-id")
	case "rotate":
		if err := requireCredentialRotateArguments(arguments); err != nil {
			return err
		}
		if arguments.overlap < 0 || arguments.overlap > maximumCredentialOverlap {
			return fmt.Errorf("--overlap must be between zero and %s", maximumCredentialOverlap)
		}
		return auth.ValidateCreateCredentialRequest(auth.CreateCredentialRequest{
			OrgID: arguments.orgID, Name: arguments.name, RepositoryScopes: splitCSV(arguments.repositoryScopes),
			Scopes: splitCSV(arguments.scopes), CreatedBy: arguments.actor, ExpiresAt: expiresAt,
		})
	case "revoke":
		return requireCredentialArguments(arguments, "org-id", "credential-id", "actor")
	default:
		return fmt.Errorf("unknown credentials command %q; use create, list, rotate, or revoke", command)
	}
}

func credentialDSN(lookup lookupEnv) (string, error) {
	dsn, err := config.SecretValue(lookup, postgresDSNEnvironment)
	if err != nil {
		return "", err
	}
	if dsn == "" {
		return "", fmt.Errorf("%s or %s is required", postgresDSNEnvironment, postgresDSNFileEnvironment)
	}
	return dsn, nil
}

// validateDeclaredConnectionKind rejects a declared ACR_POSTGRES_CONNECTION_KIND
// that contradicts the presence of a PgBouncer administration DSN, applying
// the same rule the hosted server enforces. The declaration is optional for
// this administrative CLI; when absent, the PgBouncer administration DSN
// alone continues to control the session-mode probe.
func validateDeclaredConnectionKind(lookup lookupEnv, poolerAdminDSN string) error {
	raw, declared := lookup(postgresConnectionKindEnvironment)
	if !declared || strings.TrimSpace(raw) == "" {
		return nil
	}
	kind, err := runtimepostgres.ParseConnectionKind(raw)
	if err != nil {
		return err
	}
	return runtimepostgres.ValidateConnectionKind(kind, poolerAdminDSN)
}

func requireCredentialCreateArguments(arguments credentialCommandArguments) error {
	return requireCredentialArguments(arguments, "org-id", "repository-scope", "scope", "name", "actor")
}

func requireCredentialRotateArguments(arguments credentialCommandArguments) error {
	return requireCredentialArguments(arguments, "org-id", "credential-id", "repository-scope", "scope", "name", "actor")
}

func requireCredentialArguments(arguments credentialCommandArguments, required ...string) error {
	values := map[string]string{
		"org-id": arguments.orgID, "credential-id": arguments.credentialID, "repository-scope": arguments.repositoryScopes,
		"scope": arguments.scopes, "name": arguments.name, "actor": arguments.actor,
	}
	for _, name := range required {
		if strings.TrimSpace(values[name]) == "" {
			return fmt.Errorf("--%s is required", name)
		}
	}
	return nil
}

func parseCredentialExpiry(value string, now time.Time) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, value)
	if err != nil || !expiresAt.After(now) || expiresAt.After(now.Add(maximumCredentialLifetime)) {
		return nil, errors.New("--expires-at must be a future RFC3339 timestamp within one year")
	}
	return &expiresAt, nil
}

func splitCSV(value string) []string {
	return strings.Split(value, ",")
}

func writeIssuedCredential(stdout, stderr io.Writer, issued auth.IssuedCredential, metadataJSON bool) error {
	if _, err := fmt.Fprintln(stdout, issued.Token); err != nil {
		return fmt.Errorf("write credential token: %w", err)
	}
	return writeCredentialMetadata(stderr, issued.Credential, metadataJSON)
}

func writeCredentialMetadata(output io.Writer, value any, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(output).Encode(value)
	}
	_, err := fmt.Fprintln(output, value)
	return err
}
