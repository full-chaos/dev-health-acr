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
)

// CredentialStore persists ACR credential metadata in PostgreSQL. The caller
// owns database construction and driver selection; this package never parses
// or logs DSNs.
type CredentialStore struct {
	DB *sql.DB
}

func NewCredentialStore(db *sql.DB) (*CredentialStore, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &CredentialStore{DB: db}, nil
}

func (s *CredentialStore) Create(ctx context.Context, record storage.CredentialRecord) error {
	return insertCredential(ctx, s.DB, record)
}

func (s *CredentialStore) List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at
FROM acr.client_credentials
WHERE org_id = $1
ORDER BY created_at, credential_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()
	result := make([]contractsv1.ClientCredential, 0)
	for rows.Next() {
		credential, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		result = append(result, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", err)
	}
	return result, nil
}

func (s *CredentialStore) GetByID(ctx context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at
FROM acr.client_credentials
WHERE org_id = $1 AND credential_id = $2`, orgID, credentialID)
	credential, err := scanCredential(row)
	return credential, mapNotFound("get credential", err)
}

func (s *CredentialStore) FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at
FROM acr.client_credentials
WHERE token_hash = $1`, tokenHash)
	credential, err := scanCredential(row)
	return credential, mapNotFound("find credential", err)
}

func (s *CredentialStore) Rotate(ctx context.Context, orgID, credentialID string, replacement storage.CredentialRecord, previousValidUntil *time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential rotation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var result sql.Result
	if previousValidUntil == nil || !previousValidUntil.After(replacement.Metadata.CreatedAt) {
		result, err = tx.ExecContext(ctx, `
UPDATE acr.client_credentials
SET revoked_at = CASE
        WHEN revoked_at IS NULL OR revoked_at > $3 THEN $3
        ELSE revoked_at
    END
WHERE org_id = $1 AND credential_id = $2`, orgID, credentialID, replacement.Metadata.CreatedAt)
	} else {
		result, err = tx.ExecContext(ctx, `
UPDATE acr.client_credentials
SET expires_at = CASE
        WHEN expires_at IS NULL OR expires_at > $3 THEN $3
        ELSE expires_at
    END
WHERE org_id = $1 AND credential_id = $2`, orgID, credentialID, *previousValidUntil)
	}
	if err != nil {
		return fmt.Errorf("update previous credential: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("credential rotation rows affected: %w", err)
	}
	if rows != 1 {
		return storage.ErrNotFound
	}
	if err := insertCredential(ctx, tx, replacement); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential rotation: %w", err)
	}
	return nil
}

func (s *CredentialStore) Revoke(ctx context.Context, orgID, credentialID string, revokedAt time.Time) (contractsv1.ClientCredential, error) {
	row := s.DB.QueryRowContext(ctx, `
UPDATE acr.client_credentials
SET revoked_at = CASE
        WHEN revoked_at IS NULL OR revoked_at > $3 THEN $3
        ELSE revoked_at
    END
WHERE org_id = $1 AND credential_id = $2
RETURNING credential_id, name, token_prefix, org_id, repository_scopes, scopes,
          created_at, expires_at, revoked_at, last_used_at`, orgID, credentialID, revokedAt)
	credential, err := scanCredential(row)
	return credential, mapNotFound("revoke credential", err)
}

func (s *CredentialStore) TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
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
		return fmt.Errorf("touch credential last used: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("credential last-used rows affected: %w", err)
	}
	if rows != 1 {
		return storage.ErrNotFound
	}
	return nil
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
		return fmt.Errorf("insert credential: %w", err)
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
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
