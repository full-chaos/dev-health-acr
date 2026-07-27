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
       created_at, expires_at, revoked_at, last_used_at
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
       created_at, expires_at, revoked_at, last_used_at
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
       created_at, expires_at, revoked_at, last_used_at
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
	repositories, err := json.Marshal(record.Metadata.RepositoryScopes)
	if err != nil {
		return fmt.Errorf("encode repository scopes: %w", err)
	}
	scopes, err := json.Marshal(record.Metadata.Scopes)
	if err != nil {
		return fmt.Errorf("encode credential scopes: %w", err)
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO acr.client_credentials (
    credential_id, org_id, name, token_prefix, token_hash,
    repository_scopes, scopes, created_by, created_at, expires_at,
    revoked_at, last_used_at, last_used_ip, last_used_user_agent
) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, NULLIF($8, ''), $9, $10, $11, $12, NULLIF($13, '')::inet, NULLIF($14, ''))`,
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
	credential.SchemaVersion = contractsv1.ClientCredentialSchema
	return credential, nil
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
		return storage.ErrConflict
	}
	return storage.ErrUnavailable
}
