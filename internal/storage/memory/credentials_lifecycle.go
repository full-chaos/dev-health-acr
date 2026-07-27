package memory

import (
	"context"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *credentialStore) createCredential(ctx context.Context, input storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if _, exists := s.byID[input.CredentialID]; exists {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	if _, exists := s.byHash[input.TokenHash]; exists {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	now := s.now().UTC()
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	credential := credentialFromCreate(input, now)
	record := storage.CredentialRecord{
		Metadata: credential, TokenHash: input.TokenHash, CreatedBy: input.ActorID,
		IssuanceProvenance: input.IssuanceProvenance,
	}
	if err := s.recordAudit(ctx, credentialCreatedEvent(record)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	record = cloneRecord(record)
	s.byID[record.Metadata.CredentialID] = record
	s.byHash[record.TokenHash] = record.Metadata.CredentialID
	return cloneCredential(credential), nil
}

func (s *credentialStore) rollbackCredentialRotation(ctx context.Context, input storage.CredentialRotationRollbackInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	now := s.now().UTC()
	if !input.RollbackUntil.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	source, sourceOK := s.byID[input.SourceCredentialID]
	successor, successorOK := s.byID[input.SuccessorCredentialID]
	if !sourceOK || !successorOK || source.Metadata.OrgID != input.OrgID || successor.Metadata.OrgID != input.OrgID ||
		source.Metadata.RevokedAt != nil || successor.Metadata.RevokedAt != nil || successor.RotatedAt != nil ||
		source.Metadata.ExpiresAt == nil || !source.Metadata.ExpiresAt.After(now) ||
		!slices.Equal(source.Metadata.RepositoryScopes, successor.Metadata.RepositoryScopes) || !slices.Equal(source.Metadata.Scopes, successor.Metadata.Scopes) ||
		!s.rotationRelates(source.Metadata.CredentialID, successor.Metadata.CredentialID) {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	if err := s.recordAudit(ctx, credentialRevokedEvent(successor.Metadata, input.ActorID, now)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	successor.Metadata.RevokedAt = ptrTime(now)
	s.byID[successor.Metadata.CredentialID] = cloneRecord(successor)
	return cloneCredential(successor.Metadata), nil
}

func (s *credentialStore) rotationRelates(sourceID, successorID string) bool {
	if s.audit == nil {
		return false
	}
	s.audit.mu.RLock()
	defer s.audit.mu.RUnlock()
	for _, event := range s.audit.events {
		if event.Action != storage.AuditActionCredentialRotated || event.ResourceID != sourceID {
			continue
		}
		if replacement, ok := event.Metadata["replacement_credential_id"].(string); ok && replacement == successorID {
			return true
		}
	}
	return false
}

func (s *credentialStore) rotateCredential(ctx context.Context, input storage.CredentialRotationInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	old, ok := s.byID[input.SourceCredentialID]
	if !ok || old.Metadata.OrgID != input.OrgID {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	now := s.now().UTC()
	if old.RotatedAt != nil {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	if old.Metadata.RevokedAt != nil {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	replacementInput := input.Replacement
	if replacementInput.ExpiresAt != nil && !replacementInput.ExpiresAt.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	if _, exists := s.byID[replacementInput.CredentialID]; exists {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	if _, exists := s.byHash[replacementInput.TokenHash]; exists {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	replacement := credentialFromRotation(replacementInput, old.Metadata.OrgID, now)
	previousValidUntil := overlapExpiry(now, replacementInput.Overlap)
	if err := s.recordAudit(ctx, credentialRotatedEvent(old.Metadata, replacement, input.ActorID, replacementInput.Overlap, now)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if previousValidUntil == nil || !previousValidUntil.After(now) {
		old.Metadata.RevokedAt = ptrTime(now)
	} else if old.Metadata.ExpiresAt == nil || previousValidUntil.Before(*old.Metadata.ExpiresAt) {
		old.Metadata.ExpiresAt = ptrTime(*previousValidUntil)
	}
	old.RotatedAt = ptrTime(now)
	s.byID[input.SourceCredentialID] = cloneRecord(old)
	replacementRecord := cloneRecord(storage.CredentialRecord{Metadata: replacement, TokenHash: replacementInput.TokenHash, CreatedBy: input.ActorID})
	s.byID[replacement.CredentialID] = replacementRecord
	s.byHash[replacementInput.TokenHash] = replacement.CredentialID
	return cloneCredential(replacement), nil
}

func (s *credentialStore) revokeCredential(ctx context.Context, input storage.CredentialRevocationInput) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	record, ok := s.byID[input.CredentialID]
	if !ok || record.Metadata.OrgID != input.OrgID {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	if record.Metadata.RevokedAt != nil {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	now := s.now().UTC()
	if err := s.recordAudit(ctx, credentialRevokedEvent(record.Metadata, input.ActorID, now)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	record.Metadata.RevokedAt = ptrTime(now)
	s.byID[input.CredentialID] = cloneRecord(record)
	return cloneCredential(record.Metadata), nil
}

func (s *credentialStore) recordAudit(ctx context.Context, event storage.AuditEvent) error {
	if s.audit == nil {
		return storage.ErrInvalidCredentialLifecycle
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.audit.mu.Lock()
	defer s.audit.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.audit.recordLocked(event)
}
