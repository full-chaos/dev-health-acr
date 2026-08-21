package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *credentialStore) rollbackCredentialRotation(ctx context.Context, input storage.CredentialRotationRollbackInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if !uuidPattern.MatchString(input.OrgID) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("begin credential rotation rollback: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	ids := []string{input.SourceCredentialID, input.SuccessorCredentialID}
	slices.Sort(ids)
	first, err := lockedCredential(ctx, tx, input.OrgID, ids[0])
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	second, err := lockedCredential(ctx, tx, input.OrgID, ids[1])
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	source, successor := first, second
	if source.CredentialID != input.SourceCredentialID {
		source, successor = successor, source
	}
	now := s.now().UTC()
	if !input.RollbackUntil.After(now) || source.RevokedAt != nil ||
		source.ExpiresAt == nil || !source.ExpiresAt.After(now) ||
		!slices.Equal(source.RepositoryScopes, successor.RepositoryScopes) || !slices.Equal(source.Scopes, successor.Scopes) {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	var successorRotated bool
	err = tx.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM acr.audit_events
    WHERE org_id = $1 AND action = 'credential_rotated' AND resource_id = $2
)`, input.OrgID, input.SuccessorCredentialID).Scan(&successorRotated)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("verify successor rotation state: %w", sanitizeDatabaseError(err))
	}
	if successorRotated {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	var relation int
	err = tx.QueryRowContext(ctx, `
SELECT 1 FROM acr.audit_events
WHERE org_id = $1 AND action = 'credential_rotated' AND resource_id = $2
  AND metadata->>'replacement_credential_id' = $3
LIMIT 1`, input.OrgID, input.SourceCredentialID, input.SuccessorCredentialID).Scan(&relation)
	if errors.Is(err, sql.ErrNoRows) {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("verify credential rotation relation: %w", sanitizeDatabaseError(err))
	}
	result, err := tx.ExecContext(ctx, `
UPDATE acr.client_credentials
SET revoked_at = $3
WHERE org_id = $1 AND credential_id = $2 AND revoked_at IS NULL`, input.OrgID, input.SuccessorCredentialID, now)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("revoke rotation successor: %w", sanitizeDatabaseError(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("rotation rollback rows affected: %w", sanitizeDatabaseError(err))
	}
	if rows != 1 {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	successor.RevokedAt = cloneTime(&now)
	if err := s.audit.record(ctx, tx, credentialRevokedEvent(successor, input.ActorID, now)); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("record credential rotation rollback audit: %w", sanitizeDatabaseError(err))
	}
	if err := tx.Commit(); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("commit credential rotation rollback: %w", sanitizeDatabaseError(err))
	}
	return successor, nil
}

func (s *credentialStore) createCredential(ctx context.Context, input storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if !uuidPattern.MatchString(input.OrgID) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	now := s.now().UTC()
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	credential := credentialFromCreate(input, now)
	// Codex round 1 finding: IssuanceProvenance was never threaded into
	// the audit record here (pre-existing, since before workload_exchange
	// existed) -- production audit events for a device_authorization or
	// workload_exchange credential silently omitted issuance_provenance,
	// unlike the memory store's equivalent path.
	record := storage.CredentialRecord{Metadata: credential, TokenHash: input.TokenHash, CreatedBy: input.ActorID, IssuanceProvenance: input.IssuanceProvenance}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("begin credential create: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertCredential(ctx, tx, record); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := s.audit.record(ctx, tx, credentialCreatedEvent(record)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("commit credential create: %w", sanitizeDatabaseError(err))
	}
	return credential, nil
}

func (s *credentialStore) rotateCredential(ctx context.Context, input storage.CredentialRotationInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if !uuidPattern.MatchString(input.OrgID) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("begin credential rotation: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	source, err := lockedCredential(ctx, tx, input.OrgID, input.SourceCredentialID)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	now := s.now().UTC()
	if source.RevokedAt != nil {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	if input.Replacement.ExpiresAt != nil && !input.Replacement.ExpiresAt.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	replacement := credentialFromRotation(input.Replacement, source.OrgID, now)
	if err := rotateCredential(ctx, tx, source.OrgID, source.CredentialID, replacement, input.Replacement.TokenHash, input.ActorID, overlapExpiry(now, input.Replacement.Overlap), now); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := s.audit.record(ctx, tx, credentialRotatedEvent(source, replacement, input.ActorID, input.Replacement.Overlap, now)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("commit credential rotation: %w", sanitizeDatabaseError(err))
	}
	return replacement, nil
}

func (s *credentialStore) revokeCredential(ctx context.Context, input storage.CredentialRevocationInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if !uuidPattern.MatchString(input.OrgID) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("begin credential revoke: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	credential, err := lockedCredential(ctx, tx, input.OrgID, input.CredentialID)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if credential.RevokedAt != nil {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `
UPDATE acr.client_credentials
SET revoked_at = $3
WHERE org_id = $1 AND credential_id = $2
	  AND revoked_at IS NULL`, input.OrgID, input.CredentialID, now)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("revoke credential: %w", sanitizeDatabaseError(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("revoke credential rows affected: %w", sanitizeDatabaseError(err))
	}
	if rows != 1 {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	credential.RevokedAt = cloneTime(&now)
	if err := s.audit.record(ctx, tx, credentialRevokedEvent(credential, input.ActorID, now)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("commit credential revoke: %w", sanitizeDatabaseError(err))
	}
	return credential, nil
}

func lockedCredential(ctx context.Context, tx *sql.Tx, orgID, credentialID string) (contractsv1.ClientCredential, error) {
	row := tx.QueryRowContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at, workload_binding_id
FROM acr.client_credentials WHERE org_id = $1 AND credential_id = $2 FOR UPDATE`, orgID, credentialID)
	credential, err := scanCredential(row)
	if err != nil {
		return contractsv1.ClientCredential{}, mapNotFound("lock credential for rotation", err)
	}
	return credential, nil
}

func rotateCredential(ctx context.Context, tx *sql.Tx, orgID, credentialID string, replacement contractsv1.ClientCredential, tokenHash, actorID string, previousValidUntil *time.Time, occurredAt time.Time) error {
	var (
		result sql.Result
		err    error
	)
	if previousValidUntil == nil || !previousValidUntil.After(occurredAt) {
		result, err = tx.ExecContext(ctx, `
UPDATE acr.client_credentials
	SET revoked_at = CASE
	        WHEN revoked_at IS NULL OR revoked_at > $3 THEN $3
	        ELSE revoked_at
	    END,
	    rotated_at = $3
WHERE org_id = $1 AND credential_id = $2
	  AND rotated_at IS NULL`, orgID, credentialID, occurredAt)
	} else {
		result, err = tx.ExecContext(ctx, `
UPDATE acr.client_credentials
	SET expires_at = CASE
        WHEN expires_at IS NULL OR expires_at > $4 THEN $4
        ELSE expires_at
	    END,
	    rotated_at = $3
WHERE org_id = $1 AND credential_id = $2
	  AND rotated_at IS NULL`, orgID, credentialID, occurredAt, *previousValidUntil)
	}
	if err != nil {
		return fmt.Errorf("update previous credential: %w", sanitizeDatabaseError(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("credential rotation rows affected: %w", sanitizeDatabaseError(err))
	}
	if rows != 1 {
		return storage.ErrConflict
	}
	return insertCredential(ctx, tx, storage.CredentialRecord{Metadata: replacement, TokenHash: tokenHash, CreatedBy: actorID})
}

func credentialCreatedEvent(record storage.CredentialRecord) storage.AuditEvent {
	credential := record.Metadata
	metadata := map[string]any{
		"name":              credential.Name,
		"token_prefix":      credential.TokenPrefix,
		"repository_scopes": append([]string(nil), credential.RepositoryScopes...),
		"scopes":            append([]string(nil), credential.Scopes...),
		"expires_at":        credential.ExpiresAt,
	}
	if record.IssuanceProvenance != "" {
		metadata["issuance_provenance"] = string(record.IssuanceProvenance)
	}
	if credential.WorkloadBindingID != nil {
		metadata["workload_binding_id"] = *credential.WorkloadBindingID
	}
	return storage.AuditEvent{
		OrgID: credential.OrgID, ActorType: "user", ActorID: record.CreatedBy,
		Action: storage.AuditActionCredentialCreated, ResourceType: "acr_credential", ResourceID: credential.CredentialID,
		Status: "success", CreatedAt: credential.CreatedAt,
		Metadata: metadata,
	}
}

func credentialRotatedEvent(source, replacement contractsv1.ClientCredential, actorID string, overlap time.Duration, occurredAt time.Time) storage.AuditEvent {
	return storage.AuditEvent{
		OrgID: source.OrgID, ActorType: "user", ActorID: actorID,
		Action: storage.AuditActionCredentialRotated, ResourceType: "acr_credential", ResourceID: source.CredentialID,
		Status: "success", CreatedAt: occurredAt,
		Metadata: map[string]any{
			"replacement_credential_id": replacement.CredentialID,
			"overlap_seconds":           int(overlap.Seconds()),
		},
	}
}

func credentialRevokedEvent(credential contractsv1.ClientCredential, actorID string, occurredAt time.Time) storage.AuditEvent {
	return storage.AuditEvent{
		OrgID: credential.OrgID, ActorType: "user", ActorID: actorID,
		Action: storage.AuditActionCredentialRevoked, ResourceType: "acr_credential", ResourceID: credential.CredentialID,
		Status: "success", CreatedAt: occurredAt,
	}
}

func credentialFromCreate(input storage.CredentialCreateInput, createdAt time.Time) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{
		SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: input.CredentialID, OrgID: input.OrgID, Name: input.Name, TokenPrefix: input.TokenPrefix,
		RepositoryScopes: append([]string(nil), input.RepositoryScopes...), Scopes: append([]string(nil), input.Scopes...), CreatedAt: createdAt, ExpiresAt: cloneTime(input.ExpiresAt),
		WorkloadBindingID: nonEmptyPtr(input.WorkloadBindingID),
	}
}

// nonEmptyPtr returns nil for an empty string and a pointer to value
// otherwise, matching ClientCredential.WorkloadBindingID's "nil means not a
// workload credential" convention.
func nonEmptyPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func credentialFromRotation(input storage.CredentialRotationReplacement, orgID string, createdAt time.Time) contractsv1.ClientCredential {
	return credentialFromCreate(storage.CredentialCreateInput{CredentialID: input.CredentialID, OrgID: orgID, Name: input.Name, TokenPrefix: input.TokenPrefix, RepositoryScopes: input.RepositoryScopes, Scopes: input.Scopes, ExpiresAt: input.ExpiresAt}, createdAt)
}

func overlapExpiry(now time.Time, overlap time.Duration) *time.Time {
	if overlap <= 0 {
		return nil
	}
	value := now.Add(overlap)
	return &value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
