package memory

import (
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func cloneRecord(record storage.CredentialRecord) storage.CredentialRecord {
	record.Metadata = cloneCredential(record.Metadata)
	record.RotatedAt = cloneTime(record.RotatedAt)
	return record
}

func cloneCredential(value contractsv1.ClientCredential) contractsv1.ClientCredential {
	value.RepositoryScopes = append([]string(nil), value.RepositoryScopes...)
	value.Scopes = append([]string(nil), value.Scopes...)
	value.ExpiresAt = cloneTime(value.ExpiresAt)
	value.RevokedAt = cloneTime(value.RevokedAt)
	value.LastUsedAt = cloneTime(value.LastUsedAt)
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ptrTime(value time.Time) *time.Time {
	copy := value
	return &copy
}

func credentialCreatedEvent(record storage.CredentialRecord) storage.AuditEvent {
	credential := record.Metadata
	metadata := map[string]any{"name": credential.Name, "token_prefix": credential.TokenPrefix, "repository_scopes": append([]string(nil), credential.RepositoryScopes...), "scopes": append([]string(nil), credential.Scopes...), "expires_at": cloneTime(credential.ExpiresAt)}
	if record.IssuanceProvenance != "" {
		metadata["issuance_provenance"] = string(record.IssuanceProvenance)
	}
	if credential.WorkloadBindingID != nil {
		metadata["workload_binding_id"] = *credential.WorkloadBindingID
	}
	return storage.AuditEvent{OrgID: credential.OrgID, ActorType: "user", ActorID: record.CreatedBy, Action: storage.AuditActionCredentialCreated, ResourceType: "acr_credential", ResourceID: credential.CredentialID, Status: "success", CreatedAt: credential.CreatedAt, Metadata: metadata}
}

func credentialRotatedEvent(source, replacement contractsv1.ClientCredential, actorID string, overlap time.Duration, occurredAt time.Time) storage.AuditEvent {
	return storage.AuditEvent{OrgID: source.OrgID, ActorType: "user", ActorID: actorID, Action: storage.AuditActionCredentialRotated, ResourceType: "acr_credential", ResourceID: source.CredentialID, Status: "success", CreatedAt: occurredAt, Metadata: map[string]any{"replacement_credential_id": replacement.CredentialID, "overlap_seconds": int(overlap.Seconds())}}
}

func credentialRevokedEvent(credential contractsv1.ClientCredential, actorID string, occurredAt time.Time) storage.AuditEvent {
	return storage.AuditEvent{OrgID: credential.OrgID, ActorType: "user", ActorID: actorID, Action: storage.AuditActionCredentialRevoked, ResourceType: "acr_credential", ResourceID: credential.CredentialID, Status: "success", CreatedAt: occurredAt}
}

func credentialFromCreate(input storage.CredentialCreateInput, createdAt time.Time) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{
		SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: input.CredentialID, OrgID: input.OrgID, Name: input.Name, TokenPrefix: input.TokenPrefix,
		RepositoryScopes: append([]string(nil), input.RepositoryScopes...), Scopes: append([]string(nil), input.Scopes...), CreatedAt: createdAt, ExpiresAt: cloneTime(input.ExpiresAt),
		WorkloadBindingID: nonEmptyPtr(input.WorkloadBindingID),
	}
}

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
