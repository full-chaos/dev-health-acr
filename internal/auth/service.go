package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type ServiceOptions struct {
	Now                  func() time.Time
	GenerateToken        func() (string, error)
	GenerateCredentialID func() (string, error)
	MaximumOverlap       time.Duration
}

type Service struct {
	store                storage.CredentialStore
	audit                storage.AuditStore
	now                  func() time.Time
	generateToken        func() (string, error)
	generateCredentialID func() (string, error)
	maximumOverlap       time.Duration
}

func NewService(store storage.CredentialStore, audit storage.AuditStore, options ServiceOptions) (*Service, error) {
	if store == nil {
		return nil, errors.New("credential store is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.GenerateToken == nil {
		options.GenerateToken = GenerateToken
	}
	if options.GenerateCredentialID == nil {
		options.GenerateCredentialID = GenerateCredentialID
	}
	if options.MaximumOverlap <= 0 {
		options.MaximumOverlap = 15 * time.Minute
	}
	return &Service{
		store:                store,
		audit:                audit,
		now:                  options.Now,
		generateToken:        options.GenerateToken,
		generateCredentialID: options.GenerateCredentialID,
		maximumOverlap:       options.MaximumOverlap,
	}, nil
}

func (s *Service) Create(ctx context.Context, request CreateCredentialRequest) (IssuedCredential, error) {
	normalized, err := normalizeCreateRequest(request)
	if err != nil {
		return IssuedCredential{}, err
	}
	now := s.now().UTC()
	if normalized.ExpiresAt != nil && !normalized.ExpiresAt.After(now) {
		return IssuedCredential{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidCredential)
	}
	token, credential, record, err := s.issueRecord(normalized, now)
	if err != nil {
		return IssuedCredential{}, err
	}
	if err := s.store.Create(ctx, record); err != nil {
		return IssuedCredential{}, fmt.Errorf("create credential: %w", err)
	}
	s.recordAudit(ctx, storage.AuditEvent{
		OrgID: normalized.OrgID, ActorType: "user", ActorID: normalized.CreatedBy,
		Action: "credential_created", ResourceType: "acr_credential", ResourceID: credential.CredentialID,
		Status: "success", Metadata: safeCredentialMetadata(credential), CreatedAt: now,
	})
	return IssuedCredential{Credential: credential, Token: token}, nil
}

func (s *Service) List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	if orgID == "" {
		return nil, fmt.Errorf("%w: org_id is required", ErrInvalidCredential)
	}
	return s.store.List(ctx, orgID)
}

func (s *Service) Rotate(ctx context.Context, request RotateCredentialRequest) (IssuedCredential, error) {
	if request.Overlap < 0 || request.Overlap > s.maximumOverlap {
		return IssuedCredential{}, fmt.Errorf("%w: overlap must be between zero and %s", ErrInvalidCredential, s.maximumOverlap)
	}
	current, err := s.store.GetByID(ctx, request.OrgID, request.CredentialID)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("load credential for rotation: %w", err)
	}
	create := CreateCredentialRequest{
		OrgID: request.OrgID, Name: request.Name, RepositoryScopes: request.RepositoryScopes,
		Scopes: request.Scopes, CreatedBy: request.CreatedBy, ExpiresAt: request.ExpiresAt,
	}
	if create.Name == "" {
		create.Name = current.Name
	}
	if len(create.RepositoryScopes) == 0 {
		create.RepositoryScopes = current.RepositoryScopes
	}
	if len(create.Scopes) == 0 {
		create.Scopes = current.Scopes
	}
	normalized, err := normalizeCreateRequest(create)
	if err != nil {
		return IssuedCredential{}, err
	}
	now := s.now().UTC()
	if normalized.ExpiresAt != nil && !normalized.ExpiresAt.After(now) {
		return IssuedCredential{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidCredential)
	}
	token, credential, record, err := s.issueRecord(normalized, now)
	if err != nil {
		return IssuedCredential{}, err
	}
	var previousValidUntil *time.Time
	if request.Overlap > 0 {
		value := now.Add(request.Overlap)
		previousValidUntil = &value
	}
	if err := s.store.Rotate(ctx, request.OrgID, request.CredentialID, record, previousValidUntil); err != nil {
		return IssuedCredential{}, fmt.Errorf("rotate credential: %w", err)
	}
	s.recordAudit(ctx, storage.AuditEvent{
		OrgID: request.OrgID, ActorType: "user", ActorID: request.CreatedBy,
		Action: "credential_rotated", ResourceType: "acr_credential", ResourceID: request.CredentialID,
		Status: "success", Metadata: map[string]any{
			"replacement_credential_id": credential.CredentialID,
			"overlap_seconds":           int(request.Overlap.Seconds()),
		}, CreatedAt: now,
	})
	return IssuedCredential{Credential: credential, Token: token}, nil
}

func (s *Service) Revoke(ctx context.Context, orgID, credentialID, actorID string) (contractsv1.ClientCredential, error) {
	now := s.now().UTC()
	credential, err := s.store.Revoke(ctx, orgID, credentialID, now)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("revoke credential: %w", err)
	}
	s.recordAudit(ctx, storage.AuditEvent{
		OrgID: orgID, ActorType: "user", ActorID: actorID,
		Action: "credential_revoked", ResourceType: "acr_credential", ResourceID: credentialID,
		Status: "success", CreatedAt: now,
	})
	return credential, nil
}

func (s *Service) issueRecord(request CreateCredentialRequest, now time.Time) (string, contractsv1.ClientCredential, storage.CredentialRecord, error) {
	token, err := s.generateToken()
	if err != nil {
		return "", contractsv1.ClientCredential{}, storage.CredentialRecord{}, err
	}
	if !IsTokenShapeValid(token) {
		return "", contractsv1.ClientCredential{}, storage.CredentialRecord{}, errors.New("token generator returned an invalid ACR token")
	}
	credentialID, err := s.generateCredentialID()
	if err != nil {
		return "", contractsv1.ClientCredential{}, storage.CredentialRecord{}, err
	}
	credential := contractsv1.ClientCredential{
		SchemaVersion: ClientCredentialSchema(), CredentialID: credentialID, Name: request.Name,
		TokenPrefix: DisplayPrefix(token), OrgID: request.OrgID,
		RepositoryScopes: append([]string(nil), request.RepositoryScopes...),
		Scopes:           append([]string(nil), request.Scopes...), CreatedAt: now, ExpiresAt: cloneTime(request.ExpiresAt),
	}
	record := storage.CredentialRecord{Metadata: credential, TokenHash: HashToken(token), CreatedBy: request.CreatedBy}
	return token, credential, record, nil
}

func ClientCredentialSchema() string { return contractsv1.ClientCredentialSchema }

func (s *Service) recordAudit(ctx context.Context, event storage.AuditEvent) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(context.WithoutCancel(ctx), event)
}

func safeCredentialMetadata(credential contractsv1.ClientCredential) map[string]any {
	return map[string]any{
		"name":              credential.Name,
		"token_prefix":      credential.TokenPrefix,
		"repository_scopes": append([]string(nil), credential.RepositoryScopes...),
		"scopes":            append([]string(nil), credential.Scopes...),
		"expires_at":        credential.ExpiresAt,
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
