package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const selfRotationRedacted = "auth.SelfRotation{redacted}"

type SelfRotationReceipt struct {
	SourceCredentialID    string
	SuccessorCredentialID string
	RollbackUntil         time.Time
}

type SelfRotation struct {
	Issued  IssuedCredential
	Receipt SelfRotationReceipt
}

type SelfRevocation struct {
	CredentialID string
}

type credentialSnapshot struct {
	CredentialID     string
	RepositoryScopes []string
	Scopes           []string
	CreatedAt        time.Time
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
}

func (s *Service) RotateSelf(ctx context.Context, principal storage.Principal) (SelfRotation, error) {
	source, err := s.authenticatedCredential(ctx, principal)
	if err != nil {
		return SelfRotation{}, err
	}
	expiresAt := s.now().UTC().Add(DeviceCredentialLifetime)
	issued, err := s.Rotate(ctx, RotateCredentialRequest{
		OrgID:        principal.OrgID,
		CredentialID: principal.CredentialID,
		CreatedBy:    principal.Subject,
		ExpiresAt:    &expiresAt,
		Overlap:      storage.MaximumCredentialOverlap,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return SelfRotation{}, ErrStaleSelfCredential
		}
		return SelfRotation{}, err
	}
	persistedSource, err := s.store.GetByID(ctx, principal.OrgID, source.CredentialID)
	if err != nil {
		return SelfRotation{}, fmt.Errorf("load persisted self-rotation source: %w", err)
	}
	if persistedSource.ExpiresAt == nil {
		return SelfRotation{}, ErrInvalidCredential
	}
	return SelfRotation{
		Issued: issued,
		Receipt: SelfRotationReceipt{
			SourceCredentialID:    principal.CredentialID,
			SuccessorCredentialID: issued.Credential.CredentialID,
			RollbackUntil:         *persistedSource.ExpiresAt,
		},
	}, nil
}

func (s *Service) RollbackSelfRotation(
	ctx context.Context,
	principal storage.Principal,
	receipt SelfRotationReceipt,
) (SelfRevocation, error) {
	successor, err := s.authenticatedCredential(ctx, principal)
	if err != nil {
		return SelfRevocation{}, err
	}
	if strings.TrimSpace(receipt.SourceCredentialID) == "" ||
		strings.TrimSpace(receipt.SuccessorCredentialID) != principal.CredentialID ||
		receipt.SourceCredentialID == principal.CredentialID {
		return SelfRevocation{}, ErrInvalidCredential
	}
	source, err := s.store.GetByID(ctx, principal.OrgID, receipt.SourceCredentialID)
	if err != nil {
		return SelfRevocation{}, fmt.Errorf("load self-rotation source: %w", err)
	}
	if source.RevokedAt != nil || source.ExpiresAt == nil || !source.ExpiresAt.After(s.now().UTC()) ||
		successor.CreatedAt.Before(source.CreatedAt) ||
		source.ExpiresAt.Before(successor.CreatedAt) || source.ExpiresAt.After(successor.CreatedAt.Add(storage.MaximumCredentialOverlap)) ||
		!slices.Equal(successor.RepositoryScopes, source.RepositoryScopes) || !slices.Equal(successor.Scopes, source.Scopes) {
		return SelfRevocation{}, ErrInvalidCredential
	}
	return s.RevokeSelf(ctx, principal)
}

func (s *Service) RevokeSelf(ctx context.Context, principal storage.Principal) (SelfRevocation, error) {
	if _, err := s.authenticatedCredential(ctx, principal); err != nil {
		return SelfRevocation{}, err
	}
	credential, err := s.Revoke(ctx, principal.OrgID, principal.CredentialID, principal.Subject)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return SelfRevocation{}, ErrStaleSelfCredential
		}
		return SelfRevocation{}, err
	}
	return SelfRevocation{CredentialID: credential.CredentialID}, nil
}

func (s *Service) authenticatedCredential(ctx context.Context, principal storage.Principal) (credentialSnapshot, error) {
	if s == nil || s.store == nil || s.now == nil || storage.IsNil(ctx) ||
		principal.AuthenticationMethod != storage.AuthenticationMethodCredential ||
		strings.TrimSpace(principal.Subject) == "" || principal.Subject != principal.CredentialID ||
		strings.TrimSpace(principal.OrgID) == "" {
		return credentialSnapshot{}, ErrInvalidCredential
	}
	repositories, err := NormalizeRepositoryScopes(principal.RepositoryScopes)
	if err != nil || !slices.Equal(repositories, principal.RepositoryScopes) {
		return credentialSnapshot{}, ErrInvalidCredential
	}
	permissions, err := normalizeScopes(principal.Permissions)
	if err != nil || !slices.Equal(permissions, principal.Permissions) {
		return credentialSnapshot{}, ErrInvalidCredential
	}
	credential, err := s.store.GetByID(ctx, principal.OrgID, principal.CredentialID)
	if err != nil {
		return credentialSnapshot{}, fmt.Errorf("load authenticated credential: %w", err)
	}
	if credential.RevokedAt != nil || credential.ExpiresAt != nil && !credential.ExpiresAt.After(s.now().UTC()) ||
		!slices.Equal(credential.RepositoryScopes, repositories) || !slices.Equal(credential.Scopes, permissions) {
		return credentialSnapshot{}, ErrInvalidCredential
	}
	return credentialSnapshot{
		CredentialID: credential.CredentialID, RepositoryScopes: append([]string(nil), credential.RepositoryScopes...),
		Scopes: append([]string(nil), credential.Scopes...), CreatedAt: credential.CreatedAt,
		ExpiresAt: cloneTime(credential.ExpiresAt), RevokedAt: cloneTime(credential.RevokedAt),
	}, nil
}

func (SelfRotation) String() string { return selfRotationRedacted }

func (SelfRotation) GoString() string { return selfRotationRedacted }

func (SelfRotation) LogValue() slog.Value { return slog.StringValue(selfRotationRedacted) }
