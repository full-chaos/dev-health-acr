package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
	"github.com/jackc/pgx/v5/pgconn"
)

// CredentialStore persists ACR credential metadata in PostgreSQL. The caller
// owns database construction and driver selection; this package never parses
// or logs DSNs.
type credentialStore struct {
	DB    *sql.DB
	audit *AuditStore
	now   func() time.Time
}

func NewCredentialStore(db *sql.DB, audit *AuditStore) (*storage.CredentialLifecycle, error) {
	_, lifecycle, err := newCredentialStore(db, audit, time.Now)
	return lifecycle, err
}

func newCredentialStore(db *sql.DB, audit *AuditStore, now func() time.Time) (*credentialStore, *storage.CredentialLifecycle, error) {
	if db == nil {
		return nil, nil, storage.ErrInvalidCredentialLifecycle
	}
	if audit == nil || audit.DB != db || audit.GenerateID == nil || now == nil {
		return nil, nil, storage.ErrInvalidCredentialLifecycle
	}
	store := &credentialStore{DB: db, audit: audit, now: now}
	if err := audit.bindLifecycle(store); err != nil {
		return nil, nil, err
	}
	lifecycle, err := credentiallifecycle.New(credentiallifecycle.Backend{
		Store: store, Create: store.createCredential, Rotate: store.rotateCredential, Revoke: store.revokeCredential, Rollback: store.rollbackCredentialRotation,
	})
	if err != nil {
		return nil, nil, err
	}
	return store, lifecycle, nil
}

func (s *credentialStore) List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at, workload_binding_id, resource
FROM acr.client_credentials
WHERE org_id = $1
ORDER BY created_at, credential_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", sanitizeDatabaseError(err))
	}
	defer rows.Close()
	result := make([]contractsv1.ClientCredential, 0)
	for rows.Next() {
		credential, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("scan credential: %w", sanitizeDatabaseError(err))
		}
		result = append(result, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", sanitizeDatabaseError(err))
	}
	return result, nil
}

func (s *credentialStore) GetByID(ctx context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at, workload_binding_id, resource
FROM acr.client_credentials
WHERE org_id = $1 AND credential_id = $2`, orgID, credentialID)
	credential, err := scanCredential(row)
	return credential, mapNotFound("get credential", err)
}

func (s *credentialStore) FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at, workload_binding_id, resource
FROM acr.client_credentials
WHERE token_hash = $1
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`, tokenHash)
	credential, err := scanCredential(row)
	return credential, mapNotFound("find credential", err)
}

func (s *credentialStore) TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `
UPDATE acr.client_credentials
SET last_used_at = CASE
        WHEN last_used_at IS NULL OR last_used_at < $2 THEN $2
        ELSE last_used_at
    END,
    last_used_ip = CASE
        WHEN last_used_at IS NULL OR last_used_at < $2 THEN NULLIF($3, '')::inet
        ELSE last_used_ip
    END,
    last_used_user_agent = CASE
        WHEN last_used_at IS NULL OR last_used_at < $2 THEN NULLIF($4, '')
        ELSE last_used_user_agent
    END
WHERE credential_id = $1`, credentialID, usedAt, ip, userAgent)
	if err != nil {
		return fmt.Errorf("touch credential last used: %w", sanitizeDatabaseError(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("credential last-used rows affected: %w", sanitizeDatabaseError(err))
	}
	if rows != 1 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *credentialStore) ready(ctx context.Context) error {
	if s == nil || s.DB == nil || s.audit == nil || s.now == nil || ctx == nil {
		return storage.ErrInvalidCredentialLifecycle
	}
	return ctx.Err()
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertCredential(ctx context.Context, executor execer, record storage.CredentialRecord) error {
	repositories, err := marshalJSONStringArray(record.Metadata.RepositoryScopes)
	if err != nil {
		return fmt.Errorf("encode repository scopes: %w", err)
	}
	scopes, err := marshalJSONStringArray(record.Metadata.Scopes)
	if err != nil {
		return fmt.Errorf("encode credential scopes: %w", err)
	}
	var workloadBindingID any
	if record.Metadata.WorkloadBindingID != nil {
		workloadBindingID = *record.Metadata.WorkloadBindingID
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO acr.client_credentials (
    credential_id, org_id, name, token_prefix, token_hash,
    repository_scopes, scopes, created_by, created_at, expires_at,
    revoked_at, last_used_at, last_used_ip, last_used_user_agent, workload_binding_id, resource
) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, NULLIF($8, ''), $9, $10, $11, $12, NULLIF($13, '')::inet, NULLIF($14, ''), $15, NULLIF($16, ''))`,
		record.Metadata.CredentialID,
		record.Metadata.OrgID,
		record.Metadata.Name,
		record.Metadata.TokenPrefix,
		record.TokenHash,
		string(repositories),
		string(scopes),
		record.CreatedBy,
		record.Metadata.CreatedAt,
		record.Metadata.ExpiresAt,
		record.Metadata.RevokedAt,
		record.Metadata.LastUsedAt,
		record.LastUsedIP,
		record.LastUsedUserAgent,
		workloadBindingID,
		record.Metadata.Resource,
	)
	if err != nil {
		return fmt.Errorf("insert credential: %w", sanitizeDatabaseError(err))
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanCredential(row scanner) (contractsv1.ClientCredential, error) {
	var credential contractsv1.ClientCredential
	var repositoryJSON, scopeJSON []byte
	var workloadBindingID sql.NullString
	var resource sql.NullString
	err := row.Scan(
		&credential.CredentialID,
		&credential.Name,
		&credential.TokenPrefix,
		&credential.OrgID,
		&repositoryJSON,
		&scopeJSON,
		&credential.CreatedAt,
		&credential.ExpiresAt,
		&credential.RevokedAt,
		&credential.LastUsedAt,
		&workloadBindingID,
		&resource,
	)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := json.Unmarshal(repositoryJSON, &credential.RepositoryScopes); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("decode repository scopes: %w", err)
	}
	if err := json.Unmarshal(scopeJSON, &credential.Scopes); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("decode credential scopes: %w", err)
	}
	if workloadBindingID.Valid {
		credential.WorkloadBindingID = &workloadBindingID.String
	}
	if resource.Valid {
		credential.Resource = resource.String
	}
	credential.SchemaVersion = contractsv1.ClientCredentialSchema
	return credential, nil
}

// marshalJSONStringArray encodes a []string for a `JSONB NOT NULL` column
// carrying a `CHECK (jsonb_typeof(col) = 'array')` constraint. Seven columns
// across four tables share this exact shape: oauth_clients.redirect_uris,
// device_authorizations' repository_hints/authorized_repository_scopes/
// authorized_scopes, client_credentials' repository_scopes/scopes, and
// workload_bindings.repository_scopes (migration 0030). Every INSERT/UPDATE
// call site in this package touching one of the first six goes through this
// helper. workload_bindings.repository_scopes is the seventh: its adapter
// (workload_bindings.go) only ever SELECTs the column, with no Go
// INSERT/UPDATE call site of its own to fix -- it carries the identical
// constraint but nothing in this package can put a bad value in it.
// encoding/json marshals a nil []string as the JSON literal `null`, whose
// jsonb_typeof is "null", not "array" -- every one of those CHECK
// constraints then rejects the row, and
// the caller sees a generic wrapped/sanitized database error with no hint
// this is why (CHAOS-6233: a device-only OAuth client, which legitimately
// has zero redirect_uris, failed exactly this way). This is the single seam
// every INSERT/UPDATE touching one of those columns must go through instead
// of a bare json.Marshal, so a nil-vs-empty slice can never reach one of
// them as anything but `[]`.
func marshalJSONStringArray(values []string) ([]byte, error) {
	if values == nil {
		values = []string{}
	}
	return json.Marshal(values)
}

func mapNotFound(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, sanitizeDatabaseError(err))
	}
	return nil
}

func sanitizeDatabaseError(err error) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrUnavailable) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if class := classifyDatabaseError(err); class != nil {
			return fmt.Errorf("%w: %w", storage.ErrConflict, class)
		}
		return storage.ErrConflict
	}
	if class := classifyDatabaseError(err); class != nil {
		return fmt.Errorf("%w: %w", storage.ErrUnavailable, class)
	}
	return storage.ErrUnavailable
}

// postgresErrorClassNames maps a Postgres SQLSTATE code to a closed-
// vocabulary class name for CHAOS-6278's dependency-failure logging.
// Deliberately not exhaustive: SQLState itself always survives on
// storage.DependencyErrorClass regardless, so an unmapped code degrades to
// "unclassified" rather than losing the code entirely. Covers the classes
// this repository has actually needed to tell apart: constraint violations
// (the CHAOS-6233 register-503 incident's own root cause,
// oauth_clients_redirect_uris_check, was a check_violation), a missing
// runtime-role grant (CHAOS-6277's own class, insufficient_privilege), and
// connection-level failures.
var postgresErrorClassNames = map[string]string{
	"23502": "not_null_violation",
	"23503": "foreign_key_violation",
	"23505": "unique_violation",
	"23514": "check_violation",
	"42501": "insufficient_privilege",
	"28000": "invalid_authorization_specification",
	"28P01": "invalid_password",
	"08000": "connection_exception",
	"08003": "connection_does_not_exist",
	"08006": "connection_failure",
	"57014": "query_canceled",
}

// classifyDatabaseError builds the safe classification CHAOS-6278's
// dependency-failure logging needs from err. Handles two shapes:
//
//   - *pgconn.PgError: a real server-side SQLSTATE (a constraint violation,
//     a permission denial once the connection succeeded, ...) -- classified
//     by postgresErrorClassNames, with the constraint/table names it
//     carries.
//   - *pgconn.ConnectError: a dial-time failure (refused, timed out, DNS,
//     TLS) -- the connection attempt itself never reached a point where
//     Postgres could hand back a SQLSTATE at all (codex round cf-6278-r1b's
//     P1, executed repro: a refused connection classified as nothing,
//     indistinguishable from an unrelated unclassified failure). Classified
//     under the SQL-standard "08" connection-exception class (SQLState
//     "08000") even though no server ever produced that code, since that is
//     the closed vocabulary's own bucket for exactly this failure shape.
//     ConnectError.Config (the attempted connection's own DSN) is
//     DELIBERATELY never read here -- this repository never logs raw
//     transports/DSNs (AGENTS.md), so only the class name is derived, never
//     the address that failed.
//
// Anything else (context.Canceled/DeadlineExceeded are handled by the
// caller before this is reached; any other error) returns nil -- "no class
// available" -- so a caller's errors.As simply finds nothing rather than a
// zero-value class with an empty SQLState.
func classifyDatabaseError(err error) *storage.DependencyErrorClass {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		name, ok := postgresErrorClassNames[pgErr.Code]
		if !ok {
			name = "unclassified"
		}
		return &storage.DependencyErrorClass{
			SQLState:   pgErr.Code,
			Class:      name,
			Constraint: pgErr.ConstraintName,
			Table:      pgErr.TableName,
		}
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return &storage.DependencyErrorClass{
			SQLState: "08000",
			Class:    "connection_failure",
		}
	}
	return nil
}
