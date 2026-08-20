package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkloadBindingStore is the read-only PostgreSQL lookup for
// acr.workload_bindings (CHAOS-4013). There is no write path here by
// design -- see storage.WorkloadBinding's doc comment.
type WorkloadBindingStore struct {
	DB *sql.DB
}

func NewWorkloadBindingStore(db *sql.DB) (*WorkloadBindingStore, error) {
	if db == nil {
		return nil, fmt.Errorf("workload binding store: %w", storage.ErrInvalidCredentialLifecycle)
	}
	return &WorkloadBindingStore{DB: db}, nil
}

func (s *WorkloadBindingStore) Lookup(ctx context.Context, key storage.WorkloadBindingKey) (storage.WorkloadBinding, error) {
	if s == nil || s.DB == nil {
		return storage.WorkloadBinding{}, storage.ErrInvalidCredentialLifecycle
	}
	if err := ctx.Err(); err != nil {
		return storage.WorkloadBinding{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
SELECT binding_id, org_id, role, repository_scopes, disabled_at
FROM acr.workload_bindings
WHERE trust_domain = $1 AND namespace = $2 AND service_account_name = $3 AND service_account_uid = $4`,
		key.TrustDomain, key.Namespace, key.ServiceAccountName, key.ServiceAccountUID)
	var binding storage.WorkloadBinding
	var repositoryJSON []byte
	if err := row.Scan(&binding.BindingID, &binding.OrgID, &binding.Role, &repositoryJSON, &binding.DisabledAt); err != nil {
		return storage.WorkloadBinding{}, mapNotFound("lookup workload binding", err)
	}
	if err := json.Unmarshal(repositoryJSON, &binding.RepositoryScopes); err != nil {
		return storage.WorkloadBinding{}, fmt.Errorf("decode workload binding repository scopes: %w", err)
	}
	return binding, nil
}

// NewWorkloadCredentialPurger returns a bounded, batched purge function for
// expired workload-exchanged credential rows (acr.client_credentials rows
// carrying a non-null workload_binding_id). A workload re-exchanges a
// fresh row roughly every 10 minutes for as long as it runs, so these rows
// accumulate far faster than ordinary long-lived credentials; unlike
// ordinary credentials they are hard-deleted once expired rather than left
// revoked; the issuance audit event already captured everything worth
// keeping. Wired into the same bounded-tick purge loop pattern as
// packets.PurgeExpiredWithAudit (see postgres.go/postgres_purge.go).
func NewWorkloadCredentialPurger(db *sql.DB) func(ctx context.Context, before time.Time, limit int) (int, error) {
	return func(ctx context.Context, before time.Time, limit int) (int, error) {
		if db == nil {
			return 0, storage.ErrInvalidCredentialLifecycle
		}
		result, err := db.ExecContext(ctx, `
DELETE FROM acr.client_credentials
WHERE credential_id IN (
    SELECT credential_id FROM acr.client_credentials
    WHERE workload_binding_id IS NOT NULL
      AND expires_at IS NOT NULL
      AND expires_at < $1
    ORDER BY expires_at
    LIMIT $2
)`, before, limit)
		if err != nil {
			return 0, fmt.Errorf("purge expired workload credentials: %w", sanitizeDatabaseError(err))
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("purge expired workload credentials rows affected: %w", sanitizeDatabaseError(err))
		}
		return int(rows), nil
	}
}
