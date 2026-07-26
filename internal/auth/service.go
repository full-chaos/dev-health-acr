package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	store                credentialLifecycle
	now                  func() time.Time
	generateToken        func() (string, error)
	generateCredentialID func() (string, error)
	maximumOverlap       time.Duration
}

type credentialLifecycle interface {
	Validate() error
	CreateCredential(context.Context, storage.CredentialCreateInput) (contractsv1.ClientCredential, error)
	RotateCredential(context.Context, storage.CredentialRotationInput) (contractsv1.ClientCredential, error)
	RevokeCredential(context.Context, storage.CredentialRevocationInput) (contractsv1.ClientCredential, error)
	List(context.Context, string) ([]contractsv1.ClientCredential, error)
	GetByID(context.Context, string, string) (contractsv1.ClientCredential, error)
}

type PreparedCredential struct {
	complete func(contractsv1.ClientCredential) IssuedCredential
	input    storage.CredentialCreateInput
}

const preparedCredentialRedacted = "auth.PreparedCredential{redacted}"

func NewService(store *storage.CredentialLifecycle, options ServiceOptions) (*Service, error) {
	return newService(store, options)
}

func newService(store credentialLifecycle, options ServiceOptions) (*Service, error) {
	if store == nil {
		return nil, storage.ErrInvalidCredentialLifecycle
	}
	if err := store.Validate(); err != nil {
		return nil, err
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
		now:                  options.Now,
		generateToken:        options.GenerateToken,
		generateCredentialID: options.GenerateCredentialID,
		maximumOverlap:       options.MaximumOverlap,
	}, nil
}

func (s *Service) Create(ctx context.Context, request CreateCredentialRequest) (IssuedCredential, error) {
	prepared, err := s.PrepareCreate(request)
	if err != nil {
		return IssuedCredential{}, err
	}
	credential, err := s.store.CreateCredential(ctx, prepared.StorageInput())
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("create credential: %w", err)
	}
	issued, err := prepared.Complete(credential)
	if err != nil {
		return IssuedCredential{}, err
	}
	return issued, nil
}

func (s *Service) PrepareCreate(request CreateCredentialRequest) (PreparedCredential, error) {
	normalized, err := normalizeCreateRequest(request)
	if err != nil {
		return PreparedCredential{}, err
	}
	now := s.now().UTC()
	if normalized.ExpiresAt != nil && !normalized.ExpiresAt.After(now) {
		return PreparedCredential{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidCredential)
	}
	token, input, err := s.issueCreateInput(normalized)
	if err != nil {
		return PreparedCredential{}, err
	}
	return PreparedCredential{
		complete: func(credential contractsv1.ClientCredential) IssuedCredential {
			return IssuedCredential{Credential: credential, Token: token}
		},
		input: input,
	}, nil
}

func (p PreparedCredential) StorageInput() storage.CredentialCreateInput {
	input := p.input
	input.RepositoryScopes = append([]string(nil), p.input.RepositoryScopes...)
	input.Scopes = append([]string(nil), p.input.Scopes...)
	input.ExpiresAt = cloneTime(p.input.ExpiresAt)
	return input
}

func (p PreparedCredential) Complete(credential contractsv1.ClientCredential) (IssuedCredential, error) {
	if p.complete == nil || credential.CredentialID != p.input.CredentialID || credential.OrgID != p.input.OrgID {
		return IssuedCredential{}, ErrInvalidCredential
	}
	return p.complete(credential), nil
}

func (PreparedCredential) String() string { return preparedCredentialRedacted }

func (PreparedCredential) GoString() string { return preparedCredentialRedacted }

func (PreparedCredential) LogValue() slog.Value { return slog.StringValue(preparedCredentialRedacted) }

func (PreparedCredential) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }

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
	request.OrgID = strings.TrimSpace(request.OrgID)
	request.CredentialID = strings.TrimSpace(request.CredentialID)
	request.CreatedBy = strings.TrimSpace(request.CreatedBy)
	if request.OrgID == "" || request.CredentialID == "" || request.CreatedBy == "" {
		return IssuedCredential{}, fmt.Errorf("%w: org_id, credential_id, and actor are required", ErrInvalidCredential)
	}
	current, err := s.store.GetByID(ctx, request.OrgID, request.CredentialID)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("load credential for rotation: %w", err)
	}
	now := s.now().UTC()
	if current.RevokedAt != nil {
		return IssuedCredential{}, fmt.Errorf("%w: source credential is no longer active", ErrInvalidCredential)
	}
	create := CreateCredentialRequest{
		OrgID: current.OrgID, Name: request.Name, RepositoryScopes: request.RepositoryScopes,
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
	if normalized.ExpiresAt != nil && !normalized.ExpiresAt.After(now) {
		return IssuedCredential{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidCredential)
	}
	token, input, err := s.issueRotationInput(normalized, request.Overlap)
	if err != nil {
		return IssuedCredential{}, err
	}
	credential, err := s.store.RotateCredential(ctx, storage.CredentialRotationInput{
		OrgID: request.OrgID, SourceCredentialID: request.CredentialID, ActorID: request.CreatedBy, Replacement: input,
	})
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("rotate credential: %w", err)
	}
	return IssuedCredential{Credential: credential, Token: token}, nil
}

func (s *Service) Revoke(ctx context.Context, orgID, credentialID, actorID string) (contractsv1.ClientCredential, error) {
	orgID = strings.TrimSpace(orgID)
	credentialID = strings.TrimSpace(credentialID)
	actorID = strings.TrimSpace(actorID)
	if orgID == "" || credentialID == "" || actorID == "" {
		return contractsv1.ClientCredential{}, fmt.Errorf("%w: org_id, credential_id, and actor are required", ErrInvalidCredential)
	}
	credential, err := s.store.RevokeCredential(ctx, storage.CredentialRevocationInput{OrgID: orgID, CredentialID: credentialID, ActorID: actorID})
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("revoke credential: %w", err)
	}
	return credential, nil
}

func (s *Service) issueCreateInput(request CreateCredentialRequest) (string, storage.CredentialCreateInput, error) {
	token, err := s.generateToken()
	if err != nil {
		return "", storage.CredentialCreateInput{}, err
	}
	if !IsTokenShapeValid(token) {
		return "", storage.CredentialCreateInput{}, errors.New("token generator returned an invalid ACR token")
	}
	credentialID, err := s.generateCredentialID()
	if err != nil {
		return "", storage.CredentialCreateInput{}, err
	}
	return token, storage.CredentialCreateInput{
		CredentialID: credentialID, OrgID: request.OrgID, Name: request.Name, TokenPrefix: DisplayPrefix(token), TokenHash: HashToken(token),
		RepositoryScopes: append([]string(nil), request.RepositoryScopes...), Scopes: append([]string(nil), request.Scopes...),
		ActorID: request.CreatedBy, ExpiresAt: cloneTime(request.ExpiresAt),
	}, nil

}

func (s *Service) issueRotationInput(request CreateCredentialRequest, overlap time.Duration) (string, storage.CredentialRotationReplacement, error) {
	token, input, err := s.issueCreateInput(request)
	if err != nil {
		return "", storage.CredentialRotationReplacement{}, err
	}
	return token, storage.CredentialRotationReplacement{
		CredentialID: input.CredentialID, Name: input.Name, TokenPrefix: input.TokenPrefix, TokenHash: input.TokenHash,
		RepositoryScopes: input.RepositoryScopes, Scopes: input.Scopes, ExpiresAt: input.ExpiresAt, Overlap: overlap, Immediate: overlap == 0,
	}, nil
}

func ClientCredentialSchema() string { return contractsv1.ClientCredentialSchema }

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
